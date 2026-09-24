package eva

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

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
		return nil, fmt.Errorf("task %v not found", id)
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
	// Pluck "result" from {"result": ...} wrappers.
	if len(raw) > 0 && raw[0] == '{' {
		var wrap struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &wrap); err == nil && len(wrap.Result) > 0 {
			raw = wrap.Result
		}
	}
	if len(raw) > 0 && raw[0] == '[' {
		var items []map[string]any
		if err := json.Unmarshal(raw, &items); err == nil {
			return items, len(items), nil
		}
	}
	var lr ListResult
	if err := json.Unmarshal(raw, &lr); err != nil {
		// Some get responses return a bare object — wrap it.
		var single map[string]any
		if err2 := json.Unmarshal(raw, &single); err2 == nil {
			return []map[string]any{single}, 1, nil
		}
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
	return out, lr.Total, nil
}
