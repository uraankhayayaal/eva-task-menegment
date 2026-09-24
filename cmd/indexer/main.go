package main

import (
	"context"
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
		log.Error("eva auth", "err", err)
		os.Exit(1)
	}

	if err := svc.EnsureCollection(ctx); err != nil {
		log.Error("ensure collection", "err", err)
		os.Exit(1)
	}

	log.Info("starting continuous indexer",
		"rpc_url", cfg.EvaRPCURL,
		"collection", cfg.QdrantCollection,
		"embed_provider", cfg.EmbedProvider,
		"auth", eva.AuthMode(cfg),
		"recreate", cfg.IndexerRecreate,
	)

	for {
		if err := svc.IndexAll(ctx); err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Error("index scan failed; retrying in one minute", "err", err)
		}
		if ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			log.Info("indexer stopped")
			return
		case <-timer.C:
		}
	}
	log.Info("indexer stopped")
}
