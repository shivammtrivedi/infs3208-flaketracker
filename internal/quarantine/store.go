// Package quarantine persists quarantine decisions in api-db, the api
// service's own private database. Quarantines are the one piece of data only
// the api service creates, so it's the one table it owns.
package quarantine

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"flaketracker/internal/model"
)

// Sentinel errors: package-level error values that callers compare against
// with errors.Is to decide which HTTP status to return (409 / 404).
var (
	ErrAlreadyActive = errors.New("test is already quarantined")
	ErrNotActive     = errors.New("test is not quarantined")
)

// Store reads and writes the quarantines table.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store backed by api-db.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const columns = `id, test_case_id, repository, suite, name, reason, created_by, created_at, released_at`

func scan(row pgx.Row) (model.Quarantine, error) {
	var q model.Quarantine
	err := row.Scan(&q.ID, &q.TestCaseID, &q.Repository, &q.Suite, &q.Name, &q.Reason, &q.CreatedBy, &q.CreatedAt, &q.ReleasedAt)
	return q, err
}

// List returns active quarantines, newest first, optionally for one repository.
func (s *Store) List(ctx context.Context, repo string) ([]model.Quarantine, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+columns+` FROM quarantines
		 WHERE released_at IS NULL AND ($1 = '' OR repository = $1)
		 ORDER BY created_at DESC, id DESC`, repo)
	if err != nil {
		return nil, err
	}
	// CollectRows calls the function literal once per row and gathers the
	// results into a slice; the literal just adapts scan to the signature
	// CollectRows expects.
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Quarantine, error) { return scan(row) })
	if list == nil {
		list = []model.Quarantine{} // encode as [] rather than null
	}
	return list, err
}

// Active returns the active quarantine for a test, or nil if there isn't one.
func (s *Store) Active(ctx context.Context, testCaseID int64) (*model.Quarantine, error) {
	q, err := scan(s.pool.QueryRow(ctx,
		`SELECT `+columns+` FROM quarantines WHERE test_case_id = $1 AND released_at IS NULL`, testCaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// ActiveFor reports which of the given tests are currently quarantined, in
// one query (used to flag rows on a leaderboard page).
func (s *Store) ActiveFor(ctx context.Context, testCaseIDs []int64) (map[int64]bool, error) {
	active := make(map[int64]bool)
	if len(testCaseIDs) == 0 {
		return active, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT test_case_id FROM quarantines WHERE released_at IS NULL AND test_case_id = ANY($1)`, testCaseIDs)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	for _, id := range ids {
		active[id] = true
	}
	return active, err
}

// CountActive returns how many tests are quarantined right now.
func (s *Store) CountActive(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM quarantines WHERE released_at IS NULL`).Scan(&n)
	return n, err
}

// Create inserts a new active quarantine and returns it with its ID and
// timestamps filled in. ErrAlreadyActive if the test is already quarantined.
//
// There's no "check first, then insert" here: two people quarantining the
// same test at the same moment could both pass a check. Instead the partial
// unique index uq_active_quarantine (one row per test WHERE released_at IS
// NULL) makes the database itself reject the second insert, which we
// translate into ErrAlreadyActive.
func (s *Store) Create(ctx context.Context, q model.Quarantine) (model.Quarantine, error) {
	created, err := scan(s.pool.QueryRow(ctx,
		`INSERT INTO quarantines (test_case_id, repository, suite, name, reason, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+columns,
		q.TestCaseID, q.Repository, q.Suite, q.Name, q.Reason, q.CreatedBy))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return model.Quarantine{}, ErrAlreadyActive
	}
	return created, err
}

// Release ends a test's active quarantine (the row is kept, with released_at
// set, as history). ErrNotActive if the test isn't quarantined.
func (s *Store) Release(ctx context.Context, testCaseID int64) (model.Quarantine, error) {
	q, err := scan(s.pool.QueryRow(ctx,
		`UPDATE quarantines SET released_at = now()
		 WHERE test_case_id = $1 AND released_at IS NULL
		 RETURNING `+columns, testCaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Quarantine{}, ErrNotActive
	}
	return q, err
}
