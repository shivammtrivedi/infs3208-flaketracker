// This file lives in `package ingest` (not `ingest_test`) specifically so it
// can call unexported names like truncate directly if it needs to - Go
// only enforces "unexported = private to the package", and a _test.go file
// declaring the same package counts as part of it.
package ingest

import (
	"strings"
	"testing"
	"time"
)

// Go's built-in testing style is "table-driven": one slice of input/expected
// pairs, one loop that runs them all through t.Run. There's no assertion
// library in the standard library (no assertEqual) - you compare values
// yourself and call t.Errorf/t.Fatalf to report a failure.
func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		payload RunPayload
		wantErr bool
	}{
		{
			name: "valid minimal payload",
			payload: RunPayload{
				Repository: "shivam/flaky-demo",
				CIRunID:    "1",
				Branch:     "main",
				CommitSHA:  "abc123",
				StartedAt:  time.Now(),
				Results: []ResultPayload{
					{Suite: "a.spec.ts", Name: "does a thing", Attempts: []AttemptPayload{{Status: StatusPassed}}},
				},
			},
			wantErr: false,
		},
		{
			name:    "missing every required field",
			payload: RunPayload{},
			wantErr: true,
		},
		{
			name: "invalid attempt status",
			payload: RunPayload{
				Repository: "r", CIRunID: "1", Branch: "main", CommitSHA: "abc", StartedAt: time.Now(),
				Results: []ResultPayload{
					{Suite: "a", Name: "b", Attempts: []AttemptPayload{{Status: "timedOut"}}},
				},
			},
			wantErr: true,
		},
		{
			name: "result with no attempts",
			payload: RunPayload{
				Repository: "r", CIRunID: "1", Branch: "main", CommitSHA: "abc", StartedAt: time.Now(),
				Results: []ResultPayload{{Suite: "a", Name: "b"}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		// Capturing `tt` as a local inside the loop body (the `tt := tt`
		// idiom isn't needed here since Go 1.22 fixed loop-variable capture,
		// but t.Run's own closure is what makes each case show up as its own
		// named sub-test in `go test -v` output).
		t.Run(tt.name, func(t *testing.T) {
			problems := tt.payload.Validate()
			if got := len(problems) > 0; got != tt.wantErr {
				t.Errorf("Validate() returned problems=%v (len=%d), want error=%v", problems, len(problems), tt.wantErr)
			}
		})
	}
}

func TestValidateDefaultsCIRunAttempt(t *testing.T) {
	p := RunPayload{
		Repository: "r", CIRunID: "1", Branch: "main", CommitSHA: "abc", StartedAt: time.Now(),
	}
	p.Validate()
	if p.CIRunAttempt != 1 {
		t.Errorf("CIRunAttempt = %d, want 1 (the Section 9.1 default when omitted)", p.CIRunAttempt)
	}
}

func TestDeriveResult(t *testing.T) {
	tests := []struct {
		name              string
		attempts          []AttemptPayload
		wantFinal         Status
		wantPassedOnRetry bool
		wantAttempts      int
	}{
		{
			name:         "single pass",
			attempts:     []AttemptPayload{{Status: StatusPassed}},
			wantFinal:    StatusPassed,
			wantAttempts: 1,
		},
		{
			name:              "failed then passed on retry",
			attempts:          []AttemptPayload{{Status: StatusFailed}, {Status: StatusPassed}},
			wantFinal:         StatusPassed,
			wantPassedOnRetry: true,
			wantAttempts:      2,
		},
		{
			name:         "always fails",
			attempts:     []AttemptPayload{{Status: StatusFailed}, {Status: StatusFailed}},
			wantFinal:    StatusFailed,
			wantAttempts: 2,
		},
		{
			name:         "skipped",
			attempts:     []AttemptPayload{{Status: StatusSkipped}},
			wantFinal:    StatusSkipped,
			wantAttempts: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DeriveResult(tt.attempts)
			if d.FinalStatus != tt.wantFinal {
				t.Errorf("FinalStatus = %v, want %v", d.FinalStatus, tt.wantFinal)
			}
			if d.PassedOnRetry != tt.wantPassedOnRetry {
				t.Errorf("PassedOnRetry = %v, want %v", d.PassedOnRetry, tt.wantPassedOnRetry)
			}
			if d.Attempts != tt.wantAttempts {
				t.Errorf("Attempts = %d, want %d", d.Attempts, tt.wantAttempts)
			}
		})
	}
}

func TestDeriveResultCapturesFirstFailureMessage(t *testing.T) {
	d := DeriveResult([]AttemptPayload{
		{Status: StatusFailed, Error: "first failure"},
		{Status: StatusFailed, Error: "second failure"},
	})
	if d.ErrorMessage != "first failure" {
		t.Errorf("ErrorMessage = %q, want %q", d.ErrorMessage, "first failure")
	}
}

func TestDeriveResultTruncatesErrorMessage(t *testing.T) {
	longError := strings.Repeat("x", 3000)
	d := DeriveResult([]AttemptPayload{{Status: StatusFailed, Error: longError}})
	if len(d.ErrorMessage) != 2048 {
		t.Errorf("ErrorMessage length = %d, want 2048", len(d.ErrorMessage))
	}
}
