package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

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

	log.Info("starting batch index",
		"rpc_url", cfg.EvaRPCURL,
		"collection", cfg.QdrantCollection,
		"embed_provider", cfg.EmbedProvider,
		"auth", eva.AuthMode(cfg),
		"recreate", cfg.IndexerRecreate,
	)

	if err := svc.IndexAll(ctx); err != nil {
		log.Error("batch index failed", "err", err)
		os.Exit(1)
	}
	log.Info("batch index finished")
}
