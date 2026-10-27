// Package analyzer is the analyzer service's core: it pulls new test results
// from ingest's feed into analyzer-db, re-scores the affected tests, keeps
// the daily flaky-count history up to date (cycle.go), and serves the
// resulting stats over an internal HTTP API (api.go).
//
// Several analyzer replicas run for availability, but only ONE - the leader -
// computes. Leadership is a Postgres advisory lock held on a dedicated
// connection (see lead below). The other replicas stand by, keep serving
// reads, and take over if the leader goes away.
package analyzer

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"flaketracker/internal/httpx"
	"flaketracker/internal/model"
	"flaketracker/internal/scoring"
)

// leaderLockKey identifies the advisory lock all replicas compete for. Any
// fixed 64-bit number works; this one is the ASCII bytes of "Flake".
const leaderLockKey int64 = 0x466c616b65

// FeedSource is anything that can return a page of ingest's results feed.
//
// It's an interface rather than the concrete *clients.Ingest so the analyzer
// only depends on the one method it actually uses. Go interfaces are
// satisfied implicitly - *clients.Ingest never mentions FeedSource, it just
// happens to have a matching Results method - which also makes it trivial to
// substitute a fake feed in tests.
type FeedSource interface {
	Results(ctx context.Context, afterID int64, limit int) (model.FeedPage, error)
}

// Settings are the cycle's operational knobs (from config.Analyzer).
type Settings struct {
	Interval     time.Duration
	FeedPageSize int
	FeedMaxPages int
	RescoreBatch int
}

// Analyzer runs the leader-only compute loop.
type Analyzer struct {
	pool      *pgxpool.Pool
	feed      FeedSource
	version   string            // algorithm version baked into this image ("v1"/"v2")
	algorithm scoring.Algorithm // the function for that version
	params    scoring.Params
	settings  Settings

	// leaderConn is non-nil while this replica is the leader: it's the
	// connection whose session holds the advisory lock. Only the Run
	// goroutine touches it, so it needs no mutex.
	leaderConn *pgxpool.Conn
	wasLeader  bool // previous tick's leadership, so changes are logged once...
	ticked     bool // ...and the very first tick always logs which role we have
}

// New builds an Analyzer, failing if version isn't a known algorithm.
func New(pool *pgxpool.Pool, feed FeedSource, version string, params scoring.Params, settings Settings) (*Analyzer, error) {
	algorithm, err := scoring.ForVersion(version)
	if err != nil {
		return nil, err
	}
	return &Analyzer{
		pool:      pool,
		feed:      feed,
		version:   version,
		algorithm: algorithm,
		params:    params,
		settings:  settings,
	}, nil
}

// Run ticks every Settings.Interval until ctx is cancelled. On each tick the
// replica tries to be (or stay) the leader, and if it is, runs one cycle.
// When Run returns it gives up leadership, so another replica can take over
// immediately - that's what makes a rolling update hand over cleanly.
func (a *Analyzer) Run(ctx context.Context) {
	defer a.resign()

	// A Ticker delivers the current time on its channel C at a fixed
	// interval. Stop() releases its resources when we're done.
	ticker := time.NewTicker(a.settings.Interval)
	defer ticker.Stop()

	for {
		a.tick(ctx)
		// Wait for whichever comes first: the next tick, or shutdown.
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *Analyzer) tick(ctx context.Context) {
	leader := a.lead(ctx)
	if !a.ticked || leader != a.wasLeader {
		if leader {
			slog.Info("analyzer: became leader; this replica now runs the compute cycle", "algorithm", a.version)
		} else {
			slog.Info("analyzer: standing by; another replica is the leader")
		}
		a.wasLeader, a.ticked = leader, true
	}
	if !leader {
		return
	}

	// Give the cycle its own ID. It's forwarded on the feed requests, so
	// ingest's log lines for this cycle's pulls carry the same "cycle-..." ID.
	cycleCtx := httpx.ContextWithRequestID(ctx, "cycle-"+httpx.NewRequestID())

	stats, err := a.Cycle(cycleCtx)
	if err != nil {
		if ctx.Err() == nil { // don't report errors caused by our own shutdown
			slog.ErrorContext(cycleCtx, "analyzer: cycle failed", "error", err)
		}
		return
	}
	slog.InfoContext(cycleCtx, "analyzer: cycle complete",
		"pulled", stats.Pulled,
		"rescored", stats.Rescored,
		"days_refreshed", stats.DaysRefreshed,
		"feed_error", stats.FeedError,
		"duration_ms", stats.Duration.Milliseconds(),
	)
}

// lead reports whether this replica is the leader, trying to become it if not.
//
// pg_try_advisory_lock takes a lock that belongs to the database *session*
// (connection) that took it, and returns immediately with true/false rather
// than waiting. So whichever replica's connection got the lock first is the
// leader, for as long as that connection stays open. If the leader pod dies,
// its connection drops, Postgres releases the lock automatically, and another
// replica's next attempt succeeds - no stuck locks, no separate coordinator.
func (a *Analyzer) lead(ctx context.Context) bool {
	if a.leaderConn != nil {
		if err := a.leaderConn.Ping(ctx); err == nil {
			return true
		}
		// The connection is broken, so its session - and the lock - may be
		// gone. Close it outright (rather than returning it to the pool,
		// where a half-alive session could keep holding the lock) and compete
		// again from scratch below.
		slog.Warn("analyzer: lost the leader connection")
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = a.leaderConn.Conn().Close(closeCtx)
		cancel()
		a.leaderConn.Release()
		a.leaderConn = nil
	}

	conn, err := a.pool.Acquire(ctx)
	if err != nil {
		return false
	}
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, leaderLockKey).Scan(&got); err != nil || !got {
		conn.Release() // didn't get the lock: hand the connection back unchanged
		return false
	}
	// Keep this connection checked out for as long as we're leader.
	a.leaderConn = conn
	return true
}

// resign releases the leader lock (if held) so a standby can take over at
// once, instead of waiting for this connection to time out.
func (a *Analyzer) resign() {
	if a.leaderConn == nil {
		return
	}
	// The run context is already cancelled at this point, so use a fresh
	// short-lived one for the unlock.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = a.leaderConn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, leaderLockKey)
	a.leaderConn.Release()
	a.leaderConn = nil
	slog.Info("analyzer: resigned leadership")
}
