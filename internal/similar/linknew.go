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

// LinkNewTasks polls Eva for tasks whose cmf_modified_at is newer than the
// watermark and processes every one of them. Scanning on cmf_modified_at
// (which is >= cmf_created_at) picks up both freshly created tasks and tasks
// whose title/description were filled in later — a task created title-only at
// creation becomes richer minutes or hours afterwards.
//
// Depending on LINKER_LINK_MODE it either creates relations ("link") or posts
// an «Автолинкер» comment listing the similar tasks ("comment", default). In
// comment mode an already existing «Автолинкер» comment is updated in place
// when the task content changed (see commentTaskCore/writeComment), so edits
// do not spam new comments.
//
// It returns the newest cmf_modified_at seen so the caller can persist it as
// the new watermark. The watermark advances past tasks that failed to process
// (they are logged and can be retried manually via /comment/<id>), so a task
// that always fails (e.g. has no text) does not block the scan forever.
func (s *Service) LinkNewTasks(ctx context.Context, watermark time.Time) (time.Time, error) {
	fields := []string{}
	for _, f := range s.cfg.TaskListFields {
		fields = addField(fields, f)
	}
	fields = addField(fields, s.cfg.TaskIDField)
	fields = addField(fields, s.cfg.TaskModifiedField)
	fields = addField(fields, s.cfg.TaskCodeField)
	filter := []any{[]any{s.cfg.TaskModifiedField, ">", watermark.Format(time.RFC3339)}}
	pageSize := s.cfg.IndexerPageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	newest := watermark
	processed := 0
	indexed := 0
	failed := 0
	skipped := 0
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
			if ts := lookupTime(t, s.cfg.TaskModifiedField); !ts.IsZero() && ts.After(newest) {
				newest = ts
			}
			if !s.taskActionAllowed(t) {
				skipped++
				if _, err := s.indexRawTask(ctx, t); err != nil {
					failed++
					s.log.Error("index task", "id", id, "code", strp(lookup(t, s.cfg.TaskCodeField)), "err", err)
					continue
				}
				indexed++
				s.log.Debug("indexed task without actions by code policy", "id", id, "code", strp(lookup(t, s.cfg.TaskCodeField)))
				continue
			}
			err := s.processNewTask(ctx, id)
			if err != nil {
				failed++
				s.log.Error("process task", "id", id, "mode", s.cfg.LinkerLinkMode, "err", err)
				continue
			}
			processed++
			s.log.Info("processed task", "id", id, "mode", s.cfg.LinkerLinkMode)
		}
		offset += len(tasks)
		if len(tasks) < pageSize {
			break
		}
	}
	s.log.Info("link scan done",
		"since", watermark.Format(time.RFC3339),
		"processed", processed,
		"indexed", indexed,
		"failed", failed,
		"skipped", skipped,
		"newest", newest.Format(time.RFC3339),
	)
	return newest, nil
}

// commentNewTask handles a single task according to the configured
// LINKER_LINK_MODE.
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
	_, _, err := s.CommentTask(ctx, id)
	return err
}

// CommentTask is the on-demand entrypoint: make sure the task is embedded and
// its «Автолинкер» comment lists the current matches, without creating any Eva
// relations. It is idempotent and livelock-safe: when the stored content hash
// equals the current task content (e.g. cmf_modified_at moved only because our
// own comment refresh bumped it), nothing is embedded or written. Direct
// content edits (description added, title renamed) re-embed and refresh the
// comment in place. Respects LINKS_DRY_RUN.
func (s *Service) CommentTask(ctx context.Context, taskID any) (*TaskDoc, []Match, error) {
	doc, matches, _, err := s.commentTaskCore(ctx, taskID)
	return doc, matches, err
}

// commentTaskCore handles the embed-and-comment flow with a content-hash guard
// upfront. The content-diff (not a timestamp) decides whether to act, because
// refreshing the «Автолинкер» comment itself advances cmf_modified_at; a
// timestamp-based guard would re-process the same reflected content forever.
func (s *Service) commentTaskCore(ctx context.Context, taskID any) (*TaskDoc, []Match, bool, error) {
	raw, err := s.FindTask(ctx, taskID)
	if err != nil {
		return nil, nil, false, err
	}
	doc, err := s.normalize(ctx, raw)
	if err != nil {
		return nil, nil, false, err
	}
	if doc.ContentHash != "" {
		prev, ok, err := s.qd.LoadPayload(ctx, s.cfg.QdrantCollection, map[string]any{
			"must": []any{
				map[string]any{"key": "eva_id", "match": map[string]any{"value": doc.ID}},
				map[string]any{"has_id": []uint64{chunkPointID(doc.ID, 0)}},
			},
		})
		if err != nil {
			s.log.Warn("read stored content hash", "id", doc.ID, "err", err)
		} else if ok && str(prev["content_hash"]) == doc.ContentHash {
			s.log.Info("content unchanged; skipping comment", "id", doc.ID)
			return doc, nil, false, nil
		}
	}
	if err := s.embedAndStore(ctx, []*TaskDoc{doc}); err != nil {
		return doc, nil, false, err
	}
	if !s.taskActionAllowed(raw) {
		s.log.Debug("indexed task without comment by code policy", "id", doc.ID, "code", doc.Code)
		return doc, nil, false, nil
	}
	matches, err := s.FindSimilar(ctx, doc)
	if err != nil {
		return doc, nil, false, err
	}
	if len(matches) == 0 {
		s.log.Info("no matches to comment", "id", doc.ID)
		return doc, matches, false, nil
	}
	if err := s.writeComment(ctx, doc, matches); err != nil {
		return doc, matches, false, err
	}
	return doc, matches, true, nil
}

// writeComment writes the «Автолинкер» comment for a task after its content
// changed: it refreshes the newest existing linker comment in place, or posts
// a new one when none exists yet. Respects LINKS_DRY_RUN.
func (s *Service) writeComment(ctx context.Context, doc *TaskDoc, matches []Match) error {
	parent := doc.ID
	if s.cfg.TaskCommentParentPrefix != "" && !strings.HasPrefix(parent, s.cfg.TaskCommentParentPrefix) {
		parent = s.cfg.TaskCommentParentPrefix + parent
	}
	fields := addField(addField(s.cfg.TaskCommentsFields, "id"), "cmf_created_at")
	comments, err := eva.ListComments(ctx, s.eva, s.cfg.TaskCommentsMethod, "", fields, parent)
	if err != nil {
		return err
	}
	updID, _ := pickLinkComment(comments, s.cfg.TaskCommentField, time.Time{})
	text := CommentText(commentBaseURL(s.cfg.EvaRPCURL), matches)
	if s.cfg.LinksDryRun {
		if updID != "" {
			s.log.Info("dry run: would update comment", "id", doc.ID, "comment", updID, "text", text)
		} else {
			s.log.Info("dry run: would comment", "id", doc.ID, "parent", parent, "text", text)
		}
		return nil
	}
	if updID != "" {
		if err := eva.UpdateComment(ctx, s.eva, s.cfg.TaskCommentUpdateMethod, updID, text); err != nil {
			return err
		}
		s.log.Info("comment updated", "id", doc.ID, "comment", updID, "matches", len(matches))
		return nil
	}
	if err := eva.CreateComment(ctx, s.eva, s.cfg.TaskCommentCreateMethod, parent, text); err != nil {
		return err
	}
	s.log.Info("commented", "id", doc.ID, "matches", len(matches))
	return nil
}

// pickLinkComment selects the newest «Автолинкер» comment among the raw
// ListComments rows of a task and decides whether the comment needs a refresh.
// It returns the id of the comment to update ("" when none exists) and whether
// the listener should write: false only when a comment exists and was created
// at/after the task's current cmf_modified_at (i.e. it already reflects the
// latest content). A zero taskModified means "unknown" and keeps the legacy
// idempotent behaviour (existing comment → no re-write).
func pickLinkComment(comments []map[string]any, commentField string, taskModified time.Time) (updID string, refresh bool) {
	var newest time.Time
	for _, c := range comments {
		if text := strp(lookup(c, commentField)); !strings.Contains(text, linkCommentMarker) {
			continue
		}
		if created := lookupTime(c, "cmf_created_at"); created.After(newest) {
			newest = created
			updID = strp(lookup(c, "id"))
		}
	}
	if updID == "" {
		return "", true
	}
	if !taskModified.IsZero() && !newest.Before(taskModified) {
		return updID, false
	}
	return updID, true
}

// CommentText renders the HTML comment body the linker posts on a task,
// listing its similar tasks as Eva @-mentions (clickable task cards).
// Starts with linkCommentMarker so repeated runs are idempotent.
func CommentText(baseURL string, matches []Match) string {
	var b strings.Builder
	b.WriteString("<p><b>" + linkCommentMarker + "</b> задача связана с похожими задачами:</p><ul>")
	for _, m := range matches {
		b.WriteString("<li>")
		b.WriteString(taskMentionHTML(baseURL, m.Code, m.ID, m.Title))
		b.WriteString(" &mdash; ")
		b.WriteString(html.EscapeString(m.Title))
		b.WriteString(fmt.Sprintf(" (%.4f)</li>", m.Score))
	}
	b.WriteString("</ul>")
	return b.String()
}

// commentBaseURL derives the Eva web UI origin from the RPC URL, e.g.
// https://eva.staff.rfn.ru/api -> https://eva.staff.rfn.ru.
func commentBaseURL(rpcURL string) string {
	s := strings.TrimRight(rpcURL, "/")
	s = strings.TrimSuffix(s, "/api")
	return s
}

// taskMentionHTML renders a task as an Eva @-mention, mirroring the markup
// the UI stores when a task is mentioned ("wiki-link-presentation"): an outer
// span carrying the macros parameters as JSON and an <a> that opens the task
// page.
func taskMentionHTML(baseURL, code, id, title string) string {
	href := baseURL + "/project/Task/" + code
	params := `{"viewMode":"text","href":"` + href + `","title":"` + title + `","objId":"` + id + `","modelName":"CmfTask"}`
	label := code + ": " + title
	if code == "" {
		label = id + ": " + title
	}
	const icon = `<svg viewbox="0 0 24 24" class="ng-star-inserted"><path fill="currentColor" d="M19,3H14.82C14.4,1.84 13.3,1 12,1C10.7,1 9.6,1.84 9.18,3H5A2,2 0 0,0 3,5V19A2,2 0 0,0 5,21H19A2,2 0 0,0 21,19V5A2,2 0 0,0 19,3M12,3A1,1 0 0,1 13,4A1,1 0 0,1 12,5A1,1 0 0,1 11,4A1,1 0 0,1 12,3M7,7H17V5H19V19H5V5H7V7M7.5,13.5L9,12L11,14L15.5,9.5L17,11L11,17L7.5,13.5Z"></path></svg>`
	return `<span contenteditable="false" class="wiki-link-wrap mceNonEditable wiki-link-wrap__text" data-internal-link="true" data-macros="wiki-link-presentation" data-macros-parameters="` +
		escapedAttrJSON(params) + `"><a class="wiki-card-text" href="` + href + `" data-mention-type="task" data-object-id="` + id + `"><span class="inline-card-icon-and-title link-view" title="` + html.EscapeString(title) + `"><span>` + icon + `</span><span class="card-name">` + html.EscapeString(label) + `</span></span></a></span>`
}

// escapedAttrJSON encodes the macros JSON for the data-macros-parameters
// attribute exactly as the Eva editor stores it: quotes become &quot; on a
// first pass and the & is cloned again so a stored attribute shows as
// &amp;quot; — the double entity form Eva's comment renderer expects.
func escapedAttrJSON(s string) string {
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
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

// lookupTime reads a dotted path and parses it as a timestamp, returning the
// zero time when the path is absent or unparsable.
func lookupTime(m map[string]any, path string) time.Time {
	v, ok := lookup(m, path)
	if !ok {
		return time.Time{}
	}
	return evaTime(v)
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
