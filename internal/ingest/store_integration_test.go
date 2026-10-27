//go:build integration

// The line above is a Go "build tag": this file is excluded from a normal
// `go build` or `go test ./...` and only compiles when explicitly requested
// with `go test -tags=integration ./...`. That keeps CI's default test run
// fast and database-free, while still letting this test exist for anyone
// (or any pipeline) that has a real Postgres to point at.
package ingest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestInsertIdempotency is the Phase 2 acceptance check from the project
// plan, expressed as a test: posting the same run twice must insert it once
// and report the second attempt as a duplicate, with matching run IDs.
func TestInsertIdempotency(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	payload := &RunPayload{
		Repository: "test/repo",
		CIRunID:    "integration-test-1",
		Branch:     "main",
		CommitSHA:  "deadbeef",
		StartedAt:  time.Now(),
		Results: []ResultPayload{
			{Suite: "a.spec.ts", Name: "works", Attempts: []AttemptPayload{{Status: StatusPassed}}},
		},
	}
	payload.Validate()

	first, err := Insert(context.Background(), pool, payload)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if first.Duplicate {
		t.Fatalf("first insert reported as duplicate, want new")
	}
	if first.Results != 1 {
		t.Fatalf("first insert reported %d results, want 1", first.Results)
	}

	second, err := Insert(context.Background(), pool, payload)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("second insert not reported as duplicate, want duplicate")
	}
	if second.RunID != first.RunID {
		t.Fatalf("duplicate run_id = %d, want %d (same run as the first insert)", second.RunID, first.RunID)
	}

	var count int
	err = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM test_results tr JOIN test_runs r ON r.id = tr.run_id WHERE r.id = $1`,
		first.RunID,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count results: %v", err)
	}
	if count != 1 {
		t.Fatalf("test_results row count = %d, want 1 (rows must exist once, not twice)", count)
	}
}
