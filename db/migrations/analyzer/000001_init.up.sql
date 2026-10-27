-- analyzer-db schema (owned by the analyzer service). Its own copy of the data
-- scoring needs (pulled from ingest's feed) plus the stats it computes.
-- test_case_id / result_id values are ingest's IDs: logical references only,
-- deliberately not foreign keys, because they live in another service's database.

-- Local catalogue of tests seen in the feed (copied so the leaderboard can
-- filter by repo and search by name without calling ingest).
CREATE TABLE tests (
  test_case_id  BIGINT PRIMARY KEY,             -- ingest's test_cases.id
  repository    TEXT NOT NULL,                  -- e.g. "shivam/flaky-demo"
  suite         TEXT NOT NULL,
  name          TEXT NOT NULL,
  needs_rescore BOOLEAN NOT NULL DEFAULT true   -- set when new observations arrive; cleared once re-scored
);
CREATE INDEX idx_tests_repository ON tests (repository);

-- One row per result pulled from ingest's feed: just the fields scoring needs.
CREATE TABLE observations (
  result_id       BIGINT PRIMARY KEY,           -- ingest's test_results.id (makes re-pulling idempotent)
  test_case_id    BIGINT NOT NULL REFERENCES tests(test_case_id) ON DELETE CASCADE,
  started_at      TIMESTAMPTZ NOT NULL,         -- run start time; orders the scoring window
  commit_sha      TEXT NOT NULL,                -- needed for commit_conflict_rate
  final_status    TEXT NOT NULL CHECK (final_status IN ('passed','failed','skipped')),
  passed_on_retry BOOLEAN NOT NULL
);
CREATE INDEX idx_observations_window ON observations (test_case_id, started_at DESC);

-- Precomputed stats, written only by the active analyzer replica.
CREATE TABLE test_stats (
  test_case_id        BIGINT PRIMARY KEY REFERENCES tests(test_case_id) ON DELETE CASCADE,
  window_size         INT NOT NULL,
  sample_size         INT NOT NULL,
  pass_count          INT NOT NULL,
  fail_count          INT NOT NULL,
  flip_rate           DOUBLE PRECISION NOT NULL,
  retry_pass_rate     DOUBLE PRECISION NOT NULL,
  commit_conflict_rate DOUBLE PRECISION NOT NULL,
  flakiness_score     DOUBLE PRECISION NOT NULL,   -- 0.0 - 1.0
  classification      TEXT NOT NULL CHECK (classification IN ('stable','flaky','broken','insufficient_data')),
  algorithm_version   TEXT NOT NULL,               -- e.g. "v1", "v2" (visible in rollout demo)
  last_result_at      TIMESTAMPTZ,
  computed_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_stats_score ON test_stats (classification, flakiness_score DESC);

-- One row per day for the "flaky tests over time" chart: how many tests were
-- flaky/broken as of the end of that day (UTC), by replaying the classifier.
CREATE TABLE daily_flaky_counts (
  day               DATE PRIMARY KEY,
  flaky_count       INT NOT NULL,
  broken_count      INT NOT NULL,
  algorithm_version TEXT NOT NULL,              -- recomputed when the algorithm changes
  computed_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Feed cursor: the highest ingest test_results.id already pulled (always exactly one row).
CREATE TABLE sync_state (
  id              INT PRIMARY KEY CHECK (id = 1),
  last_result_id  BIGINT NOT NULL DEFAULT 0,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO sync_state (id) VALUES (1);
