package eva

import (
	"context"
	"encoding/json"
	"fmt"
)

// ListResult is the expected shape of a list call result. Depending on the
// Eva endpoint the list may come back as a bare array or as an object with
// "items"/"rows". We accept both and return []any.
type ListResult struct {
	Total int               `json:"total,omitempty"`
	Items []any             `json:"items,omitempty"`
	Rows  []any             `json:"rows,omitempty"`
	Data  []any             `json:"data,omitempty"`
	List  []any             `json:"list,omitempty"`
	Raw   []json.RawMessage `json:"-"`
}

// ListTasks fetches one page of tasks (offset/limit) and returns the raw
// items. Exactly which args the list endpoint expects is instance-dependent;
// defaults use [filter, offset, limit] positional args.
func ListTasks(ctx context.Context, c *Client, method string, filter map[string]any, offset, limit int) ([]map[string]any, int, error) {
	args := []any{filter, offset, limit}
	raw, err := c.Call(ctx, modelOf(method), methodOf(method), args, map[string]any{"offset": offset, "limit": limit})
	if err != nil {
		return nil, 0, err
	}
	return decodeItems(raw)
}

// GetTask fetches a single task by its id.
func GetTask(ctx context.Context, c *Client, method string, id any) (map[string]any, error) {
	raw, err := c.Call(ctx, modelOf(method), methodOf(method), []any{id}, nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ListComments returns the comments of a task. The positional convention is
// [task_id, filter]; override via config if your instance differs.
func ListComments(ctx context.Context, c *Client, method string, taskID any, filter map[string]any) ([]map[string]any, error) {
	args := []any{taskID}
	if filter != nil {
		args = append(args, filter)
	}
	raw, err := c.Call(ctx, modelOf(method), methodOf(method), args, map[string]any{"filter": filter})
	if err != nil {
		return nil, err
	}
	items, _, err := decodeItems(raw)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// LinkTasks requests Eva to create a link between two tasks. The exact
// payload depends on your instance; by default it calls
// <model>.save_links with [{from,to,type}]. Override EVA_TASK_LINK_METHOD
// / link args if required.
func LinkTasks(ctx context.Context, c *Client, method string, from, to any, linkType string) error {
	model := modelOf(method)
	args := []any{[]any{map[string]any{
		"from": from,
		"to":   to,
		"type": linkType,
	}}}
	_, err := c.Call(ctx, model, methodOf(method), args, nil)
	return err
}

func modelOf(full string) string {
	for i := 0; i < len(full); i++ {
		if full[i] == '.' {
			return full[:i]
		}
	}
	return full
}

func methodOf(full string) string {
	for i := 0; i < len(full); i++ {
		if full[i] == '.' {
			return full[i+1:]
		}
	}
	return ""
}

func decodeItems(raw json.RawMessage) ([]map[string]any, int, error) {
	// Strip a leading {"result": ...} wrapper if present.
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
