package similar

import (
	"context"
	"fmt"
	"time"

	"evasimilar/internal/eva"
)

// LinkNewTasks polls Eva for tasks created after watermark and runs
// find-and-link on every one of them. It returns the newest cmf_created_at
// seen so the caller can persist it as the new watermark.
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
			_, matches, err := s.FindAndLink(ctx, id)
			if err != nil {
				failed++
				s.log.Error("link new task", "id", id, "err", err)
				continue
			}
			processed++
			s.log.Info("linked new task", "id", id, "matches", len(matches), "dry_run", s.cfg.LinksDryRun)
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