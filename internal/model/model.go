// Package model holds the JSON types that cross service boundaries over HTTP
// - the "contract" between FlakeTracker's microservices. The service that
// serves an endpoint and the service that calls it both use the same struct,
// so the JSON field names can't silently drift apart.
//
// Sharing these *shapes* is fine under database-per-service: services still
// never share a database, and each one only ever sees another's data through
// these API responses.
package model

import "time"

// FeedRow is one test result as published by ingest's results feed
// (GET /internal/v1/results) and consumed by the analyzer. It carries just
// what scoring needs, plus the test's identity so the analyzer can build its
// own local catalogue of tests.
type FeedRow struct {
	ResultID      int64     `json:"result_id"`
	TestCaseID    int64     `json:"test_case_id"`
	Repository    string    `json:"repository"`
	Suite         string    `json:"suite"`
	Name          string    `json:"name"`
	StartedAt     time.Time `json:"started_at"`
	CommitSHA     string    `json:"commit_sha"`
	FinalStatus   string    `json:"final_status"`
	PassedOnRetry bool      `json:"passed_on_retry"`
}

// FeedPage is one page of the results feed. NextAfterID is the cursor to send
// as after_id on the next request (unchanged if the page is empty).
type FeedPage struct {
	Results     []FeedRow `json:"results"`
	NextAfterID int64     `json:"next_after_id"`
}

// TestStats is one test's identity plus its latest computed stats, as served
// by the analyzer's internal API.
type TestStats struct {
	TestCaseID         int64   `json:"test_case_id"`
	Repository         string  `json:"repository"`
	Suite              string  `json:"suite"`
	Name               string  `json:"name"`
	Classification     string  `json:"classification"`
	FlakinessScore     float64 `json:"flakiness_score"`
	FlipRate           float64 `json:"flip_rate"`
	RetryPassRate      float64 `json:"retry_pass_rate"`
	CommitConflictRate float64 `json:"commit_conflict_rate"`
	WindowSize         int     `json:"window_size"`
	SampleSize         int     `json:"sample_size"`
	PassCount          int     `json:"pass_count"`
	FailCount          int     `json:"fail_count"`
	AlgorithmVersion   string  `json:"algorithm_version"`
	// A pointer, so a test with no results yet encodes as JSON null rather
	// than Go's zero time ("0001-01-01T00:00:00Z").
	LastResultAt *time.Time `json:"last_result_at"`
	ComputedAt   time.Time  `json:"computed_at"`
}

// TestList is one page of the leaderboard.
type TestList struct {
	Tests  []TestStats `json:"tests"`
	Total  int         `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

// DailyCount is one point on the "flaky tests over time" chart.
type DailyCount struct {
	Day    string `json:"day"` // YYYY-MM-DD (UTC)
	Flaky  int    `json:"flaky"`
	Broken int    `json:"broken"`
}

// AnalyzerSummary is the analyzer's contribution to the overview dashboard.
type AnalyzerSummary struct {
	TestsTracked    int            `json:"tests_tracked"`
	Classifications map[string]int `json:"classifications"` // classification -> number of tests
	// AlgorithmVersion and Commit describe the build of the replica that answered.
	AlgorithmVersion string `json:"algorithm_version"`
	Commit           string `json:"commit"`
	// ScoredBy counts tests by the algorithm their stored stats were computed
	// with. During a rollout it shows re-scoring progress, e.g. {"v1": 120, "v2": 330}.
	ScoredBy    map[string]int `json:"scored_by"`
	LastCycleAt *time.Time     `json:"last_cycle_at"`
	Daily       []DailyCount   `json:"daily"`
}

// ---- ingest's internal read API (served by ingest, called by api) ----

// Repo is one repository with a little context for filter dropdowns.
type Repo struct {
	Name      string     `json:"name"`
	Tests     int        `json:"tests"`
	Runs      int        `json:"runs"`
	Branches  []string   `json:"branches"`
	LastRunAt *time.Time `json:"last_run_at"`
}

// RepoList is the response of GET /internal/v1/repos.
type RepoList struct {
	Repos []Repo `json:"repos"`
}

// TestIdentity is what ingest knows about a test case: who it is, not how it scores.
type TestIdentity struct {
	TestCaseID  int64     `json:"test_case_id"`
	Repository  string    `json:"repository"`
	Suite       string    `json:"suite"`
	Name        string    `json:"name"`
	FirstSeenAt time.Time `json:"first_seen_at"`
}

// HistoryEntry is one result of one test, with the run it came from.
type HistoryEntry struct {
	ResultID      int64     `json:"result_id"`
	RunID         int64     `json:"run_id"`
	CIRunID       string    `json:"ci_run_id"`
	CIRunAttempt  int       `json:"ci_run_attempt"`
	Branch        string    `json:"branch"`
	CommitSHA     string    `json:"commit_sha"`
	RunURL        string    `json:"run_url"`
	StartedAt     time.Time `json:"started_at"`
	FinalStatus   string    `json:"final_status"`
	Attempts      int       `json:"attempts"`
	PassedOnRetry bool      `json:"passed_on_retry"`
	DurationMs    int       `json:"duration_ms"`
	ErrorMessage  string    `json:"error_message"`
}

// TestHistory is a test's identity plus its most recent results, newest first.
type TestHistory struct {
	Test    TestIdentity   `json:"test"`
	Results []HistoryEntry `json:"results"`
}

// RunCounts tallies a run's results by outcome.
type RunCounts struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	Retried int `json:"retried"` // passed, but only after a retry
}

// Run is one CI run with its result counts.
//
// RunCounts is *embedded* (a field with only a type, no name). Its fields are
// promoted onto Run, and encoding/json flattens them too, so the JSON is
// {"run_id": 1, ..., "total": 150, "passed": 148, ...} rather than nesting
// the counts under a separate key.
type Run struct {
	RunID        int64     `json:"run_id"`
	Repository   string    `json:"repository"`
	CIRunID      string    `json:"ci_run_id"`
	CIRunAttempt int       `json:"ci_run_attempt"`
	Branch       string    `json:"branch"`
	CommitSHA    string    `json:"commit_sha"`
	RunURL       string    `json:"run_url"`
	StartedAt    time.Time `json:"started_at"`
	IngestedAt   time.Time `json:"ingested_at"`
	RunCounts
}

// RunList is one page of the run explorer.
type RunList struct {
	Runs   []Run `json:"runs"`
	Total  int   `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

// RunResult is one test's outcome within a run.
type RunResult struct {
	TestCaseID    int64  `json:"test_case_id"`
	Suite         string `json:"suite"`
	Name          string `json:"name"`
	FinalStatus   string `json:"final_status"`
	Attempts      int    `json:"attempts"`
	PassedOnRetry bool   `json:"passed_on_retry"`
	DurationMs    int    `json:"duration_ms"`
	ErrorMessage  string `json:"error_message"`
}

// RunDetail is one run plus every result in it (failures first).
type RunDetail struct {
	Run
	Results []RunResult `json:"results"`
}

// IngestSummary is ingest's contribution to the overview dashboard.
type IngestSummary struct {
	Repositories    int `json:"repositories"`
	Tests           int `json:"tests"`
	RunsIngested    int `json:"runs_ingested"`
	ResultsIngested int `json:"results_ingested"`
}

// ---- api's own data (api-db) ----

// Quarantine is one quarantine record. Repository/suite/name are copied from
// ingest when the quarantine is created, so api can answer "what's
// quarantined in this repo?" from its own database alone.
type Quarantine struct {
	ID         int64      `json:"id"`
	TestCaseID int64      `json:"test_case_id"`
	Repository string     `json:"repository"`
	Suite      string     `json:"suite"`
	Name       string     `json:"name"`
	Reason     string     `json:"reason"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ReleasedAt *time.Time `json:"released_at"` // null while the quarantine is active
}
