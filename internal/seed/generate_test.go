package seed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"flaketracker/internal/ingest"
)

// smallConfig keeps tests fast while still producing every kind of test.
func smallConfig(seed uint64) Config {
	return Config{
		Seed:         seed,
		Repos:        2,
		TestsPerRepo: 40,
		RunsPerRepo:  60,
		Days:         12,
		End:          time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}
}

// collect runs Generate and gathers every emitted payload into a slice.
// t.Helper() makes failures point at the calling test's line, not this one.
func collect(t *testing.T, cfg Config) ([]*ingest.RunPayload, Summary) {
	t.Helper()
	var runs []*ingest.RunPayload
	sum, err := Generate(cfg, func(p *ingest.RunPayload) error {
		runs = append(runs, p)
		return nil
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return runs, sum
}

// digest fingerprints all generated data, so two outputs can be compared in one go.
func digest(t *testing.T, runs []*ingest.RunPayload) string {
	t.Helper()
	b, err := json.Marshal(runs)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestGenerateIsDeterministic(t *testing.T) {
	a, _ := collect(t, smallConfig(42))
	b, _ := collect(t, smallConfig(42))
	c, _ := collect(t, smallConfig(43))

	if digest(t, a) != digest(t, b) {
		t.Error("same seed produced different data")
	}
	if digest(t, a) == digest(t, c) {
		t.Error("different seeds produced identical data")
	}
}

func TestGenerateProducesValidPayloads(t *testing.T) {
	runs, _ := collect(t, smallConfig(1))
	for _, p := range runs {
		if problems := p.Validate(); len(problems) > 0 {
			t.Fatalf("run %s attempt %d is invalid: %v", p.CIRunID, p.CIRunAttempt, problems)
		}
	}
}

func TestGenerateCountsAndMix(t *testing.T) {
	cfg := smallConfig(7)
	runs, sum := collect(t, cfg)

	if want := cfg.Repos * cfg.RunsPerRepo; sum.Runs != want || len(runs) != want {
		t.Errorf("runs: summary=%d emitted=%d, want %d", sum.Runs, len(runs), want)
	}
	if want := cfg.Repos * cfg.TestsPerRepo * cfg.RunsPerRepo; sum.Results != want {
		t.Errorf("results = %d, want %d", sum.Results, want)
	}
	// 40 tests per repo: round(4.0)=4 flaky, round(1.2)=1 broken, round(0.8)=1 fixed.
	if sum.Flaky != 8 || sum.Broken != 2 || sum.Fixed != 2 || sum.Stable != 68 {
		t.Errorf("mix = %d stable / %d flaky / %d broken / %d fixed, want 68/8/2/2",
			sum.Stable, sum.Flaky, sum.Broken, sum.Fixed)
	}
}

func TestGenerateIncludesRetriesAndReruns(t *testing.T) {
	runs, sum := collect(t, smallConfig(42))

	retryPasses := 0
	for _, p := range runs {
		for _, r := range p.Results {
			if ingest.DeriveResult(r.Attempts).PassedOnRetry {
				retryPasses++
			}
		}
	}
	if retryPasses == 0 {
		t.Error("expected some results that passed on retry")
	}

	if sum.Reruns == 0 {
		t.Fatal("expected some same-commit re-runs")
	}
	// Every re-run must share run ID and commit with the run before it.
	for i, p := range runs {
		if p.CIRunAttempt > 1 {
			prev := runs[i-1]
			if prev.CIRunID != p.CIRunID || prev.CommitSHA != p.CommitSHA || prev.CIRunAttempt != p.CIRunAttempt-1 {
				t.Errorf("re-run %s/%d doesn't follow its original run", p.CIRunID, p.CIRunAttempt)
			}
		}
	}
}

func TestBrokenTestFailsEveryAttemptAfterBreaking(t *testing.T) {
	breakAt := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	tp := testProfile{kind: kindBroken, changeAt: breakAt, baseMs: 100, errMsg: "boom"}
	rng := rand.New(rand.NewPCG(1, 2))

	for i := 0; i < 200; i++ {
		attempts := tp.simulate(rng, breakAt.Add(time.Hour))
		if attempts[0].Status == ingest.StatusSkipped {
			continue
		}
		if len(attempts) != maxAttempts || ingest.DeriveResult(attempts).FinalStatus != ingest.StatusFailed {
			t.Fatalf("broken test after its break: got %+v, want %d failed attempts", attempts, maxAttempts)
		}
	}
}

func TestFixedTestStopsFlakingAfterFix(t *testing.T) {
	fixAt := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	tp := testProfile{kind: kindFixed, failProb: 0.4, retryFailProb: 0.5, changeAt: fixAt, baseMs: 100, errMsg: "flake"}

	first, _ := tp.failureOdds(fixAt.Add(-time.Hour))
	if first != 0.4 {
		t.Errorf("before the fix: first-attempt fail prob = %v, want 0.4", first)
	}
	first, _ = tp.failureOdds(fixAt.Add(time.Hour))
	if first != stableFailProb {
		t.Errorf("after the fix: first-attempt fail prob = %v, want %v", first, stableFailProb)
	}
}
