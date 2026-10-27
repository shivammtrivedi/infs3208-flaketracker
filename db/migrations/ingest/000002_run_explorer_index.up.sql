-- The run explorer lists runs newest first. Without this index every page
-- would sort the whole test_runs table, which grows with every CI run (and
-- quickly during a load test).
CREATE INDEX idx_runs_started ON test_runs (started_at DESC, id DESC);
