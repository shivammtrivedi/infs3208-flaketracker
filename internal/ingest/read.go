package ingest

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"flaketracker/internal/httpx"
	"flaketracker/internal/model"
)

// Reader serves ingest's internal read endpoints (Section 9.1). The api
// service calls these to answer the dashboard's run-explorer and history
// questions - ingest-db is ingest's private database, so this HTTP API is the
// only way any other service can see its raw data.
//
// Like the feed, these live under /internal/*, which the Ingress never routes
// to; they're reachable only inside the cluster.
type Reader struct {
	pool *pgxpool.Pool
}

// NewReader returns the read handlers, backed by ingest-db.
func NewReader(pool *pgxpool.Pool) *Reader {
	return &Reader{pool: pool}
}

// Repos serves GET /internal/v1/repos: every repository with its test and
// run counts and the branches seen, for the dashboard's filter dropdowns.
func (h *Reader) Repos(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT r.full_name,
		       (SELECT count(*) FROM test_cases tc WHERE tc.repository_id = r.id),
		       (SELECT count(*) FROM test_runs ru WHERE ru.repository_id = r.id),
		       COALESCE((SELECT array_agg(DISTINCT ru.branch ORDER BY ru.branch)
		                 FROM test_runs ru WHERE ru.repository_id = r.id), '{}'),
		       (SELECT max(ru.started_at) FROM test_runs ru WHERE ru.repository_id = r.id)
		FROM repositories r
		ORDER BY r.full_name`)
	if err != nil {
		h.fail(w, r, "list repos", err)
		return
	}
	defer rows.Close()

	list := model.RepoList{Repos: []model.Repo{}}
	for rows.Next() {
		var repo model.Repo
		// pgx scans a Postgres text[] straight into a Go []string, and a
		// NULL timestamp into a nil *time.Time.
		if err := rows.Scan(&repo.Name, &repo.Tests, &repo.Runs, &repo.Branches, &repo.LastRunAt); err != nil {
			h.fail(w, r, "scan repo", err)
			return
		}
		list.Repos = append(list.Repos, repo)
	}
	if err := rows.Err(); err != nil {
		h.fail(w, r, "list repos", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

// Test serves GET /internal/v1/tests/{id}: who a test is (404 if unknown).
// api uses it to check a test exists before quarantining it.
func (h *Reader) Test(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := h.identity(r, id)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "test not found")
		return
	}
	if err != nil {
		h.fail(w, r, "get test", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

// History serves GET /internal/v1/tests/{id}/history?limit=50: a test's most
// recent results, newest first, each with the run it came from.
func (h *Reader) History(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := httpx.QueryInt(r, "limit", 50, 1, 500)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	t, err := h.identity(r, id)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "test not found")
		return
	}
	if err != nil {
		h.fail(w, r, "get test", err)
		return
	}

	rows, err := h.pool.Query(r.Context(), `
		SELECT tr.id, ru.id, ru.ci_run_id, ru.ci_run_attempt, ru.branch, ru.commit_sha,
		       COALESCE(ru.run_url, ''), ru.started_at, tr.final_status, tr.attempts,
		       tr.passed_on_retry, tr.duration_ms, COALESCE(tr.error_message, '')
		FROM test_results tr
		JOIN test_runs ru ON ru.id = tr.run_id
		WHERE tr.test_case_id = $1
		ORDER BY ru.started_at DESC, tr.id DESC
		LIMIT $2`, id, limit)
	if err != nil {
		h.fail(w, r, "test history", err)
		return
	}
	defer rows.Close()

	history := model.TestHistory{Test: t, Results: []model.HistoryEntry{}}
	for rows.Next() {
		var e model.HistoryEntry
		if err := rows.Scan(&e.ResultID, &e.RunID, &e.CIRunID, &e.CIRunAttempt, &e.Branch, &e.CommitSHA,
			&e.RunURL, &e.StartedAt, &e.FinalStatus, &e.Attempts, &e.PassedOnRetry, &e.DurationMs, &e.ErrorMessage); err != nil {
			h.fail(w, r, "scan history", err)
			return
		}
		history.Results = append(history.Results, e)
	}
	if err := rows.Err(); err != nil {
		h.fail(w, r, "test history", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, history)
}

// runCounts is a LATERAL subquery that tallies one run's results. It's shared
// by the run list and run detail queries; `ru` must be the test_runs alias.
// The (run_id, test_case_id) unique index makes each tally an index scan.
const runCounts = `
	CROSS JOIN LATERAL (
		SELECT count(*),
		       count(*) FILTER (WHERE final_status = 'passed'),
		       count(*) FILTER (WHERE final_status = 'failed'),
		       count(*) FILTER (WHERE final_status = 'skipped'),
		       count(*) FILTER (WHERE passed_on_retry)
		FROM test_results WHERE run_id = ru.id
	) c(total, passed, failed, skipped, retried)`

const runColumns = `
	ru.id, r.full_name, ru.ci_run_id, ru.ci_run_attempt, ru.branch, ru.commit_sha,
	COALESCE(ru.run_url, ''), ru.started_at, ru.ingested_at,
	c.total, c.passed, c.failed, c.skipped, c.retried`

// scanRun reads one row of runColumns.
func scanRun(row pgx.Row) (model.Run, error) {
	var run model.Run
	err := row.Scan(&run.RunID, &run.Repository, &run.CIRunID, &run.CIRunAttempt, &run.Branch, &run.CommitSHA,
		&run.RunURL, &run.StartedAt, &run.IngestedAt,
		&run.Total, &run.Passed, &run.Failed, &run.Skipped, &run.Retried)
	return run, err
}

// Runs serves GET /internal/v1/runs?repo=&branch=&limit=50&offset=0: CI runs,
// newest first, with result counts.
func (h *Reader) Runs(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	branch := r.URL.Query().Get("branch")
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

	// An empty parameter disables its filter, so this stays one fixed SQL
	// string with user input only ever passed as parameters ($1, $2).
	const where = `WHERE ($1 = '' OR r.full_name = $1) AND ($2 = '' OR ru.branch = $2)`

	list := model.RunList{Runs: []model.Run{}, Limit: limit, Offset: offset}
	err = h.pool.QueryRow(r.Context(),
		`SELECT count(*) FROM test_runs ru JOIN repositories r ON r.id = ru.repository_id `+where,
		repo, branch).Scan(&list.Total)
	if err != nil {
		h.fail(w, r, "count runs", err)
		return
	}

	// The CTE picks the page of runs first, so the counts are only computed
	// for the (at most 200) runs actually returned.
	rows, err := h.pool.Query(r.Context(), `
		WITH page AS (
			SELECT ru.id FROM test_runs ru JOIN repositories r ON r.id = ru.repository_id `+where+`
			ORDER BY ru.started_at DESC, ru.id DESC
			LIMIT $3 OFFSET $4
		)
		SELECT `+runColumns+`
		FROM page p
		JOIN test_runs ru ON ru.id = p.id
		JOIN repositories r ON r.id = ru.repository_id`+runCounts+`
		ORDER BY ru.started_at DESC, ru.id DESC`,
		repo, branch, limit, offset)
	if err != nil {
		h.fail(w, r, "list runs", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			h.fail(w, r, "scan run", err)
			return
		}
		list.Runs = append(list.Runs, run)
	}
	if err := rows.Err(); err != nil {
		h.fail(w, r, "list runs", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

// Run serves GET /internal/v1/runs/{id}: one run and all its results -
// failures first, then retried passes, then passes, then skips.
func (h *Reader) Run(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	run, err := scanRun(h.pool.QueryRow(r.Context(), `
		SELECT `+runColumns+`
		FROM test_runs ru
		JOIN repositories r ON r.id = ru.repository_id`+runCounts+`
		WHERE ru.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		h.fail(w, r, "get run", err)
		return
	}

	rows, err := h.pool.Query(r.Context(), `
		SELECT tr.test_case_id, tc.suite, tc.name, tr.final_status, tr.attempts,
		       tr.passed_on_retry, tr.duration_ms, COALESCE(tr.error_message, '')
		FROM test_results tr
		JOIN test_cases tc ON tc.id = tr.test_case_id
		WHERE tr.run_id = $1
		ORDER BY CASE WHEN tr.final_status = 'failed' THEN 0
		              WHEN tr.passed_on_retry         THEN 1
		              WHEN tr.final_status = 'passed' THEN 2
		              ELSE 3 END,
		         tc.suite, tc.name`, id)
	if err != nil {
		h.fail(w, r, "run results", err)
		return
	}
	defer rows.Close()

	detail := model.RunDetail{Run: run, Results: []model.RunResult{}}
	for rows.Next() {
		var res model.RunResult
		if err := rows.Scan(&res.TestCaseID, &res.Suite, &res.Name, &res.FinalStatus, &res.Attempts,
			&res.PassedOnRetry, &res.DurationMs, &res.ErrorMessage); err != nil {
			h.fail(w, r, "scan run result", err)
			return
		}
		detail.Results = append(detail.Results, res)
	}
	if err := rows.Err(); err != nil {
		h.fail(w, r, "run results", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

// Summary serves GET /internal/v1/summary: headline counts for the overview.
func (h *Reader) Summary(w http.ResponseWriter, r *http.Request) {
	var s model.IngestSummary
	err := h.pool.QueryRow(r.Context(), `
		SELECT (SELECT count(*) FROM repositories),
		       (SELECT count(*) FROM test_cases),
		       (SELECT count(*) FROM test_runs),
		       (SELECT count(*) FROM test_results)`,
	).Scan(&s.Repositories, &s.Tests, &s.RunsIngested, &s.ResultsIngested)
	if err != nil {
		h.fail(w, r, "summary", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s)
}

// identity looks up one test's repository, suite and name.
func (h *Reader) identity(r *http.Request, id int64) (model.TestIdentity, error) {
	var t model.TestIdentity
	err := h.pool.QueryRow(r.Context(), `
		SELECT tc.id, r.full_name, tc.suite, tc.name, tc.first_seen_at
		FROM test_cases tc JOIN repositories r ON r.id = tc.repository_id
		WHERE tc.id = $1`, id,
	).Scan(&t.TestCaseID, &t.Repository, &t.Suite, &t.Name, &t.FirstSeenAt)
	return t, err
}

func (h *Reader) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.ErrorContext(r.Context(), "ingest read: "+what, "error", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal server error")
}
