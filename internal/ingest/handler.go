package ingest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"flaketracker/internal/httpx"
)

// maxBodyBytes caps request bodies at 5 MB (Section 9.1), so a malicious or
// buggy client can't exhaust server memory with an oversized payload.
const maxBodyBytes = 5 << 20 // 5 * 1024 * 1024, using a bit-shift the way Go code conventionally spells byte sizes

// NewHandler returns the HTTP handler for POST /ingest/v1/runs.
//
// Returning a closure that captures `pool` is Go's usual substitute for
// dependency injection: rather than a framework wiring a database connection
// into a controller (as Spring's @Autowired or FastAPI's Depends() would),
// the handler function itself just closes over whatever it needs.
func NewHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// http.MaxBytesReader makes any read past maxBodyBytes fail, so
		// json.Decode below can't be tricked into reading an unbounded body.
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

		var payload RunPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"errors": []string{"invalid JSON: " + err.Error()},
			})
			return
		}

		if problems := payload.Validate(); len(problems) > 0 {
			httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"errors": problems})
			return
		}

		// r.Context() carries the request's lifecycle: if the client
		// disconnects, anything downstream using it can stop early.
		// WithTimeout derives a child context that is ALSO cancelled after
		// InsertTimeout. pgx aborts the transaction when its context is
		// cancelled, so no ingest transaction can outlive this deadline -
		// the results feed's no-skipped-rows guarantee depends on that (see feed.go).
		ctx, cancel := context.WithTimeout(r.Context(), InsertTimeout)
		defer cancel()

		result, err := Insert(ctx, pool, &payload)
		if err != nil {
			slog.ErrorContext(r.Context(), "ingest: insert failed", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		if result.Duplicate {
			// Section 9.1: a duplicate (repository, ci_run_id, ci_run_attempt)
			// is a 200, not an error - this is what makes client retries safe.
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"run_id": result.RunID, "duplicate": true})
			return
		}
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"run_id": result.RunID, "results": result.Results})
	}
}
