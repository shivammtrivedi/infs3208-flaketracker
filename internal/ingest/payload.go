// Package ingest validates and persists incoming CI test-run payloads -
// the canonical JSON format described in Section 9.1 of the project plan -
// into ingest-db, the ingest service's own private database. No other
// service reads these tables directly; they get this data through ingest's
// internal HTTP endpoints instead.
package ingest

import (
	"fmt"
	"time"
)

// Status is the outcome of a single test attempt. Go doesn't have a built-in
// enum type; the idiomatic substitute is a named string type plus a fixed
// set of constants, which is what this is. Because the underlying type is
// still `string`, encoding/json can decode straight into it with no extra
// code, and `switch` statements below can still do exhaustive-style checks.
type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// RunPayload is the JSON body posted to POST /ingest/v1/runs.
//
// The `json:"..."` text after each field is a "struct tag" - metadata
// attached to the field that the encoding/json package reads via reflection
// to decide how to map JSON keys to Go fields. This plays the same role as
// field aliases on a Pydantic BaseModel in FastAPI, or @JsonProperty in a
// Spring/Jackson DTO: it's what lets snake_case JSON map onto CamelCase Go.
type RunPayload struct {
	Repository   string          `json:"repository"`
	CIRunID      string          `json:"ci_run_id"`
	CIRunAttempt int             `json:"ci_run_attempt"`
	RunURL       string          `json:"run_url"`
	Branch       string          `json:"branch"`
	CommitSHA    string          `json:"commit_sha"`
	StartedAt    time.Time       `json:"started_at"`
	Results      []ResultPayload `json:"results"`
}

// ResultPayload is one test's outcome within a run: its identity, plus every
// attempt made at it (a test may be retried, producing more than one).
type ResultPayload struct {
	Suite    string           `json:"suite"`
	Name     string           `json:"name"`
	Attempts []AttemptPayload `json:"attempts"`
}

// AttemptPayload is a single execution of one test.
type AttemptPayload struct {
	Status     Status `json:"status"`
	DurationMs int    `json:"duration_ms"`
	Error      string `json:"error"`
}

// Validate checks the payload against the rules in Section 9.1 and returns a
// human-readable problem string for each thing that's wrong.
//
// Go has no exceptions - functions that can fail return an error value (or,
// here, a slice of them) instead of throwing. Returning every problem at
// once (rather than stopping at the first, the way a single `error` would)
// is what lets the HTTP handler satisfy the spec's "400 with a JSON error
// body listing problems" requirement in one round trip.
//
// The receiver `p *RunPayload` (a pointer) rather than `p RunPayload` (a
// copy) matters here: this method also fills in the default for
// CIRunAttempt below, and that mutation needs to be visible to the caller.
func (p *RunPayload) Validate() []string {
	var problems []string

	if p.Repository == "" {
		problems = append(problems, "repository is required")
	}
	if p.CIRunID == "" {
		problems = append(problems, "ci_run_id is required")
	}
	if p.Branch == "" {
		problems = append(problems, "branch is required")
	}
	if p.CommitSHA == "" {
		problems = append(problems, "commit_sha is required")
	}
	if p.StartedAt.IsZero() {
		problems = append(problems, "started_at is required")
	}
	if p.CIRunAttempt == 0 {
		// Section 9.1: ci_run_attempt defaults to 1 when the field is omitted
		// from the JSON body (Go zero-initialises a missing int field to 0).
		p.CIRunAttempt = 1
	}

	for i, result := range p.Results {
		if result.Suite == "" {
			problems = append(problems, fmt.Sprintf("results[%d].suite is required", i))
		}
		if result.Name == "" {
			problems = append(problems, fmt.Sprintf("results[%d].name is required", i))
		}
		if len(result.Attempts) == 0 {
			problems = append(problems, fmt.Sprintf("results[%d].attempts must have at least one entry", i))
		}
		for j, attempt := range result.Attempts {
			switch attempt.Status {
			case StatusPassed, StatusFailed, StatusSkipped:
				// valid, nothing to report
			default:
				problems = append(problems, fmt.Sprintf("results[%d].attempts[%d].status %q is invalid", i, j, attempt.Status))
			}
		}
	}

	return problems
}

// Derived is what actually gets stored for one test result, computed from
// its raw attempts per the rules in Section 9.1. Keeping it as its own type
// keeps the derivation logic testable in isolation from both JSON decoding
// and SQL.
//
// It's exported (capitalised) because the seed generator's --mode=db writes
// rows straight into ingest-db and must store exactly what the HTTP path
// would - so it reuses this function instead of re-implementing the rules.
type Derived struct {
	FinalStatus   Status
	Attempts      int
	PassedOnRetry bool
	DurationMs    int
	ErrorMessage  string
}

// DeriveResult applies Section 9.1's rules to one test's attempts:
//   - final_status is the status of the last attempt.
//   - passed_on_retry is true if the test failed at least once before that
//     final, successful attempt.
//   - error_message is the first failure's message, truncated to 2 KB.
//
// attempts must be non-empty (Validate guarantees that).
func DeriveResult(attempts []AttemptPayload) Derived {
	last := attempts[len(attempts)-1]

	d := Derived{
		FinalStatus: last.Status,
		Attempts:    len(attempts),
		DurationMs:  last.DurationMs,
	}

	if d.FinalStatus == StatusPassed {
		// attempts[:len(attempts)-1] is a slice expression: "every element
		// except the last one". We only care about earlier failures if the
		// test ultimately passed.
		for _, a := range attempts[:len(attempts)-1] {
			if a.Status == StatusFailed {
				d.PassedOnRetry = true
				break
			}
		}
	}

	for _, a := range attempts {
		if a.Status == StatusFailed && a.Error != "" {
			d.ErrorMessage = truncate(a.Error, 2048)
			break
		}
	}

	return d
}

// truncate cuts s down to at most max bytes, leaving it unchanged if it's
// already shorter.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
