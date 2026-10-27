// cmd/analyzer is the analyzer service: a background compute loop (only the
// leader replica runs it) plus an internal HTTP API that every replica serves.
// It owns analyzer-db and gets ingest's data only through ingest's feed.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flaketracker/internal/analyzer"
	"flaketracker/internal/clients"
	"flaketracker/internal/config"
	"flaketracker/internal/db"
	"flaketracker/internal/httpx"
	"flaketracker/internal/version"
)

func main() {
	// `app healthcheck` checks this service's own /readyz and exits (used by Docker Compose).
	httpx.HealthcheckCommand()

	slog.SetDefault(httpx.NewLogger(os.Stdout))

	cfg, err := config.LoadAnalyzer()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// Cancelled on Ctrl+C or on the SIGTERM Kubernetes sends before stopping a pod.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx) // analyzer-db, via this pod's own DB_* settings
	if err != nil {
		slog.Error("failed to create database pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// A generous timeout: a feed page can carry up to 10,000 results.
	feed := clients.NewIngest(cfg.IngestURL, 30*time.Second)
	an, err := analyzer.New(pool, feed, version.Algorithm, cfg.Scoring, analyzer.Settings{
		Interval:     cfg.Interval,
		FeedPageSize: cfg.FeedPageSize,
		FeedMaxPages: cfg.FeedMaxPages,
		RescoreBatch: cfg.RescoreBatch,
	})
	if err != nil {
		slog.Error("invalid algorithm version baked into this build", "error", err)
		os.Exit(1)
	}

	api := analyzer.NewAPI(pool, version.Algorithm)
	mux := http.NewServeMux()
	// Method-value syntax: api.ListTests is a function value with `api`
	// already bound as its receiver, so it fits http.HandlerFunc directly.
	mux.HandleFunc("GET /internal/v1/tests", api.ListTests)
	mux.HandleFunc("GET /internal/v1/tests/{id}", api.GetTest)
	mux.HandleFunc("GET /internal/v1/summary", api.Summary)
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	mux.HandleFunc("GET /readyz", httpx.Readyz(pool))

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      httpx.Chain(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	slog.Info("analyzer starting",
		"port", cfg.Port, "algorithm", version.Algorithm, "commit", version.Commit,
		"ingest_url", cfg.IngestURL, "interval", cfg.Interval.String())

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	// The compute loop runs in its own goroutine. `done` is closed when it
	// returns, which lets main wait for it to give up leadership before exiting.
	// `chan struct{}` carries no data - closing it is the whole signal.
	done := make(chan struct{})
	go func() {
		an.Run(ctx)
		close(done)
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
	// Wait for the loop to finish its current cycle and release the leader
	// lock, so a standby replica can take over straight away.
	<-done
}
