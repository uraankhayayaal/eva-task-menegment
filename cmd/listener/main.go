package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"evasimilar/internal/config"
	"evasimilar/internal/embed"
	"evasimilar/internal/eva"
	"evasimilar/internal/qdrant"
	"evasimilar/internal/similar"
	"evasimilar/internal/slogx"
	"evasimilar/internal/textutil"
)

func main() {
	cfg := config.Load()
	log := slogx.New(cfg.LogLevel)

	ec := eva.NewFromConfig(cfg)
	emb := embed.New(cfg.EmbedProvider, cfg.OllamaURL, cfg.OllamaModel, cfg.TEIURL, cfg.EmbedDim)
	qc := qdrant.New(cfg.QdrantURL)
	svc := similar.New(cfg, ec, emb, qc, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := ec.Auth(ctx); err != nil {
		log.Error("eva auth", "err", err, "mode", eva.AuthMode(cfg))
		os.Exit(1)
	}
	if err := svc.EnsureCollection(ctx); err != nil {
		log.Error("ensure collection", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok"}`)
	})
	wh := handleWebhook(svc, cfg, log, cfg.WebhookToken != "")
	mux.HandleFunc("POST "+cfg.ListenerWebhookPath, wh)
	manual := func(h http.HandlerFunc) http.HandlerFunc {
		if cfg.WebhookToken == "" {
			return h
		}
		return func(w http.ResponseWriter, r *http.Request) {
			if tok := webhookToken(r); !constantTimeEqual(tok, cfg.WebhookToken) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /index/{id}", manual(handleManualIndex(svc, log)))
	mux.HandleFunc("GET /link/{id}", manual(handleManualLink(svc, log)))

	srv := &http.Server{Addr: cfg.ListenerAddr, Handler: mux}
	go func() {
		log.Info("listener started", "addr", cfg.ListenerAddr, "webhook", cfg.ListenerWebhookPath, "dry_run", cfg.LinksDryRun, "webhook_auth", cfg.WebhookToken != "", "eva_auth", eva.AuthMode(cfg))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listener", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("shutdown", "err", err)
	}
	log.Info("listener stopped")
}

type webhookHandler struct {
	svc          *similar.Service
	cfg          config.Config
	log          *slog.Logger
	enforceToken bool
}

func handleWebhook(svc *similar.Service, cfg config.Config, log *slog.Logger, enforceToken bool) http.HandlerFunc {
	h := &webhookHandler{svc: svc, cfg: cfg, log: log, enforceToken: enforceToken}
	return func(w http.ResponseWriter, r *http.Request) {
		if h.enforceToken {
			if tok := webhookToken(r); !constantTimeEqual(tok, cfg.WebhookToken) {
				h.log.Warn("webhook: unauthorized", "remote", r.RemoteAddr)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id, err := extractTaskID(body, cfg.ListenerTaskIDPath)
		if err != nil {
			h.log.Warn("webhook: no task id", "err", err, "body", textutil.Truncate(string(body), 200))
			http.Error(w, "no task id: "+err.Error(), http.StatusBadRequest)
			return
		}
		doc, matches, err := svc.FindAndLink(r.Context(), id)
		if err != nil {
			h.log.Error("webhook: process task", "id", id, "err", err)
			http.Error(w, "process task: "+err.Error(), http.StatusInternalServerError)
			return
		}
		out := map[string]any{
			"task_id": doc.ID,
			"linked":  matches,
		}
		h.log.Info("webhook processed", "task_id", doc.ID, "matches", len(matches))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

func handleManualIndex(svc *similar.Service, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		doc, err := svc.IndexTask(r.Context(), id)
		if err != nil {
			log.Error("manual index", "id", id, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"indexed": doc.ID})
	}
}

func handleManualLink(svc *similar.Service, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		doc, matches, err := svc.FindAndLink(r.Context(), id)
		if err != nil {
			log.Error("manual link", "id", id, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"task_id": doc.ID, "linked": matches})
	}
}

// webhookToken extracts the bearer/header/query token a client (Eva) sent.
func webhookToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("Bearer "):])
	}
	if t := r.Header.Get("X-Webhook-Token"); t != "" {
		return t
	}
	return r.URL.Query().Get("token")
}

// constantTimeEqual compares two strings in constant time.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// extractTaskID walks the JSON payload by the configured dotted path
// ("id", "task.id", "object.task.id") and falls back to common keys.
func extractTaskID(body []byte, path string) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	try := []string{path, "id", "task_id", "task.id", "object.id", "data.id"}
	for _, p := range try {
		if v, ok := walk(payload, p); ok && v != nil {
			if s := fmt.Sprint(v); s != "" && s != "<nil>" {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("task id not found (tried paths: %s)", strings.Join(try, ", "))
}

func walk(m map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	if len(parts) == 1 {
		v, ok := m[parts[0]]
		return v, ok
	}
	child, ok := m[parts[0]]
	if !ok {
		return nil, false
	}
	cm, ok := child.(map[string]any)
	if !ok {
		return nil, false
	}
	return walk(cm, strings.Join(parts[1:], "."))
}
