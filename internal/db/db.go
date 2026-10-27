// Package db provides the pgx connection pool each service uses to talk to
// its OWN database. The package is shared code, but every running service
// gets its own pool pointed at its own Postgres instance (via DB_* env vars),
// so no data is shared between services through it.
package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"flaketracker/internal/config"
)

// NewPool creates a pgx connection pool using the DB_* settings from the
// environment.
//
// A pool keeps a set of open database connections and hands one out per
// query, which matters because a Go HTTP server handles many requests at the
// same time (each on its own goroutine) - the same idea as SQLAlchemy's
// engine pool in Python or HikariCP in Spring. pgxpool.New doesn't connect
// eagerly; the first query (or a Ping in /readyz) opens the first connection.
func NewPool(ctx context.Context) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, config.LoadDB().DSN())
}
