// Package api is the api service: FlakeTracker's public read API for the
// dashboard (Section 9.2 of the project plan) and the owner of quarantine
// decisions.
//
// api holds no copy of test results or stats. For each request it calls the
// service that owns the data - ingest (runs, history) and/or analyzer
// (leaderboard, stats) - over internal HTTP, merges in quarantine data from
// its own api-db, and returns a single response. This is sometimes called
// "API composition"; it's how a database-per-service system answers a
// question whose data is spread across services.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"flaketracker/internal/clients"
	"flaketracker/internal/httpx"
	"flaketracker/internal/model"
)

// QuarantineStore is what the api needs from its database. It's an
// interface (satisfied by *quarantine.Store) so the handler tests can use an
// in-memory fake instead of a real Postgres.
type QuarantineStore interface {
	List(ctx context.Context, repo string) ([]model.Quarantine, error)
	Active(ctx context.Context, testCaseID int64) (*model.Quarantine, error)
	ActiveFor(ctx context.Context, testCaseIDs []int64) (map[int64]bool, error)
	CountActive(ctx context.Context) (int, error)
	Create(ctx context.Context, q model.Quarantine) (model.Quarantine, error)
	Release(ctx context.Context, testCaseID int64) (model.Quarantine, error)
}

// Server holds the api's dependencies: clients for the two downstream
// services, and its own quarantine store.
type Server struct {
	ingest      *clients.Ingest
	analyzer    *clients.Analyzer
	quarantines QuarantineStore
}

// NewServer wires up a Server.
func NewServer(ingest *clients.Ingest, analyzer *clients.Analyzer, quarantines QuarantineStore) *Server {
	return &Server{ingest: ingest, analyzer: analyzer, quarantines: quarantines}
}

// Register adds every public route to mux. All paths start with /api/
// because the Ingress forwards /api/* to this service unchanged.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/summary", s.summary)
	mux.HandleFunc("GET /api/v1/version", s.version)
	mux.HandleFunc("GET /api/v1/repos", s.repos)
	mux.HandleFunc("GET /api/v1/tests", s.listTests)
	mux.HandleFunc("GET /api/v1/tests/{id}", s.getTest)
	mux.HandleFunc("GET /api/v1/tests/{id}/history", s.history)
	mux.HandleFunc("GET /api/v1/runs", s.listRuns)
	mux.HandleFunc("GET /api/v1/runs/{id}", s.getRun)
	mux.HandleFunc("GET /api/v1/quarantine", s.listQuarantines)
	mux.HandleFunc("POST /api/v1/quarantine", s.createQuarantine)
	mux.HandleFunc("DELETE /api/v1/quarantine/{test_case_id}", s.releaseQuarantine)
}

// downstreamError turns a failed call to another service into a response.
//
//   - 400 / 404 from downstream mean "your request is wrong" or "no such
//     thing": pass the status and message straight through to the browser.
//   - Anything else (connection refused, timeout, 5xx) means that service is
//     unavailable: return 503 for THIS endpoint only. Endpoints that don't
//     need that service keep working - this is the failure isolation that
//     database-per-service buys.
func (s *Server) downstreamError(w http.ResponseWriter, r *http.Request, service string, err error) {
	var se *clients.StatusError
	if errors.As(err, &se) && (se.Code == http.StatusBadRequest || se.Code == http.StatusNotFound) {
		httpx.WriteError(w, se.Code, se.Message)
		return
	}
	slog.WarnContext(r.Context(), "api: downstream call failed", "service", service, "error", err)
	httpx.WriteError(w, http.StatusServiceUnavailable, service+" unavailable")
}

// internalError reports a failure of api's own database.
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.ErrorContext(r.Context(), "api: "+what, "error", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal server error")
}

// forward copies the named query parameters (and only those) from the
// incoming request, ready to send downstream. An allow-list like this means
// a caller can't smuggle unexpected parameters through to an internal API.
func forward(r *http.Request, keys ...string) url.Values {
	in := r.URL.Query()
	out := url.Values{}
	for _, k := range keys {
		if v := in.Get(k); v != "" {
			out.Set(k, v)
		}
	}
	return out
}
