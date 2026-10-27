package seed

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"flaketracker/internal/ingest"
)

// LoadDB generates data and bulk-loads it straight into ingest-db.
//
// This is the fast path for an initial load (~135k results in seconds). It
// only ever writes to ingest-db: the seeder is an ingest-owned tool, and
// under database-per-service nothing writes to another service's database.
// The analyzer picks this data up through ingest's feed exactly as it would
// live data.
//
// Everything happens in ONE transaction, so a failed load (for example,
// re-running the same --seed without --truncate) leaves the database exactly
// as it was - never half-loaded.
func LoadDB(ctx context.Context, pool *pgxpool.Pool, cfg Config, truncate bool) (Summary, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Summary{}, err
	}
	defer tx.Rollback(ctx) // no-op once Commit succeeds (see internal/ingest/store.go)

	if truncate {
		// No RESTART IDENTITY on purpose: IDs keep counting up from where
		// they were. The analyzer pulls ingest's feed by "results with an ID
		// greater than the last one I saw", so resetting IDs to 1 would make
		// it silently skip the new data.
		if _, err := tx.Exec(ctx, `TRUNCATE test_results, test_runs, test_cases, repositories`); err != nil {
			return Summary{}, fmt.Errorf("truncate: %w", err)
		}
	}

	// Repositories and test cases are few (3 and ~450), so they're inserted
	// one by one and their generated IDs remembered in these maps. A struct
	// works as a map key in Go as long as all its fields are comparable.
	type caseKey struct {
		repoID      int64
		suite, name string
	}
	repoIDs := map[string]int64{}
	caseIDs := map[caseKey]int64{}

	// Result rows are many (~135k), so they're collected here and sent in one
	// COPY at the end. [][]any is "a slice of rows, each row a slice of
	// values of any type" - the shape pgx.CopyFromRows expects.
	var rows [][]any

	// The function literal passed to Generate is a closure: it can read and
	// modify ctx, tx, repoIDs, caseIDs and rows from the surrounding function,
	// even though Generate is the one calling it.
	sum, err := Generate(cfg, func(p *ingest.RunPayload) error {
		repoID, ok := repoIDs[p.Repository]
		if !ok {
			err := tx.QueryRow(ctx,
				`INSERT INTO repositories (full_name) VALUES ($1)
				 ON CONFLICT (full_name) DO UPDATE SET full_name = EXCLUDED.full_name
				 RETURNING id`,
				p.Repository,
			).Scan(&repoID)
			if err != nil {
				return fmt.Errorf("upsert repository: %w", err)
			}
			repoIDs[p.Repository] = repoID
		}

		// Plain INSERT (no ON CONFLICT): if this run already exists, we want
		// the unique-key error so the whole load aborts and rolls back.
		var runID int64
		err := tx.QueryRow(ctx,
			`INSERT INTO test_runs (repository_id, ci_run_id, ci_run_attempt, branch, commit_sha, run_url, started_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 RETURNING id`,
			repoID, p.CIRunID, p.CIRunAttempt, p.Branch, p.CommitSHA, p.RunURL, p.StartedAt,
		).Scan(&runID)
		if err != nil {
			return fmt.Errorf("insert run %s/%d: %w", p.CIRunID, p.CIRunAttempt, err)
		}

		for _, r := range p.Results {
			key := caseKey{repoID, r.Suite, r.Name}
			caseID, ok := caseIDs[key]
			if !ok {
				err := tx.QueryRow(ctx,
					`INSERT INTO test_cases (repository_id, suite, name) VALUES ($1, $2, $3)
					 ON CONFLICT (repository_id, suite, name) DO UPDATE SET name = EXCLUDED.name
					 RETURNING id`,
					repoID, r.Suite, r.Name,
				).Scan(&caseID)
				if err != nil {
					return fmt.Errorf("upsert test case: %w", err)
				}
				caseIDs[key] = caseID
			}

			// Same derivation as the HTTP path, so both modes store identical rows.
			d := ingest.DeriveResult(r.Attempts)

			// A nil `any` becomes SQL NULL; an empty string would be stored as ''.
			var errMsg any
			if d.ErrorMessage != "" {
				errMsg = d.ErrorMessage
			}
			rows = append(rows, []any{runID, caseID, string(d.FinalStatus), d.Attempts, d.PassedOnRetry, d.DurationMs, errMsg})
		}
		return nil
	})
	if err != nil {
		return sum, explainLoadError(err, cfg.Seed)
	}

	// COPY streams all rows to Postgres in a single bulk operation - far
	// faster than 135k separate INSERT statements, and the main reason
	// --mode=db finishes in seconds.
	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"test_results"},
		[]string{"run_id", "test_case_id", "final_status", "attempts", "passed_on_retry", "duration_ms", "error_message"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return sum, fmt.Errorf("copy test_results: %w", err)
	}

	return sum, tx.Commit(ctx)
}

// explainLoadError turns "duplicate key" into an actionable message.
//
// errors.As walks the chain of wrapped errors (the %w in fmt.Errorf above
// wraps one error inside another) looking for one of the requested type, and
// if found, stores it in pgErr. It's the typed counterpart of errors.Is.
func explainLoadError(err error, seed uint64) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // 23505 = unique_violation
		return fmt.Errorf("ingest-db already contains runs from --seed=%d; re-run with --truncate to wipe ingest-db first, or use a different --seed (%w)", seed, err)
	}
	return err
}
