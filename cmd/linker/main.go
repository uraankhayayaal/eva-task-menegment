package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"
	"time"

	"evasimilar/internal/config"
	"evasimilar/internal/embed"
	"evasimilar/internal/eva"
	"evasimilar/internal/qdrant"
	"evasimilar/internal/similar"
	"evasimilar/internal/slogx"
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

	watermark, err := loadWatermark(cfg.LinkerWatermarkFile)
	if err != nil {
		log.Warn("load watermark (starting from lookback window)", "file", cfg.LinkerWatermarkFile, "err", err)
	}
	lookback := time.Duration(cfg.LinkerInitialLookback) * time.Hour
	if cfg.LinkerInitialLookback <= 0 {
		lookback = 24 * time.Hour
	}
	if watermark.IsZero() {
		watermark = time.Now().Add(-lookback)
		log.Info("no watermark found; starting from lookback", "since", watermark.Format(time.RFC3339))
	}
	interval := time.Duration(cfg.LinkerPollInterval) * time.Second
	if cfg.LinkerPollInterval <= 0 {
		interval = time.Minute
	}

	log.Info("polling linker started",
		"rpc_url", cfg.EvaRPCURL,
		"collection", cfg.QdrantCollection,
		"watermark", watermark.Format(time.RFC3339),
		"interval", interval,
		"dry_run", cfg.LinksDryRun,
		"link_mode", cfg.LinkerLinkMode,
		"auth", eva.AuthMode(cfg),
	)

	for {
		next, err := svc.LinkNewTasks(ctx, watermark)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Error("link scan failed; retrying in one interval", "err", err)
		} else if next.After(watermark) {
			watermark = next
			if err := saveWatermark(cfg.LinkerWatermarkFile, watermark); err != nil {
				log.Warn("save watermark", "err", err)
			}
		}
		if ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			log.Info("linker stopped")
			return
		case <-timer.C:
		}
	}
	log.Info("linker stopped")
}

// loadWatermark reads the persisted "created_at" watermark, if any.
func loadWatermark(path string) (time.Time, error) {
	if path == "" {
		return time.Time{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	var st struct {
		Watermark string `json:"watermark"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, st.Watermark)
}

// saveWatermark persists the watermark atomically; a no-op when path is empty.
func saveWatermark(path string, t time.Time) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(map[string]string{"watermark": t.Format(time.RFC3339)})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}