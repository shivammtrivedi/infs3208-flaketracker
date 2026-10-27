package ingest

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"flaketracker/internal/httpx"
	"flaketracker/internal/model"
)

// InsertTimeout caps how long any single ingest transaction may stay open.
// The results feed depends on it (see Feed), so the ingest service refuses to
// start unless FEED_LAG is at least twice this.
const InsertTimeout = 4 * time.Second

const (
	defaultFeedLimit = 1000
	maxFeedLimit     = 10000
)

// NewFeedHandler serves GET /internal/v1/results?after_id=<cursor>&limit=<n>,
// the results feed the analyzer pulls from. It's internal: the Ingress only
// routes /ingest/* to this service, so /internal/* is reachable only from
// inside the cluster (through the ingest ClusterIP Service).
func NewFeedHandler(pool *pgxpool.Pool, lag time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		afterID, err := httpx.QueryInt64(r, "after_id", 0, 0, math.MaxInt64)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		limit, err := httpx.QueryInt(r, "limit", defaultFeedLimit, 1, maxFeedLimit)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}

		page, err := Feed(r.Context(), pool, afterID, limit, lag)
		if err != nil {
			slog.ErrorContext(r.Context(), "ingest: feed query failed", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, page)
	}
}

// Feed returns up to limit results with an ID greater than afterID, lowest ID
// first. The caller remembers the last ID it received and passes it back next
// time - a cursor, so no result is ever handed out twice or skipped.
//
// Why "skipped" is a real risk: Postgres hands out BIGSERIAL IDs when a row
// is INSERTed, not when its transaction commits. A slow transaction holding
// ID 100 can commit *after* a fast one holding ID 101. If the analyzer read
// 101 first, its cursor would move past 100 and never come back for it.
//
// The guarantee, in two parts:
//  1. Every ingest transaction finishes (commits or rolls back) within
//     InsertTimeout (4 s), enforced with a context deadline in the handler.
//  2. The feed only returns an unbroken run of results whose run was
//     ingested at least `lag` (10 s) ago, and stops at the first newer one.
//
// If result B is returned, B's transaction started more than 10 s ago and
// inserted B within 4 s of that. Any result A with a smaller ID was inserted
// before B, so A's transaction also started more than 6 s ago and - lasting
// at most 4 s - has already committed (so the query can see it) or rolled
// back (so it never existed). Either way nothing below B can appear later.
func Feed(ctx context.Context, pool *pgxpool.Pool, afterID int64, limit int, lag time.Duration) (model.FeedPage, error) {
	// The CTE ("WITH next AS ...") takes the next `limit` results by ID. The
	// outer query then keeps only those *before* the first one that is too
	// recent - truncating, not filtering, so the returned IDs have no gaps.
	// ingested_at is the run's transaction start time (Postgres now()).
	rows, err := pool.Query(ctx, `
		WITH next AS (
			SELECT tr.id, tr.test_case_id, r.full_name, tc.suite, tc.name,
			       ru.started_at, ru.commit_sha, tr.final_status, tr.passed_on_retry,
			       ru.ingested_at
			FROM test_results tr
			JOIN test_runs ru   ON ru.id = tr.run_id
			JOIN test_cases tc  ON tc.id = tr.test_case_id
			JOIN repositories r ON r.id = tc.repository_id
			WHERE tr.id > $1
			ORDER BY tr.id
			LIMIT $2
		)
		SELECT id, test_case_id, full_name, suite, name, started_at, commit_sha, final_status, passed_on_retry
		FROM next
		WHERE id < COALESCE(
			(SELECT min(id) FROM next WHERE ingested_at >= now() - ($3 * interval '1 millisecond')),
			9223372036854775807)
		ORDER BY id`,
		afterID, limit, lag.Milliseconds(),
	)
	if err != nil {
		return model.FeedPage{}, err
	}
	defer rows.Close()

	// Start with an empty (non-nil) slice so an empty page encodes as
	// "results": [] rather than "results": null.
	page := model.FeedPage{Results: []model.FeedRow{}, NextAfterID: afterID}
	for rows.Next() {
		var f model.FeedRow
		if err := rows.Scan(&f.ResultID, &f.TestCaseID, &f.Repository, &f.Suite, &f.Name,
			&f.StartedAt, &f.CommitSHA, &f.FinalStatus, &f.PassedOnRetry); err != nil {
			return model.FeedPage{}, err
		}
		page.Results = append(page.Results, f)
		page.NextAfterID = f.ResultID
	}
	return page, rows.Err()
}
