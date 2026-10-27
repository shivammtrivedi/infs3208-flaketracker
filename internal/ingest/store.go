package ingest

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InsertResult is what the HTTP handler needs to know after trying to store
// a run: whether it was newly written, and if not, why not.
type InsertResult struct {
	RunID     int64
	Duplicate bool
	Results   int
}

// Insert stores one CI run and all its test results inside a single database
// transaction (Section 7.3, step 1). Wrapping everything in one transaction
// means either the whole run is saved, or none of it is - a partially
// ingested run (say, half its test results missing) would silently corrupt
// the flakiness statistics the analyzer computes later.
func Insert(ctx context.Context, pool *pgxpool.Pool, p *RunPayload) (InsertResult, error) {
	// Begin a transaction. `tx` can be used for queries just like `pool`
	// below, but none of its writes are permanent until Commit is called.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return InsertResult{}, err
	}
	// `defer tx.Rollback(ctx)` runs no matter how this function returns -
	// including via an early `return` on error. If we already reached
	// Commit successfully, calling Rollback afterwards on a finished
	// transaction is a documented no-op. This "defer a cleanup, commit
	// explicitly on the happy path" pattern is the standard way Go handles
	// "undo unless I explicitly finished" logic, since there's no automatic
	// scope-exit destructor like a Python `with` block or Java try-with-resources.
	defer tx.Rollback(ctx)

	// Upsert the repository. "ON CONFLICT ... DO UPDATE" (rather than
	// DO NOTHING) is a deliberate trick: DO NOTHING wouldn't let us RETURN
	// the id of the row that already existed, so we do a harmless no-op
	// update instead purely to get RETURNING to fire either way.
	var repositoryID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO repositories (full_name) VALUES ($1)
		 ON CONFLICT (full_name) DO UPDATE SET full_name = EXCLUDED.full_name
		 RETURNING id`,
		p.Repository,
	).Scan(&repositoryID)
	if err != nil {
		return InsertResult{}, err
	}

	// Insert the run. Here we deliberately use ON CONFLICT ... DO NOTHING:
	// (repository_id, ci_run_id, ci_run_attempt) is the idempotency key from
	// Section 9.1, so a conflict means "this exact run was already
	// ingested" and we want to detect that, not silently overwrite it.
	var runID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO test_runs (repository_id, ci_run_id, ci_run_attempt, branch, commit_sha, run_url, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (repository_id, ci_run_id, ci_run_attempt) DO NOTHING
		 RETURNING id`,
		repositoryID, p.CIRunID, p.CIRunAttempt, p.Branch, p.CommitSHA, nullableString(p.RunURL), p.StartedAt,
	).Scan(&runID)

	// pgx.ErrNoRows here doesn't mean something broke - QueryRow always
	// expects exactly one row back, and "ON CONFLICT DO NOTHING" returns
	// zero rows precisely when the conflict happened. `errors.Is` is Go's
	// way of checking "is this error (or one it wraps) equal to that
	// sentinel error", similar in spirit to `except SpecificError:` in
	// Python, but without exceptions: err is just a value we inspect.
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx,
			`SELECT id FROM test_runs WHERE repository_id = $1 AND ci_run_id = $2 AND ci_run_attempt = $3`,
			repositoryID, p.CIRunID, p.CIRunAttempt,
		).Scan(&runID)
		if err != nil {
			return InsertResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return InsertResult{}, err
		}
		return InsertResult{RunID: runID, Duplicate: true}, nil
	}
	if err != nil {
		return InsertResult{}, err
	}

	// This is a brand-new run, so insert every test result. `for _, result
	// := range p.Results` is Go's for-each loop: range gives back
	// (index, value) pairs, and `_` throws away the index we don't need here.
	for _, result := range p.Results {
		var testCaseID int64
		err = tx.QueryRow(ctx,
			`INSERT INTO test_cases (repository_id, suite, name) VALUES ($1, $2, $3)
			 ON CONFLICT (repository_id, suite, name) DO UPDATE SET name = EXCLUDED.name
			 RETURNING id`,
			repositoryID, result.Suite, result.Name,
		).Scan(&testCaseID)
		if err != nil {
			return InsertResult{}, err
		}

		d := DeriveResult(result.Attempts)

		_, err = tx.Exec(ctx,
			`INSERT INTO test_results (run_id, test_case_id, final_status, attempts, passed_on_retry, duration_ms, error_message)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			runID, testCaseID, string(d.FinalStatus), d.Attempts, d.PassedOnRetry, d.DurationMs, nullableString(d.ErrorMessage),
		)
		if err != nil {
			return InsertResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return InsertResult{}, err
	}

	return InsertResult{RunID: runID, Results: len(p.Results)}, nil
}

// nullableString turns an empty Go string into a real SQL NULL instead of
// storing an empty string in a nullable text column. It returns `any`
// (Go's alias for `interface{}`, the type that can hold a value of any
// type) because that's what pgx's query parameters accept, and `nil` is
// how you spell "NULL" when passing a Go value into a SQL query.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
