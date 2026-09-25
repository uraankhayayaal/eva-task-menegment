package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

type Point struct {
	ID      uint64         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

type ScoredPoint struct {
	ID      uint64         `json:"id"`
	Score   float32        `json:"score"`
	Payload map[string]any `json:"payload,omitempty"`
}

type ScrolledPoint struct {
	ID      uint64         `json:"id"`
	Payload map[string]any `json:"payload"`
}

type apiResponse struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// LoadPayload returns the first point payload matching the Qdrant filter, or
// ok=false when no point matches. Used to read a freshly stored content hash
// back from an exact chunk point.
func (c *Client) LoadPayload(ctx context.Context, name string, filter map[string]any) (map[string]any, bool, error) {
	body := map[string]any{
		"limit":        1,
		"with_payload": true,
		"with_vector":  false,
		"filter":       filter,
	}
	raw, err := c.doRaw(ctx, http.MethodPost, "/collections/"+name+"/points/scroll", body)
	if err != nil {
		return nil, false, err
	}
	var resp scrollResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, false, err
	}
	if resp.Error != "" {
		return nil, false, fmt.Errorf("qdrant scroll: %s", resp.Error)
	}
	if len(resp.Result.Points) == 0 || resp.Result.Points[0].Payload == nil {
		return nil, false, nil
	}
	return resp.Result.Points[0].Payload, true, nil
}

// ExistingTaskIDs returns task IDs found in point payloads. It scrolls through
// the collection once, which lets the indexer filter an Eva page without one
// Qdrant request per task.
func (c *Client) ExistingTaskIDs(ctx context.Context, name string) (map[string]struct{}, error) {
	points, err := c.Scroll(ctx, name, []string{"eva_id"})
	if err != nil {
		return nil, err
	}
	ids := make(map[string]struct{}, len(points))
	for _, point := range points {
		if id := fmt.Sprint(point.Payload["eva_id"]); id != "" && id != "<nil>" {
			ids[id] = struct{}{}
		}
	}
	return ids, nil
}

// ExistingTaskIndexes returns task IDs found in point payloads together with
// their indexed "modified_at" value. It lets an indexer detect tasks whose
// Eva content (cmf_modified_at) moved past what was last embedded.
func (c *Client) ExistingTaskIndexes(ctx context.Context, name string) (map[string]string, error) {
	points, err := c.Scroll(ctx, name, []string{"eva_id", "modified_at"})
	if err != nil {
		return nil, err
	}
	indexes := make(map[string]string, len(points))
	for _, point := range points {
		if id := fmt.Sprint(point.Payload["eva_id"]); id != "" && id != "<nil>" {
			indexes[id] = fmt.Sprint(point.Payload["modified_at"])
		}
	}
	return indexes, nil
}

type scrollResult struct {
	Points         []ScrolledPoint `json:"points"`
	NextPageOffset json.RawMessage `json:"next_page_offset"`
}

type scrollResponse struct {
	Result scrollResult `json:"result"`
	Error  string       `json:"error"`
}

// Scroll walks the whole collection and returns every point's id together with
// the requested payload fields (withVector is always false).
func (c *Client) Scroll(ctx context.Context, name string, payloadFields []string) ([]ScrolledPoint, error) {
	var out []ScrolledPoint
	var offset json.RawMessage
	for {
		body := map[string]any{
			"limit":        1000,
			"with_payload": payloadFields,
			"with_vector":  false,
		}
		if len(offset) > 0 && string(offset) != "null" {
			var value any
			if err := json.Unmarshal(offset, &value); err != nil {
				return nil, fmt.Errorf("decode qdrant scroll offset: %w", err)
			}
			body["offset"] = value
		}
		raw, err := c.doRaw(ctx, http.MethodPost, "/collections/"+name+"/points/scroll", body)
		if err != nil {
			return nil, err
		}
		var resp scrollResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, err
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("qdrant scroll: %s", resp.Error)
		}
		out = append(out, resp.Result.Points...)
		if len(resp.Result.NextPageOffset) == 0 || string(resp.Result.NextPageOffset) == "null" {
			break
		}
		offset = resp.Result.NextPageOffset
	}
	return out, nil
}

// ListCollections returns the names of all collections in this Qdrant.
func (c *Client) ListCollections(ctx context.Context) ([]string, error) {
	raw, err := c.doRaw(ctx, http.MethodGet, "/collections", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("qdrant list collections: %s", resp.Error)
	}
	out := make([]string, 0, len(resp.Result.Collections))
	for _, c := range resp.Result.Collections {
		out = append(out, c.Name)
	}
	return out, nil
}

// CountPoints returns the exact number of points stored in a collection.
func (c *Client) CountPoints(ctx context.Context, name string) (uint64, error) {
	body := map[string]any{"exact": true}
	raw, err := c.doRaw(ctx, http.MethodPost, "/collections/"+name+"/points/count", body)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Result struct {
			Count uint64 `json:"count"`
		} `json:"result"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, err
	}
	if resp.Error != "" {
		return 0, fmt.Errorf("qdrant count points: %s", resp.Error)
	}
	return resp.Result.Count, nil
}

// EnsureCollection creates the collection if it does not already exist.
func (c *Client) EnsureCollection(ctx context.Context, name string, dim int, distance string) error {
	// Check existence
	if err := c.do(ctx, http.MethodGet, "/collections/"+name, nil, nil); err == nil {
		return nil
	}
	body := map[string]any{
		"vectors": map[string]any{
			"size":     dim,
			"distance": distance,
		},
		"on_disk_payload": true,
	}
	return c.do(ctx, http.MethodPut, "/collections/"+name, body, nil)
}

// DropCollection removes the collection if present.
func (c *Client) DropCollection(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/collections/"+name, nil, nil)
}

// Upsert writes points to the collection (waits for indexing). The REST
// upsert endpoint is PUT /collections/{name}/points with {"points": [...]}.
func (c *Client) Upsert(ctx context.Context, name string, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	body := map[string]any{"points": points}
	return c.do(ctx, http.MethodPut, "/collections/"+name+"/points?wait=true", body, nil)
}

// Search returns the top-n most similar points above the given score
// threshold (cosine similarity), excluding the provided point ids (self —
// all chunk points of the querying task).
func (c *Client) Search(ctx context.Context, name string, vector []float32, limit int, threshold float64, excludeIDs []uint64) ([]ScoredPoint, error) {
	if limit <= 0 {
		limit = 5
	}
	f := map[string]any{}
	if len(excludeIDs) > 0 {
		f = map[string]any{
			"must_not": []any{
				map[string]any{"has_id": excludeIDs},
			},
		}
	}
	body := map[string]any{
		"vector":          vector,
		"limit":           limit,
		"score_threshold": threshold,
		"with_payload":    true,
		"filter":          f,
	}
	raw, err := c.doRaw(ctx, http.MethodPost, "/collections/"+name+"/points/search", body)
	if err != nil {
		return nil, err
	}
	var resp apiResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("qdrant search: %s", resp.Error)
	}
	var out []ScoredPoint
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes points matching a filter (filter is a Qdrant filter JSON).
func (c *Client) Delete(ctx context.Context, name string, filter map[string]any) error {
	body := map[string]any{"filter": filter, "wait": true}
	return c.do(ctx, http.MethodPost, "/collections/"+name+"/points/delete", body, nil)
}

// DeleteByIDs removes points by their numeric ids (waits for indexing).
func (c *Client) DeleteByIDs(ctx context.Context, name string, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	body := map[string]any{"points": ids, "wait": true}
	return c.do(ctx, http.MethodPost, "/collections/"+name+"/points/delete", body, nil)
}

// PointID hashes an Eva task id into a stable uint64 Qdrant point id.
func PointID(id string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(id))
	return h.Sum64()
}

func (c *Client) do(ctx context.Context, method, path string, body map[string]any, out *apiResponse) error {
	raw, err := c.doRaw(ctx, method, path, body)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) doRaw(ctx context.Context, method, path string, body map[string]any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qdrant %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound && method == http.MethodGet {
		return nil, fmt.Errorf("qdrant: not found")
	}
	if resp.StatusCode >= 400 {
		var ar apiResponse
		if json.Unmarshal(data, &ar) == nil && ar.Error != "" {
			return nil, fmt.Errorf("qdrant %s %s: %s", method, path, ar.Error)
		}
		return nil, fmt.Errorf("qdrant %s %s: http %d: %s", method, path, resp.StatusCode, truncate(string(data), 400))
	}
	var ar apiResponse
	if err := json.Unmarshal(data, &ar); err == nil && ar.Error != "" {
		return nil, fmt.Errorf("qdrant %s %s: %s", method, path, ar.Error)
	}
	return data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
