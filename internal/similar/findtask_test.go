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
	"evasimilar/internal/embed"
	"evasimilar/internal/eva"
	"evasimilar/internal/qdrant"
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

func TestTaskCodeLists(t *testing.T) {
	svc := New(config.Config{
		TaskCodeField:     "code",
		TaskCodeWhitelist: []string{"SMAD-", "SMOT-", "RED-"},
		TaskCodeBlacklist: []string{"SRE-"},
	}, nil, nil, nil, slog.Default())
	for _, tc := range []struct {
		code string
		want bool
	}{
		{code: "SMAD-123", want: true},
		{code: "SMOT-123-extra", want: true},
		{code: "RED-123", want: true},
		{code: "SRE-123", want: false},
		{code: "OTHER-123", want: false},
		{code: "smot-123", want: false},
		{code: "", want: false},
	} {
		if got := svc.taskActionAllowed(map[string]any{"code": tc.code}); got != tc.want {
			t.Errorf("taskActionAllowed(%q) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestTaskCodeBlacklistOverridesWhitelist(t *testing.T) {
	svc := New(config.Config{
		TaskCodeField:     "code",
		TaskCodeWhitelist: []string{"*"},
		TaskCodeBlacklist: []string{"SRE-"},
	}, nil, nil, nil, slog.Default())
	if svc.taskActionAllowed(map[string]any{"code": "SMAD-1"}) != true {
		t.Fatal("SMAD-1 should be allowed")
	}
	if svc.taskActionAllowed(map[string]any{"code": "SRE-1"}) != false {
		t.Fatal("SRE-1 should be blocked")
	}
}

func TestTaskCodeListsSkipActions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "CmfTask.get":
			io.WriteString(w, `{"jsonrpc":"2.0","result":{"id":"task-1","code":"SRE-1","name":"Test Task","text":"Description text","result":"Result"},"callid":"x"}`)
		case req.Method == "CmfComment.list":
			io.WriteString(w, `{"jsonrpc":"2.0","result":[],"callid":"x"}`)
		default:
			io.WriteString(w, `{"jsonrpc":"2.0","result":null,"callid":"x"}`)
		}
	}))
	defer srv.Close()

	embedMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"test","embedding":[0.0],"prompt_tokens":1}`)
	}))
	defer embedMock.Close()

	qdrantMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "PUT /collections/eva_tasks_test", "GET /collections/eva_tasks_test":
			io.WriteString(w, `{"status":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer qdrantMock.Close()

	ec := eva.New(srv.URL+"/api", "", "", "", "")
	emb := embed.New("ollama", embedMock.URL, "test", "", 768)
	qd := qdrant.New(qdrantMock.URL)
	cfg := config.Config{
		EvaRPCURL:         srv.URL + "/api",
		TaskGetMethod:     "CmfTask.get",
		TaskIDField:       "id",
		TaskCodeField:     "code",
		TaskCodeWhitelist: []string{"SMAD-", "SMOT-", "RED-"},
		TaskCodeBlacklist: []string{"SRE-"},
		QdrantCollection:  "eva_tasks_test",
	}
	svc := New(cfg, ec, emb, qd, slog.Default())

	if err := svc.EnsureCollection(context.Background()); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}

	if _, _, err := svc.FindAndLink(context.Background(), "task-1"); err != nil {
		t.Fatalf("FindAndLink: %v", err)
	}
	if _, _, err := svc.CommentTask(context.Background(), "task-1"); err != nil {
		t.Fatalf("CommentTask: %v", err)
	}
}
