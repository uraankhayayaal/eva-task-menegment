package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"evasimilar/internal/config"
	"evasimilar/internal/dedup"
	"evasimilar/internal/qdrant"
	"evasimilar/internal/slogx"
)

func main() {
	var (
		collections = flag.String("collections", "", "comma-separated Qdrant collection names; empty = every collection in Qdrant")
		apply       = flag.Bool("apply", false, "delete duplicates; without it the run is a dry report")
	)
	flag.Parse()

	cfg := config.Load()
	log := slogx.New(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	qc := qdrant.New(cfg.QdrantURL)

	var names []string
	if *collections != "" {
		for _, c := range strings.Split(*collections, ",") {
			if c = strings.TrimSpace(c); c != "" {
				names = append(names, c)
			}
		}
	} else {
		all, err := qc.ListCollections(ctx)
		if err != nil {
			log.Error("list collections", "err", err)
			os.Exit(1)
		}
		names = all
	}

	for _, name := range names {
		res, err := dedup.Run(ctx, qc, name, *apply, log)
		if err != nil {
			log.Error("dedup failed", "collection", name, "err", err)
			continue
		}
		log.Info("dedup result",
			"collection", name,
			"scanned", res.Scanned,
			"tasks", res.Tasks,
			"dup_groups", res.DupGroups,
			"dup_points", res.DupPoints,
			"deleted", res.Deleted,
			"apply", *apply,
		)
		for _, e := range res.Errs {
			log.Error("delete batch failed", "collection", name, "err", e)
		}
	}
}
