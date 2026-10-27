package analyzer

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"flaketracker/internal/model"
	"flaketracker/internal/scoring"
)

// CycleStats summarises one cycle, for logging.
type CycleStats struct {
	Pulled        int    // feed rows stored as observations
	Rescored      int    // tests whose stats were recomputed
	DaysRefreshed int    // daily_flaky_counts rows recomputed
	FeedError     string // non-empty if ingest couldn't be reached this cycle
	Duration      time.Duration
}

// Cycle runs one leader cycle (Section 10.1a of the project plan):
//
//  1. pull new results from ingest's feed, page by page, into observations;
//  2. advance the feed cursor;
//  3. re-score tests that have new data, no stats yet, or stats from a
//     different algorithm version (this is what makes a v1<->v2 rollout or
//     rollback take effect without any new data arriving);
//  4. refresh the daily flaky-count history.
//
// It all happens in ONE analyzer-db transaction, so the cursor can never
// move ahead of the data it points past: after a crash mid-cycle the whole
// cycle simply rolls back and the next one re-pulls the same pages.
func (a *Analyzer) Cycle(ctx context.Context) (CycleStats, error) {
	started := time.Now()
	var stats CycleStats

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback(ctx) // no-op after a successful Commit

	var cursor int64
	if err := tx.QueryRow(ctx, `SELECT last_result_id FROM sync_state WHERE id = 1`).Scan(&cursor); err != nil {
		return stats, fmt.Errorf("read cursor: %w", err)
	}

	// Step 1: pull. If ingest is down we log it and carry on with steps 3–4
	// using the data we already have - failure isolation: an ingest outage
	// makes stats a little stale but doesn't stop the analyzer working.
	var dirtyFrom time.Time // earliest started_at among new rows (zero = nothing new)
	for page := 0; page < a.settings.FeedMaxPages; page++ {
		fp, err := a.feed.Results(ctx, cursor, a.settings.FeedPageSize)
		if err != nil {
			stats.FeedError = err.Error()
			break
		}
		if len(fp.Results) == 0 {
			break
		}
		earliest, err := storeFeedRows(ctx, tx, fp.Results)
		if err != nil {
			return stats, fmt.Errorf("store feed rows: %w", err)
		}
		if dirtyFrom.IsZero() || earliest.Before(dirtyFrom) {
			dirtyFrom = earliest
		}
		stats.Pulled += len(fp.Results)
		cursor = fp.NextAfterID
		if len(fp.Results) < a.settings.FeedPageSize {
			break // a short page means we've caught up
		}
	}

	// Step 2: the cursor moves in the same transaction as the rows it covers.
	if _, err := tx.Exec(ctx, `UPDATE sync_state SET last_result_id = $1, updated_at = now() WHERE id = 1`, cursor); err != nil {
		return stats, fmt.Errorf("update cursor: %w", err)
	}

	now := time.Now().UTC()
	if stats.Rescored, err = a.rescore(ctx, tx, now); err != nil {
		return stats, fmt.Errorf("rescore: %w", err)
	}
	if stats.DaysRefreshed, err = a.refreshDaily(ctx, tx, now, dirtyFrom); err != nil {
		return stats, fmt.Errorf("refresh daily counts: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return stats, err
	}
	stats.Duration = time.Since(started)
	return stats, nil
}

// storeFeedRows upserts the tests and inserts the observations from one feed
// page, returning the earliest started_at it saw.
//
// Rather than one INSERT per row (5,000 round trips per page), each table gets
// ONE statement: the values are sent as parallel arrays and Postgres's
// unnest() turns them back into rows. pgx converts Go slices like []int64 and
// []string to Postgres arrays automatically.
func storeFeedRows(ctx context.Context, tx pgx.Tx, rows []model.FeedRow) (time.Time, error) {
	var earliest time.Time

	// Tests: one entry per distinct test (an upsert can't touch the same row
	// twice in one statement, so duplicates within the page are skipped).
	seen := make(map[int64]bool)
	var testIDs []int64
	var repos, suites, names []string

	// Observations: one entry per feed row.
	var resultIDs, obsTestIDs []int64
	var startedAts []time.Time
	var commits, statuses []string
	var retries []bool

	for _, f := range rows {
		if !seen[f.TestCaseID] {
			seen[f.TestCaseID] = true
			testIDs = append(testIDs, f.TestCaseID)
			repos = append(repos, f.Repository)
			suites = append(suites, f.Suite)
			names = append(names, f.Name)
		}
		resultIDs = append(resultIDs, f.ResultID)
		obsTestIDs = append(obsTestIDs, f.TestCaseID)
		startedAts = append(startedAts, f.StartedAt)
		commits = append(commits, f.CommitSHA)
		statuses = append(statuses, f.FinalStatus)
		retries = append(retries, f.PassedOnRetry)
		if earliest.IsZero() || f.StartedAt.Before(earliest) {
			earliest = f.StartedAt
		}
	}

	// New tests get needs_rescore = true from the column default; tests we
	// already knew get it set to true because they have new observations.
	if _, err := tx.Exec(ctx, `
		INSERT INTO tests (test_case_id, repository, suite, name)
		SELECT * FROM unnest($1::bigint[], $2::text[], $3::text[], $4::text[])
		ON CONFLICT (test_case_id) DO UPDATE SET needs_rescore = true`,
		testIDs, repos, suites, names,
	); err != nil {
		return earliest, err
	}

	// ON CONFLICT DO NOTHING on result_id makes re-pulling a page harmless.
	if _, err := tx.Exec(ctx, `
		INSERT INTO observations (result_id, test_case_id, started_at, commit_sha, final_status, passed_on_retry)
		SELECT * FROM unnest($1::bigint[], $2::bigint[], $3::timestamptz[], $4::text[], $5::text[], $6::bool[])
		ON CONFLICT (result_id) DO NOTHING`,
		resultIDs, obsTestIDs, startedAts, commits, statuses, retries,
	); err != nil {
		return earliest, err
	}
	return earliest, nil
}

// rescore recomputes stats for up to RescoreBatch tests that need it.
func (a *Analyzer) rescore(ctx context.Context, tx pgx.Tx, now time.Time) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.test_case_id
		FROM tests t
		LEFT JOIN test_stats s USING (test_case_id)
		WHERE t.needs_rescore OR s.test_case_id IS NULL OR s.algorithm_version <> $1
		ORDER BY t.test_case_id
		LIMIT $2`,
		a.version, a.settings.RescoreBatch,
	)
	if err != nil {
		return 0, err
	}
	// pgx.CollectRows reads every row through a conversion function.
	// pgx.RowTo[int64] is a *generic* function - the [int64] picks the type
	// to scan each single-column row into - so this returns a []int64.
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || len(ids) == 0 {
		return 0, err
	}

	windows, err := loadWindows(ctx, tx, ids, now, a.params.WindowSize)
	if err != nil {
		return 0, err
	}

	// A pgx.Batch queues many statements and sends them to Postgres in a
	// single network round trip, instead of one round trip per test.
	batch := &pgx.Batch{}
	for _, id := range ids {
		r := a.algorithm(windows[id], a.params, now)
		var lastResultAt any // nil -> SQL NULL for a test with no non-skipped results
		if !r.LastResultAt.IsZero() {
			lastResultAt = r.LastResultAt
		}
		batch.Queue(`
			INSERT INTO test_stats (test_case_id, window_size, sample_size, pass_count, fail_count,
				flip_rate, retry_pass_rate, commit_conflict_rate, flakiness_score,
				classification, algorithm_version, last_result_at, computed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
			ON CONFLICT (test_case_id) DO UPDATE SET
				window_size = EXCLUDED.window_size, sample_size = EXCLUDED.sample_size,
				pass_count = EXCLUDED.pass_count, fail_count = EXCLUDED.fail_count,
				flip_rate = EXCLUDED.flip_rate, retry_pass_rate = EXCLUDED.retry_pass_rate,
				commit_conflict_rate = EXCLUDED.commit_conflict_rate, flakiness_score = EXCLUDED.flakiness_score,
				classification = EXCLUDED.classification, algorithm_version = EXCLUDED.algorithm_version,
				last_result_at = EXCLUDED.last_result_at, computed_at = now()`,
			id, r.WindowSize, r.SampleSize, r.PassCount, r.FailCount,
			r.FlipRate, r.RetryPassRate, r.CommitConflictRate, r.FlakinessScore,
			string(r.Classification), r.AlgorithmVersion, lastResultAt,
		)
	}
	batch.Queue(`UPDATE tests SET needs_rescore = false WHERE test_case_id = ANY($1)`, ids)

	// Close sends nothing new; it waits for every queued statement and
	// returns the first error, if any.
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// refreshDaily keeps daily_flaky_counts current for the last 30 days (UTC).
//
// A day is recomputed if it has no row yet, if its row was computed by a
// different algorithm version, if this cycle pulled results from that day or
// earlier (new data changes the window "as of" every later day), or if it's
// today. Past days otherwise never change, so in steady state each cycle
// only recomputes today - cheap even on large datasets.
func (a *Analyzer) refreshDaily(ctx context.Context, tx pgx.Tx, now, dirtyFrom time.Time) (int, error) {
	today := truncateToDay(now)
	first := today.AddDate(0, 0, -29)

	rows, err := tx.Query(ctx, `SELECT day, algorithm_version FROM daily_flaky_counts WHERE day >= $1`, first)
	if err != nil {
		return 0, err
	}
	existing := map[string]string{} // "YYYY-MM-DD" -> algorithm version
	for rows.Next() {
		var day time.Time
		var version string
		if err := rows.Scan(&day, &version); err != nil {
			rows.Close()
			return 0, err
		}
		existing[day.Format(time.DateOnly)] = version
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	refreshed := 0
	for day := first; !day.After(today); day = day.AddDate(0, 0, 1) {
		version, ok := existing[day.Format(time.DateOnly)]
		touched := !dirtyFrom.IsZero() && !day.Before(truncateToDay(dirtyFrom))
		if ok && version == a.version && !touched && !day.Equal(today) {
			continue
		}

		// Classify every test as it stood at the end of that day (or now, for today).
		asOf := day.AddDate(0, 0, 1)
		if asOf.After(now) {
			asOf = now
		}
		windows, err := loadWindows(ctx, tx, allTests, asOf, a.params.WindowSize)
		if err != nil {
			return refreshed, err
		}
		flaky, broken := 0, 0
		for _, obs := range windows {
			switch a.algorithm(obs, a.params, asOf).Classification {
			case scoring.Flaky:
				flaky++
			case scoring.Broken:
				broken++
			}
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO daily_flaky_counts (day, flaky_count, broken_count, algorithm_version, computed_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (day) DO UPDATE SET flaky_count = EXCLUDED.flaky_count, broken_count = EXCLUDED.broken_count,
				algorithm_version = EXCLUDED.algorithm_version, computed_at = now()`,
			day, flaky, broken, a.version,
		); err != nil {
			return refreshed, err
		}
		refreshed++
	}
	return refreshed, nil
}

// allTests, passed to loadWindows, means "every test". It must be an empty
// slice, not nil: pgx sends nil as SQL NULL, and the query's
// `cardinality($1) = 0` check only matches an empty array.
var allTests = []int64{}

// loadWindows returns, for each requested test, its most recent `size`
// non-skipped observations with started_at <= asOf, oldest first.
//
// CROSS JOIN LATERAL runs the inner query once per test, which lets each test
// have its own LIMIT; the (test_case_id, started_at DESC) index makes every
// one of those a short index scan. Tests with no observations are omitted.
func loadWindows(ctx context.Context, tx pgx.Tx, ids []int64, asOf time.Time, size int) (map[int64][]scoring.Observation, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.test_case_id, o.started_at, o.commit_sha, o.final_status = 'failed', o.passed_on_retry
		FROM tests t
		CROSS JOIN LATERAL (
			SELECT result_id, started_at, commit_sha, final_status, passed_on_retry
			FROM observations
			WHERE test_case_id = t.test_case_id AND final_status <> 'skipped' AND started_at <= $2
			ORDER BY started_at DESC, result_id DESC
			LIMIT $3
		) o
		WHERE cardinality($1::bigint[]) = 0 OR t.test_case_id = ANY($1)
		ORDER BY t.test_case_id, o.started_at, o.result_id`,
		ids, asOf, size,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	windows := make(map[int64][]scoring.Observation)
	for rows.Next() {
		var id int64
		var o scoring.Observation
		if err := rows.Scan(&id, &o.StartedAt, &o.CommitSHA, &o.Failed, &o.PassedOnRetry); err != nil {
			return nil, err
		}
		windows[id] = append(windows[id], o)
	}
	return windows, rows.Err()
}

// truncateToDay returns midnight UTC at the start of t's day.
func truncateToDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
