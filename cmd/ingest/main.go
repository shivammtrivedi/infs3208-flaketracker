// main.go is the entry point for the "ingest" binary. In Go, `package main`
// combined with a `func main()` is what makes a program executable - every
// Go program that runs standalone (as opposed to a library package) needs
// exactly one of these, comparable to the `if __name__ == "__main__":` guard
// in Python or a `public static void main` in Java, except mandatory.
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

	"flaketracker/internal/config"
	"flaketracker/internal/db"
	"flaketracker/internal/httpx"
	"flaketracker/internal/ingest"
)

func main() {
	// `app healthcheck` checks this service's own /readyz and exits (used by Docker Compose).
	httpx.HealthcheckCommand()

	// slog is Go's standard structured logger. httpx.NewLogger makes every
	// log line a JSON object on stdout (this is what "structured JSON
	// logging" in Section 6 means in practice) and tags lines with the
	// request's ID, so one request can be followed across services.
	slog.SetDefault(httpx.NewLogger(os.Stdout))

	cfg, err := config.LoadIngest()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	if cfg.FeedLag < 2*ingest.InsertTimeout {
		// The feed's no-skipped-rows guarantee needs the lag to be at least
		// twice the longest possible ingest transaction (see internal/ingest/feed.go).
		slog.Error("FEED_LAG must be at least twice the insert timeout", "feed_lag", cfg.FeedLag, "insert_timeout", ingest.InsertTimeout)
		os.Exit(1)
	}
	if cfg.APIKey == "" {
		// Fail fast at startup rather than at request time: an empty
		// configured key would otherwise match an empty (missing) header in
		// httpx.RequireAPIKey, silently letting every request through.
		slog.Error("INGEST_API_KEY is not set")
		os.Exit(1)
	}

	// context.Background() is the root of every context tree in a Go
	// program - an empty starting point with no deadline and no values.
	// signal.NotifyContext derives a child context that gets cancelled the
	// instant the process receives SIGINT (Ctrl+C locally) or SIGTERM (what
	// Kubernetes sends a pod before killing it). This is how Go does
	// graceful shutdown: instead of dying mid-request, the code below gets a
	// chance to notice ctx.Done() and shut down in an orderly way.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx)
	if err != nil {
		slog.Error("failed to create database pool", "error", err)
		os.Exit(1)
	}
	// `defer pool.Close()` guarantees the pool is closed when main() returns
	// - including via the graceful-shutdown path below - the same "clean up
	// on the way out" pattern used in internal/ingest/store.go for transactions.
	defer pool.Close()

	// http.NewServeMux is Go's built-in router. Since Go 1.22, patterns can
	// include an HTTP method (e.g. "POST /path"), so the mux itself rejects
	// a GET to an ingest-only route without any extra code - no separate
	// routing library needed for a service this small.
	mux := http.NewServeMux()
	mux.Handle("POST /ingest/v1/runs", httpx.RequireAPIKey(cfg.APIKey, ingest.NewHandler(pool)))
	// Internal endpoints (no API key: they're only reachable inside the
	// cluster, because the Ingress forwards nothing but /ingest/* here).
	mux.HandleFunc("GET /internal/v1/results", ingest.NewFeedHandler(pool, cfg.FeedLag))
	reader := ingest.NewReader(pool)
	mux.HandleFunc("GET /internal/v1/repos", reader.Repos)
	mux.HandleFunc("GET /internal/v1/tests/{id}", reader.Test)
	mux.HandleFunc("GET /internal/v1/tests/{id}/history", reader.History)
	mux.HandleFunc("GET /internal/v1/runs", reader.Runs)
	mux.HandleFunc("GET /internal/v1/runs/{id}", reader.Run)
	mux.HandleFunc("GET /internal/v1/summary", reader.Summary)
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	mux.HandleFunc("GET /readyz", httpx.Readyz(pool))

	// httpx.Chain nests the middleware: a request hits WithRequestID first,
	// then WithLogging, then WithRecover, then mux. That nesting is the
	// entire "middleware chain" - there's no separate concept or framework
	// hook for it in the standard library.
	handler := httpx.Chain(mux)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// `go func() { ... }()` starts a goroutine: a lightweight, concurrently
	// scheduled function call (cheaper than an OS thread - a Go program can
	// run thousands of these). We run the server in one so that main() can
	// move on to the `<-ctx.Done()` line below and wait for a shutdown
	// signal while the server handles requests in the background.
	go func() {
		slog.Info("ingest listening", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// ListenAndServe always returns a non-nil error; ErrServerClosed
			// specifically means "someone called srv.Shutdown", which is the
			// expected, non-error way this goroutine ends.
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	// `<-ctx.Done()` receives from the context's Done channel. A channel
	// receive blocks until there's something to receive - here, that's
	// "until ctx is cancelled". This line is what makes main() sit idle,
	// serving requests via the goroutine above, until a shutdown signal
	// arrives.
	<-ctx.Done()
	slog.Info("shutting down")

	// Give in-flight requests up to 10 seconds to finish before forcing the
	// server closed - this is what "graceful shutdown on SIGTERM" (Phase 2)
	// means: a rolling Kubernetes update shouldn't cut off a request that
	// was already halfway through.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
}
