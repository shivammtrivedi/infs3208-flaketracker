-- ingest-db schema (owned by the ingest service). Raw CI results; the system of record.

-- A repository whose CI sends results.
CREATE TABLE repositories (
  id           BIGSERIAL PRIMARY KEY,
  full_name    TEXT NOT NULL UNIQUE,            -- e.g. "shivam/flaky-demo"
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A logical test, identified by repo + file/suite + title.
CREATE TABLE test_cases (
  id            BIGSERIAL PRIMARY KEY,
  repository_id BIGINT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  suite         TEXT NOT NULL,                  -- e.g. "tests/checkout.spec.ts"
  name          TEXT NOT NULL,                  -- e.g. "adds item to cart"
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (repository_id, suite, name)
);

-- One CI pipeline execution.
CREATE TABLE test_runs (
  id             BIGSERIAL PRIMARY KEY,
  repository_id  BIGINT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  ci_run_id      TEXT NOT NULL,                 -- e.g. GITHUB_RUN_ID
  ci_run_attempt INT  NOT NULL DEFAULT 1,       -- e.g. GITHUB_RUN_ATTEMPT
  branch         TEXT NOT NULL,
  commit_sha     TEXT NOT NULL,
  run_url        TEXT,
  started_at     TIMESTAMPTZ NOT NULL,
  ingested_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (repository_id, ci_run_id, ci_run_attempt)   -- idempotency key
);

-- Final outcome of one test in one run (one row per test per run).
CREATE TABLE test_results (
  id             BIGSERIAL PRIMARY KEY,
  run_id         BIGINT NOT NULL REFERENCES test_runs(id) ON DELETE CASCADE,
  test_case_id   BIGINT NOT NULL REFERENCES test_cases(id) ON DELETE CASCADE,
  final_status   TEXT NOT NULL CHECK (final_status IN ('passed','failed','skipped')),
  attempts       INT  NOT NULL DEFAULT 1,       -- total attempts incl. retries
  passed_on_retry BOOLEAN NOT NULL DEFAULT false, -- failed first, passed later in same run
  duration_ms    INT  NOT NULL,                 -- duration of final attempt
  error_message  TEXT,                          -- first failure message, truncated to 2 KB
  UNIQUE (run_id, test_case_id)
);
CREATE INDEX idx_results_test_case ON test_results (test_case_id, run_id DESC);
