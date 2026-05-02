package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"dunkirk.sh/tangle-of-trust/internal/db"
	"dunkirk.sh/tangle-of-trust/internal/resolve"
)

func main() {
	dbPath := flag.String("db", "tangle.db", "path to sqlite database")
	batchSize := flag.Int("batch", 100, "number of DIDs to resolve per batch")
	evict := flag.Bool("evict", false, "evict incomplete profiles (missing handle or avatar) before resolving")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	store, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	if *evict {
		n, err := store.DeleteIncompleteProfiles()
		if err != nil {
			slog.Warn("failed to evict incomplete profiles", "error", err)
		} else {
			slog.Info("evicted incomplete profiles", "count", n)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("interrupted, shutting down")
		cancel()
	}()

	total, err := resolve.EnrichMissing(ctx, store, *batchSize)
	if err != nil {
		slog.Warn("resolution had errors", "error", err)
	}
	slog.Info("resolution complete", "total_enriched", total)
}
