package eva

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrTaskNotFound is returned by GetTask when the filter matched no task.
var ErrTaskNotFound = errors.New("task not found")

// ListResult is the expected shape of a list call result. Depending on the
// Eva endpoint the list may come back as a bare array or as an object with
// "items"/"rows". We accept both and return []map[string]any.
type ListResult struct {
	Total int   `json:"total,omitempty"`
	Items []any `json:"items,omitempty"`
	Rows  []any `json:"rows,omitempty"`
	Data  []any `json:"data,omitempty"`
	List  []any `json:"list,omitempty"`
}

// ListTasks fetches one page of tasks via CmfTask.list. kwargs:
//
//	{filter: [[f,op,v],...], fields: [...], slice: [start, end], include_archived: false}
//
// fields requests explicit attributes (e.g. "text") which the list endpoint
// omits by default otherwise.
func ListTasks(ctx context.Context, c *Client, method string, filterTriples []any, fields []string, offset, limit int) ([]map[string]any, int, error) {
	kwargs := map[string]any{
		// EVA interprets slice as a half-open [start, end) range, not
		// [offset, limit]. Passing [100, 100] therefore returns no rows.
		"slice": []any{offset, offset + limit},
		// The indexer should cover the full task history, including archived
		// tasks; otherwise Eva may report only the active subset.
		"include_archived": true,
	}
	if len(filterTriples) > 0 {
		kwargs["filter"] = filterTriples
	}
	if len(fields) > 0 {
		kwargs["fields"] = fields
	}
	raw, err := c.Call(ctx, method, kwargs)
	if err != nil {
		return nil, 0, err
	}
	return decodeItems(raw)
}

// CountTasks returns the count for the same filters used by ListTasks.
func CountTasks(ctx context.Context, c *Client, listMethod string, filterTriples []any) (int64, error) {
	method := strings.TrimSuffix(listMethod, ".list") + ".count"
	kwargs := map[string]any{"include_archived": true}
	if len(filterTriples) > 0 {
		kwargs["filter"] = filterTriples
	}
	raw, err := c.Call(ctx, method, kwargs)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := json.Unmarshal(raw, &count); err != nil {
		return 0, fmt.Errorf("decode %s count result: %w", method, err)
	}
	return count, nil
}

// GetTask fetches a single task via CmfTask.get with
// filter = [[filterField, "==", id]].
func GetTask(ctx context.Context, c *Client, method, filterField string, id any) (map[string]any, error) {
	kwargs := map[string]any{
		"filter": []any{[]any{filterField, "==", id}},
	}
	raw, err := c.Call(ctx, method, kwargs)
	if err != nil {
		return nil, err
	}
	items, n, err := decodeItems(raw)
	if err != nil {
		return nil, err
	}
	if n == 0 || items == nil {
		return nil, fmt.Errorf("%w: %v", ErrTaskNotFound, id)
	}
	return items[0], nil
}

// ListComments returns the comments of a task via CmfComment.list with
// filter = [[parent, "==", commentParentPrefix+id]].
func ListComments(ctx context.Context, c *Client, method, commentParentPrefix string, fields []string, taskID any) ([]map[string]any, error) {
	kwargs := map[string]any{
		"filter": []any{[]any{"parent", "==", commentParentPrefix + fmt.Sprint(taskID)}},
	}
	if len(fields) > 0 {
		kwargs["fields"] = fields
	}
	raw, err := c.Call(ctx, method, kwargs)
	if err != nil {
		return nil, err
	}
	items, _, err := decodeItems(raw)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// CreateComment posts a comment to a task via CmfComment.create with kwargs
// {parent, text}. parent must be the full task reference ("CmfTask:<uuid>");
// text is HTML.
func CreateComment(ctx context.Context, c *Client, method, parent, text string) error {
	kwargs := map[string]any{
		"parent": parent,
		"text":   text,
	}
	if _, err := c.Call(ctx, method, kwargs); err != nil {
		return err
	}
	return nil
}

// UpdateComment rewrites the text of an existing comment via CmfComment.update.
// Eva addresses the target comment through args[0]; the kwargs only carry the
// new text. commentID must be the full reference ("CmfComment:<uuid>").
func UpdateComment(ctx context.Context, c *Client, method, commentID, text string) error {
	if _, err := c.CallRaw(ctx, method, []any{commentID}, map[string]any{"text": text}); err != nil {
		return err
	}
	return nil
}

// LinkTasks asks Eva to create a relation between two tasks via
// CmfRelationOption.create with kwargs {out_link, in_link, relation_type}.
func LinkTasks(ctx context.Context, c *Client, method string, from, to any, linkType string) error {
	kwargs := map[string]any{
		"out_link":      fmt.Sprint(from),
		"in_link":       fmt.Sprint(to),
		"relation_type": linkType,
	}
	if _, err := c.Call(ctx, method, kwargs); err != nil {
		return err
	}
	return nil
}

func decodeItems(raw json.RawMessage) ([]map[string]any, int, error) {
	trimmed := bytes.TrimSpace(raw)
	// A "null" result (or an empty body) means no rows, not an error.
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, 0, nil
	}
	// A bare array of records.
	if trimmed[0] == '[' {
		var items []map[string]any
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, 0, fmt.Errorf("decode list result: %w", err)
		}
		return items, len(items), nil
	}
	if trimmed[0] != '{' {
		return nil, 0, fmt.Errorf("decode list result: unexpected shape %s", truncate(string(trimmed), 100))
	}
	// A list-shaped object: {"items": [...]}, {"rows": ...}, {"data": ...},
	// {"list": ...}. A bare single object (e.g. a CmfTask.get hit) also
	// unmarshals into ListResult but keeps every list field nil.
	var lr ListResult
	if err := json.Unmarshal(trimmed, &lr); err != nil {
		return nil, 0, fmt.Errorf("decode list result: %w", err)
	}
	items := lr.Items
	if len(items) == 0 {
		items = lr.Rows
	}
	if len(items) == 0 {
		items = lr.Data
	}
	if len(items) == 0 {
		items = lr.List
	}
	if len(items) > 0 {
		out := make([]map[string]any, 0, len(items))
		for _, it := range items {
			switch v := it.(type) {
			case map[string]any:
				out = append(out, v)
			default:
				b, err := json.Marshal(it)
				if err == nil {
					var m map[string]any
					if err := json.Unmarshal(b, &m); err == nil {
						out = append(out, m)
					}
				}
			}
		}
		return out, len(out), nil
	}
	// {"result": ...} wrapper — recurse only when the nested value is
	// structured; a scalar "result" (a task's own result text) is not a
	// wrapper.
	var wrap struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(trimmed, &wrap); err == nil {
		if sub := bytes.TrimSpace(wrap.Result); len(sub) > 0 && (sub[0] == '{' || sub[0] == '[') {
			return decodeItems(wrap.Result)
		}
	}
	// No list or wrapper shape -> a bare single record.
	var single map[string]any
	if err := json.Unmarshal(trimmed, &single); err == nil {
		return []map[string]any{single}, 1, nil
	}
	return nil, 0, fmt.Errorf("decode list result: %q", string(trimmed))
}
