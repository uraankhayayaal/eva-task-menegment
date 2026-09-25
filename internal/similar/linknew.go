package similar

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"evasimilar/internal/eva"
)

// linkCommentMarker prefixes every comment posted by the linker, so repeated
// runs (e.g. a missing watermark) can detect the task was already commented.
const linkCommentMarker = "Автолинкер:"

// LinkNewTasks polls Eva for tasks created after watermark and processes every
// one of them. Depending on LINKER_LINK_MODE it either creates relations
// ("link") or posts a comment listing the similar tasks ("comment", default).
// It returns the newest cmf_created_at seen so the caller can persist it as
// the new watermark.
//
// The watermark advances past tasks that failed to process (they are logged
// and can be retried manually via /link/<id>), so a task that always fails
// (e.g. has no text) does not block the scan forever.
func (s *Service) LinkNewTasks(ctx context.Context, watermark time.Time) (time.Time, error) {
	fields := []string{}
	for _, f := range s.cfg.TaskListFields {
		fields = addField(fields, f)
	}
	fields = addField(fields, s.cfg.TaskIDField)
	fields = addField(fields, "cmf_created_at")
	filter := []any{[]any{"cmf_created_at", ">", watermark.Format(time.RFC3339)}}
	pageSize := s.cfg.IndexerPageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	newest := watermark
	processed := 0
	failed := 0
	offset := 0
	for {
		if ctx.Err() != nil {
			return newest, ctx.Err()
		}
		tasks, _, err := eva.ListTasks(ctx, s.eva, s.cfg.TaskListMethod, filter, fields, offset, pageSize)
		if err != nil {
			return newest, err
		}
		if len(tasks) == 0 {
			break
		}
		for _, t := range tasks {
			id := strp(lookup(t, s.cfg.TaskIDField))
			if id == "" {
				continue
			}
			if v, ok := lookup(t, "cmf_created_at"); ok {
				if ts := evaTime(v); !ts.IsZero() && ts.After(newest) {
					newest = ts
				}
			}
			err := s.processNewTask(ctx, id)
			if err != nil {
				failed++
				s.log.Error("link new task", "id", id, "mode", s.cfg.LinkerLinkMode, "err", err)
				continue
			}
			processed++
			s.log.Info("processed new task", "id", id, "mode", s.cfg.LinkerLinkMode)
		}
		offset += len(tasks)
		if len(tasks) < pageSize {
			break
		}
	}
	s.log.Info("link scan done",
		"since", watermark.Format(time.RFC3339),
		"processed", processed,
		"failed", failed,
		"newest", newest.Format(time.RFC3339),
	)
	return newest, nil
}

// processNewTask handles a single newly created task according to the
// configured LINKER_LINK_MODE.
func (s *Service) processNewTask(ctx context.Context, id string) error {
	switch s.cfg.LinkerLinkMode {
	case "link":
		return s.linkNewTask(ctx, id)
	default:
		return s.commentNewTask(ctx, id)
	}
}

// linkNewTask indexes the task and creates Eva relations to similar tasks
// (kept as the fallback "LINKER_LINK_MODE=link" behaviour).
func (s *Service) linkNewTask(ctx context.Context, id string) error {
	_, matches, err := s.FindAndLink(ctx, id)
	if err != nil {
		return err
	}
	s.log.Info("linked new task", "id", id, "matches", len(matches), "dry_run", s.cfg.LinksDryRun)
	return nil
}

// commentNewTask indexes the task, finds similar tasks and posts a comment on
// the task itself listing them, without creating any Eva relations.
func (s *Service) commentNewTask(ctx context.Context, id string) error {
	doc, matches, err := s.IndexAndFind(ctx, id)
	if err != nil {
		return err
	}
	s.log.Info("found candidates", "id", id, "matches", len(matches))
	if len(matches) == 0 {
		return nil
	}
	return s.linkByComment(ctx, doc, matches)
}

// linkByComment writes a comment listing the matches, skipping the write when
// the linker has already commented on the task or when LINKS_DRY_RUN is set.
func (s *Service) linkByComment(ctx context.Context, doc *TaskDoc, matches []Match) error {
	parent := doc.ID
	if s.cfg.TaskCommentParentPrefix != "" && !strings.HasPrefix(parent, s.cfg.TaskCommentParentPrefix) {
		parent = s.cfg.TaskCommentParentPrefix + parent
	}
	comments, err := eva.ListComments(ctx, s.eva, s.cfg.TaskCommentsMethod, "", s.cfg.TaskCommentsFields, parent)
	if err != nil {
		return err
	}
	for _, c := range comments {
		if text := strp(lookup(c, s.cfg.TaskCommentField)); strings.Contains(text, linkCommentMarker) {
			s.log.Info("already commented", "id", doc.ID)
			return nil
		}
	}
	text := CommentText(matches)
	if s.cfg.LinksDryRun {
		s.log.Info("dry run: would comment", "id", doc.ID, "parent", parent, "text", text)
		return nil
	}
	if err := eva.CreateComment(ctx, s.eva, s.cfg.TaskCommentCreateMethod, parent, text); err != nil {
		return err
	}
	s.log.Info("commented", "id", doc.ID, "matches", len(matches))
	return nil
}

// CommentText renders the HTML comment body the linker posts on a task,
// listing its similar tasks. Starts with linkCommentMarker so repeated runs
// are idempotent.
func CommentText(matches []Match) string {
	var b strings.Builder
	b.WriteString("<p><b>" + linkCommentMarker + "</b> задача связана с похожими задачами:</p><ul>")
	for _, m := range matches {
		label := m.Code
		if label == "" {
			label = m.ID
		}
		b.WriteString("<li>")
		b.WriteString(html.EscapeString(label))
		b.WriteString(" &mdash; ")
		b.WriteString(html.EscapeString(m.Title))
		b.WriteString(fmt.Sprintf(" (%.4f)</li>", m.Score))
	}
	b.WriteString("</ul>")
	return b.String()
}

func addField(fields []string, f string) []string {
	for _, e := range fields {
		if e == f {
			return fields
		}
	}
	if f == "" {
		return fields
	}
	return append(fields, f)
}

// evaTime parses a cmf_created_at value in the date-time layouts Eva may use.
func evaTime(v any) time.Time {
	s := fmt.Sprint(v)
	if s == "" || s == "<nil>" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}