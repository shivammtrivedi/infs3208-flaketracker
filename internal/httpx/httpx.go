// Package httpx provides shared HTTP middleware: logging, API-key auth, panic
// recovery, and Kubernetes health checks. It's used by every HTTP-serving
// binary (ingest, api) so this logic is written once.
//
// Background for readers new to Go: an http.Handler is any type with a method
// `ServeHTTP(w http.ResponseWriter, r *http.Request)`. "Middleware" is just a
// function that takes a Handler and returns a new Handler wrapping it - the
// same idea as a decorator in Python (e.g. FastAPI's @app.middleware) or a
// Filter/Interceptor in Spring, just without a framework providing the
// wiring: in Go you nest the wrapping calls yourself (see cmd/ingest/main.go).
package httpx

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WithLogging wraps a handler so every request is logged with its method,
// path, status code, and how long it took.
func WithLogging(next http.Handler) http.Handler {
	// http.HandlerFunc is a type: "a function with this exact signature".
	// Converting our closure `func(w, r) {...}` to http.HandlerFunc is what
	// lets a plain function satisfy the http.Handler interface - Go doesn't
	// have "implements" keywords, a type satisfies an interface just by
	// having the right methods, and HandlerFunc's one method is itself.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Plain http.ResponseWriter has no way to ask "what status code did
		// the handler send?" after the fact, so we wrap it in a small
		// recorder that remembers the status for the log line below.
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		// Kubernetes probes hit these every few seconds per pod; logging
		// them would bury the real traffic.
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			return
		}
		// The ...Context variant lets the log handler (requestid.go) attach
		// this request's ID to the line.
		slog.InfoContext(r.Context(), "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// statusRecorder decorates http.ResponseWriter to capture the status code
// the wrapped handler writes.
//
// Embedding http.ResponseWriter as an unnamed field (just the type, no field
// name) means statusRecorder automatically gets all of ResponseWriter's
// methods "for free" - this is Go's version of inheritance/composition. We
// only need to override the one method we care about, WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// WithRecover wraps a handler so a panic inside it becomes a 500 response
// instead of crashing the whole process (one bad request should never take
// down the other in-flight requests a Go server is handling concurrently).
func WithRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// `defer` schedules a function call to run right before the
		// surrounding function returns - including when it returns because
		// of a panic. `recover()` only has an effect when called inside a
		// deferred function; it stops the panic from propagating further up
		// the call stack and gives us back whatever value was panicked with.
		defer func() {
			if err := recover(); err != nil {
				slog.ErrorContext(r.Context(), "panic recovered", "error", err, "path", r.URL.Path)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// RequireAPIKey wraps a handler so requests must present a valid X-API-Key
// header matching key. The caller (main.go) is responsible for making sure
// key is non-empty before wiring this up - an empty configured key would
// otherwise match an empty (missing) header.
func RequireAPIKey(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-API-Key")

		// subtle.ConstantTimeCompare takes the same amount of time no matter
		// where two byte slices first differ. A plain `got == key` check
		// would return as soon as it finds a mismatched byte, and an
		// attacker who can measure response time precisely enough could use
		// that to guess the key one byte at a time. This only matters
		// because we're comparing a secret; ordinary string comparisons
		// elsewhere in the codebase don't need this.
		if subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Healthz is a liveness probe: it only proves the process is alive and able
// to respond, so it deliberately never touches the database. Kubernetes
// restarts a pod that fails this.
func Healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// Readyz is a readiness probe: it pings the database, so Kubernetes only
// routes traffic to this pod once it can actually serve a request. Returning
// a function (a closure) here, rather than a plain function, is how this
// handler "remembers" the pool it needs - Go has no dependency-injection
// container, so handlers carry their own dependencies like this instead.
func Readyz(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := pool.Ping(ctx); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}
