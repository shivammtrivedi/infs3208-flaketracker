// cmd/api is the api service: the dashboard's public read API and the owner
// of quarantine decisions (api-db). It reaches ingest and analyzer only
// through their internal HTTP APIs.
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

	"flaketracker/internal/api"
	"flaketracker/internal/clients"
	"flaketracker/internal/config"
	"flaketracker/internal/db"
	"flaketracker/internal/httpx"
	"flaketracker/internal/quarantine"
	"flaketracker/internal/version"
)

func main() {
	// `app healthcheck` checks this service's own /readyz and exits (used by Docker Compose).
	httpx.HealthcheckCommand()

	slog.SetDefault(httpx.NewLogger(os.Stdout))

	cfg, err := config.LoadAPI()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx) // api-db, via this pod's own DB_* settings
	if err != nil {
		slog.Error("failed to create database pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	server := api.NewServer(
		clients.NewIngest(cfg.IngestURL, cfg.DownstreamTimeout),
		clients.NewAnalyzer(cfg.AnalyzerURL, cfg.DownstreamTimeout),
		quarantine.NewStore(pool),
	)

	mux := http.NewServeMux()
	server.Register(mux)
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	// Readiness checks only api's OWN database. If it also checked ingest or
	// analyzer, an outage there would pull every api pod out of the load
	// balancer - and the endpoints that don't need that service (quarantine
	// list, etc.) would go down with them. That's the cascading failure
	// database-per-service is meant to avoid.
	mux.HandleFunc("GET /readyz", httpx.Readyz(pool))

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      httpx.Chain(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	slog.Info("api starting", "port", cfg.Port, "commit", version.Commit,
		"ingest_url", cfg.IngestURL, "analyzer_url", cfg.AnalyzerURL)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
}
