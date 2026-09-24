package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client embeds text using either Ollama or text-embeddings-inference.
type Client struct {
	provider  string // "ollama" | "tei"
	ollamaURL string
	model     string
	teiURL    string
	dim       int
	http      *http.Client
}

type OllamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type OllamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func New(provider, ollamaURL, model, teiURL string, dim int) *Client {
	return &Client{
		provider:  provider,
		ollamaURL: ollamaURL,
		model:     model,
		teiURL:    teiURL,
		dim:       dim,
		http:      &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *Client) Dim() int { return c.dim }

// Embed returns one embedding vector per input string.
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	switch c.provider {
	case "tei":
		return c.embedTEI(ctx, inputs)
	case "ollama", "":
		return c.embedOllama(ctx, inputs)
	default:
		return nil, fmt.Errorf("unknown embed provider %q", c.provider)
	}
}

func (c *Client) embedOllama(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(OllamaEmbedRequest{Model: c.model, Input: inputs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ollamaURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed: status %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var out OllamaEmbedResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("ollama embed: decode: %w", err)
	}
	return out.Embeddings, nil
}

func (c *Client) embedTEI(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"inputs": inputs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.teiURL+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tei embed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tei embed: status %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var vecs [][]float32
	// TEI can return either [[...]] or [[[r]]] depending on content type headers;
	// with application/json it returns a plain batch matrix.
	if err := json.Unmarshal(data, &vecs); err != nil {
		return nil, fmt.Errorf("tei embed: decode: %w", err)
	}
	return vecs, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
