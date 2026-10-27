package analyzer

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"flaketracker/internal/httpx"
	"flaketracker/internal/model"
	"flaketracker/internal/version"
)

// API serves the analyzer's internal read endpoints (Section 9.3). Every
// replica serves them - leader or not - straight from analyzer-db, so reads
// stay available even while leadership is changing hands.
type API struct {
	pool    *pgxpool.Pool
	version string
}

// NewAPI returns the handlers for the analyzer's internal endpoints.
func NewAPI(pool *pgxpool.Pool, version string) *API {
	return &API{pool: pool, version: version}
}

var validClassifications = map[string]bool{"": true, "stable": true, "flaky": true, "broken": true, "insufficient_data": true}

// statsColumns is shared by the list and single-test queries so both scan
// rows the same way (see scanStats).
const statsColumns = `
	t.test_case_id, t.repository, t.suite, t.name, s.classification, s.flakiness_score,
	s.flip_rate, s.retry_pass_rate, s.commit_conflict_rate, s.window_size, s.sample_size,
	s.pass_count, s.fail_count, s.algorithm_version, s.last_result_at, s.computed_at`

// ListTests serves GET /internal/v1/tests?repo=&classification=&q=&limit=&offset=
// - the leaderboard, highest flakiness score first.
func (h *API) ListTests(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	repo := query.Get("repo")
	classification := query.Get("classification")
	if !validClassifications[classification] {
		httpx.WriteError(w, http.StatusBadRequest, "classification must be one of stable, flaky, broken, insufficient_data")
		return
	}
	limit, err := httpx.QueryInt(r, "limit", 50, 1, 200)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	offset, err := httpx.QueryInt(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	pattern := ""
	if q := strings.TrimSpace(query.Get("q")); q != "" {
		pattern = "%" + escapeLike(q) + "%"
	}

	// The filters are written so an empty parameter disables them
	// ($1 = '' OR ...), which keeps this one fixed SQL string - never built
	// by concatenating user input, so there's no SQL injection risk.
	const where = `
		WHERE ($1 = '' OR t.repository = $1)
		  AND ($2 = '' OR s.classification = $2)
		  AND ($3 = '' OR t.name ILIKE $3 OR t.suite ILIKE $3)`

	list := model.TestList{Tests: []model.TestStats{}, Limit: limit, Offset: offset}
	err = h.pool.QueryRow(r.Context(),
		`SELECT count(*) FROM test_stats s JOIN tests t USING (test_case_id)`+where,
		repo, classification, pattern,
	).Scan(&list.Total)
	if err != nil {
		h.fail(w, r, "count tests", err)
		return
	}

	rows, err := h.pool.Query(r.Context(),
		`SELECT`+statsColumns+` FROM test_stats s JOIN tests t USING (test_case_id)`+where+`
		 ORDER BY s.flakiness_score DESC, t.test_case_id
		 LIMIT $4 OFFSET $5`,
		repo, classification, pattern, limit, offset,
	)
	if err != nil {
		h.fail(w, r, "list tests", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		ts, err := scanStats(rows)
		if err != nil {
			h.fail(w, r, "scan test", err)
			return
		}
		list.Tests = append(list.Tests, ts)
	}
	if err := rows.Err(); err != nil {
		h.fail(w, r, "list tests", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

// GetTest serves GET /internal/v1/tests/{id}: one test's stats, 404 if it
// hasn't been scored yet.
func (h *API) GetTest(w http.ResponseWriter, r *http.Request) {
	// r.PathValue reads the {id} wildcard from the route pattern (Go 1.22+).
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "id must be an integer")
		return
	}
	row := h.pool.QueryRow(r.Context(),
		`SELECT`+statsColumns+` FROM test_stats s JOIN tests t USING (test_case_id) WHERE t.test_case_id = $1`, id)
	ts, err := scanStats(row)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "test not found or not scored yet")
		return
	}
	if err != nil {
		h.fail(w, r, "get test", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ts)
}

// Summary serves GET /internal/v1/summary: counts for the overview dashboard
// plus the last 30 days of flaky/broken history.
func (h *API) Summary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := model.AnalyzerSummary{
		AlgorithmVersion: h.version,
		Commit:           version.Commit,
		Classifications:  map[string]int{"stable": 0, "flaky": 0, "broken": 0, "insufficient_data": 0},
		ScoredBy:         map[string]int{},
		Daily:            []model.DailyCount{},
	}

	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM tests`).Scan(&s.TestsTracked); err != nil {
		h.fail(w, r, "count tests", err)
		return
	}
	if err := countInto(ctx, h.pool, `SELECT classification, count(*) FROM test_stats GROUP BY 1`, s.Classifications); err != nil {
		h.fail(w, r, "count classifications", err)
		return
	}
	if err := countInto(ctx, h.pool, `SELECT algorithm_version, count(*) FROM test_stats GROUP BY 1`, s.ScoredBy); err != nil {
		h.fail(w, r, "count algorithm versions", err)
		return
	}
	if err := h.pool.QueryRow(ctx, `SELECT updated_at FROM sync_state WHERE id = 1`).Scan(&s.LastCycleAt); err != nil {
		h.fail(w, r, "read last cycle", err)
		return
	}

	first := truncateToDay(time.Now()).AddDate(0, 0, -29)
	rows, err := h.pool.Query(ctx,
		`SELECT day, flaky_count, broken_count FROM daily_flaky_counts WHERE day >= $1 ORDER BY day`, first)
	if err != nil {
		h.fail(w, r, "read daily counts", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var d model.DailyCount
		if err := rows.Scan(&day, &d.Flaky, &d.Broken); err != nil {
			h.fail(w, r, "scan daily count", err)
			return
		}
		d.Day = day.Format(time.DateOnly)
		s.Daily = append(s.Daily, d)
	}
	if err := rows.Err(); err != nil {
		h.fail(w, r, "read daily counts", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s)
}

func (h *API) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.ErrorContext(r.Context(), "analyzer api: "+what, "error", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal server error")
}

// scanStats reads one row of statsColumns. pgx.Row is the interface shared by
// a single QueryRow result and each row of a Query, so one function serves both.
func scanStats(row pgx.Row) (model.TestStats, error) {
	var ts model.TestStats
	err := row.Scan(&ts.TestCaseID, &ts.Repository, &ts.Suite, &ts.Name, &ts.Classification, &ts.FlakinessScore,
		&ts.FlipRate, &ts.RetryPassRate, &ts.CommitConflictRate, &ts.WindowSize, &ts.SampleSize,
		&ts.PassCount, &ts.FailCount, &ts.AlgorithmVersion, &ts.LastResultAt, &ts.ComputedAt)
	return ts, err
}

// countInto runs a "SELECT key, count(*) ... GROUP BY 1" query into a map.
func countInto(ctx context.Context, pool *pgxpool.Pool, sql string, into map[string]int) error {
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return err
		}
		into[key] = n
	}
	return rows.Err()
}

// escapeLike makes a search term match literally inside an ILIKE pattern:
// without it, typing "%" or "_" would act as a wildcard. Backslash is
// Postgres's default LIKE escape character.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
