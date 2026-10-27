-- api-db schema (owned by the api service). Quarantine decisions only.

-- Quarantine records (history kept; one active per test). test_case_id is
-- ingest's test_cases.id: a logical reference, not a foreign key, since it
-- lives in another service's database. Test identity is copied in at
-- quarantine time so CI can fetch the list by repo from api alone.
CREATE TABLE quarantines (
  id            BIGSERIAL PRIMARY KEY,
  test_case_id  BIGINT NOT NULL,
  repository    TEXT NOT NULL,
  suite         TEXT NOT NULL,
  name          TEXT NOT NULL,
  reason        TEXT NOT NULL,
  created_by    TEXT NOT NULL DEFAULT 'dashboard',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  released_at   TIMESTAMPTZ                      -- NULL = still active
);
CREATE UNIQUE INDEX uq_active_quarantine ON quarantines (test_case_id) WHERE released_at IS NULL;
CREATE INDEX idx_quarantines_repository ON quarantines (repository) WHERE released_at IS NULL;
