// These types mirror the JSON the api service returns (see internal/model/model.go
// and internal/api/handlers.go in the Go code). Field names are snake_case
// because that's what the Go json tags produce.

export type Classification = 'stable' | 'flaky' | 'broken' | 'insufficient_data'
export type ResultStatus = 'passed' | 'failed' | 'skipped'

export interface TestStats {
  test_case_id: number
  repository: string
  suite: string
  name: string
  classification: Classification
  flakiness_score: number
  flip_rate: number
  retry_pass_rate: number
  commit_conflict_rate: number
  window_size: number
  sample_size: number
  pass_count: number
  fail_count: number
  algorithm_version: string
  last_result_at: string | null
  computed_at: string
}

export interface Quarantine {
  id: number
  test_case_id: number
  repository: string
  suite: string
  name: string
  reason: string
  created_by: string
  created_at: string
  released_at: string | null
}

export interface LeaderboardRow extends TestStats {
  quarantined: boolean
}

export interface Leaderboard {
  tests: LeaderboardRow[]
  total: number
  limit: number
  offset: number
}

export interface TestDetail extends TestStats {
  quarantine: Quarantine | null
}

export interface HistoryEntry {
  result_id: number
  run_id: number
  ci_run_id: string
  ci_run_attempt: number
  branch: string
  commit_sha: string
  run_url: string
  started_at: string
  final_status: ResultStatus
  attempts: number
  passed_on_retry: boolean
  duration_ms: number
  error_message: string
}

export interface TestHistory {
  test: { test_case_id: number; repository: string; suite: string; name: string; first_seen_at: string }
  results: HistoryEntry[]
}

export interface Run {
  run_id: number
  repository: string
  ci_run_id: string
  ci_run_attempt: number
  branch: string
  commit_sha: string
  run_url: string
  started_at: string
  ingested_at: string
  total: number
  passed: number
  failed: number
  skipped: number
  retried: number
}

export interface RunList {
  runs: Run[]
  total: number
  limit: number
  offset: number
}

export interface RunResult {
  test_case_id: number
  suite: string
  name: string
  final_status: ResultStatus
  attempts: number
  passed_on_retry: boolean
  duration_ms: number
  error_message: string
}

export interface RunDetail extends Run {
  results: RunResult[]
}

export interface Repo {
  name: string
  tests: number
  runs: number
  branches: string[]
  last_run_at: string | null
}

export interface DailyCount {
  day: string
  flaky: number
  broken: number
}

export interface AnalyzerSummary {
  tests_tracked: number
  classifications: Record<string, number>
  algorithm_version: string
  commit: string
  scored_by: Record<string, number>
  last_cycle_at: string | null
  daily: DailyCount[]
}

export interface IngestSummary {
  repositories: number
  tests: number
  runs_ingested: number
  results_ingested: number
}

// Each part of the overview comes from a different service, so each can be
// null on its own when that service is down; `degraded` names what's missing.
export interface Summary {
  analyzer: AnalyzerSummary | null
  ingest: IngestSummary | null
  quarantined: number | null
  degraded: string[]
}

export interface BuildInfo {
  api: { version: string; commit: string }
  analyzer: { commit: string; algorithm_version: string } | null
}
