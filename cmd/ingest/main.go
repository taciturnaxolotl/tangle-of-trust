package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dunkirk.sh/tangle-of-trust/internal/db"
	"dunkirk.sh/tangle-of-trust/internal/jetstream"
	"dunkirk.sh/tangle-of-trust/internal/resolve"
	"dunkirk.sh/tangle-of-trust/internal/web"
)

func main() {
	dbPath := flag.String("db", "tangle.db", "path to sqlite database")
	jetstreamURL := flag.String("jetstream", jetstream.DefaultURL, "jetstream websocket URL")
	webAddr := flag.String("web", ":8080", "web server address")
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	sub := jetstream.NewSubscriber(*jetstreamURL, store)

	go func() {
		if err := sub.Run(ctx); err != nil && err != context.Canceled {
			slog.Error("subscriber error", "error", err)
			cancel()
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Minute):
				enriched, err := resolve.EnrichMissing(ctx, store, 50)
				if err != nil {
					slog.Warn("background profile enrichment had errors", "error", err)
				}
				if enriched > 0 {
					slog.Info("background profile enrichment", "enriched", enriched)
				}
			}
		}
	}()

	if err := web.StartServer(ctx, *webAddr, store); err != nil && err != http.ErrServerClosed {
		slog.Error("web server error", "error", err)
	}

	slog.Info("shutdown complete")
}
