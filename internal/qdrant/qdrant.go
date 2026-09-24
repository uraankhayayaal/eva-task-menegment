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

type apiResponse struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
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
// threshold (cosine similarity), excluding the provided point id (self).
func (c *Client) Search(ctx context.Context, name string, vector []float32, limit int, threshold float64, excludeID uint64) ([]ScoredPoint, error) {
	if limit <= 0 {
		limit = 5
	}
	f := map[string]any{}
	if excludeID != 0 {
		f = map[string]any{
			"must_not": []any{
				map[string]any{"has_id": []uint64{excludeID}},
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
