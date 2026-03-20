package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/org/reverb/pkg/embedding/fake"
	"github.com/org/reverb/pkg/reverb"
	"github.com/org/reverb/pkg/server"
	"github.com/org/reverb/pkg/store/memory"
	"github.com/org/reverb/pkg/vector/flat"
)

func main() {
	httpAddr := flag.String("http-addr", ":8080", "HTTP listen address")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Default configuration: in-memory store, flat vector index, fake embedder
	// In production, these would be configured via YAML/env vars.
	s := memory.New()
	vi := flat.New()
	embedder := fake.New(64)

	cfg := reverb.DefaultConfig()
	client, err := reverb.New(cfg, embedder, s, vi)
	if err != nil {
		logger.Error("failed to create reverb client", "error", err)
		os.Exit(1)
	}
	defer client.Close()

	srv := server.NewHTTPServer(client, *httpAddr)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("starting reverb server", "addr", *httpAddr)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down server")
		if err := srv.Shutdown(context.Background()); err != nil {
			logger.Error("shutdown error", "error", err)
		}
	case err := <-errCh:
		if err != nil {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
			os.Exit(1)
		}
	}
}
