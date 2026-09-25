package similar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"evasimilar/internal/config"
	"evasimilar/internal/eva"
)

// TestFindTaskCodeFallback verifies that a manual id that is a short code
// (e.g. "SMOT-9860") still resolves: the configured filter field is tried
// first and, when it matches nothing, the code field is tried.
func TestFindTaskCodeFallback(t *testing.T) {
	var lastMethod string
	var lastFilter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Kwargs struct {
				Filter []any `json:"filter"`
			} `json:"kwargs"`
		}
		json.Unmarshal(body, &req)
		lastMethod = r.URL.Query().Get("m")
		if f := req.Kwargs.Filter; len(f) > 0 {
			if triple, ok := f[0].([]any); ok && len(triple) >= 3 {
				lastFilter, _ = triple[0].(string)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if lastFilter == "code" {
			io.WriteString(w, `{"jsonrpc":"2.0","result":{"id":"CmfTask:abc","code":"SMOT-9860","name":"t","text":"d","result":"r"},"callid":"x"}`)
			return
		}
		io.WriteString(w, `{"jsonrpc":"2.0","result":null,"callid":"x"}`)
	}))
	defer srv.Close()

	ec := eva.New(srv.URL+"/api", "", "", "", "")
	cfg := config.Config{
		EvaRPCURL:          srv.URL + "/api",
		TaskGetMethod:      "CmfTask.get",
		TaskGetFilterField: "id",
		TaskCodeField:      "code",
	}
	svc := New(cfg, ec, nil, nil, slog.Default())

	raw, err := svc.FindTask(context.Background(), "SMOT-9860")
	if err != nil {
		t.Fatalf("FindTask: %v", err)
	}
	if lastMethod != "CmfTask.get" {
		t.Errorf("method = %q, want CmfTask.get", lastMethod)
	}
	if lastFilter != "code" {
		t.Errorf("fallback filter field = %q, want code", lastFilter)
	}
	if raw["code"] != "SMOT-9860" {
		t.Errorf("raw code = %v, want SMOT-9860", raw["code"])
	}
}

// TestFindTaskNotFound verifies the not-found error is returned when neither
// the configured field nor the code field match.
func TestFindTaskNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jsonrpc":"2.0","result":null,"callid":"x"}`)
	}))
	defer srv.Close()

	ec := eva.New(srv.URL+"/api", "", "", "", "")
	cfg := config.Config{
		EvaRPCURL:          srv.URL + "/api",
		TaskGetMethod:      "CmfTask.get",
		TaskGetFilterField: "id",
		TaskCodeField:      "code",
	}
	svc := New(cfg, ec, nil, nil, slog.Default())

	_, err := svc.FindTask(context.Background(), "NOPE-0000")
	if err == nil {
		t.Fatal("FindTask succeeded, want error")
	}
	if !strings.Contains(err.Error(), "NOPE-0000") {
		t.Errorf("error %q does not mention the id", err)
	}
}