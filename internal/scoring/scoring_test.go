package scoring

import (
	"math"
	"testing"
	"time"
)

var (
	now      = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	defaults = Params{WindowSize: 30, MinSamples: 5, BrokenStreak: 5, FlakyThreshold: 0.15, HalfLifeDays: 1.5, EvidenceThreshold: 0.5}
)

// history builds observations from a compact pattern, oldest first:
// 'P' = passed, 'F' = failed, 'R' = passed on retry. Results are spaced
// `every` apart and end at `end`; each result gets its own commit.
func history(pattern string, end time.Time, every time.Duration) []Observation {
	obs := make([]Observation, len(pattern))
	for i, ch := range pattern {
		obs[i] = Observation{
			StartedAt:     end.Add(-time.Duration(len(pattern)-1-i) * every),
			CommitSHA:     "c" + string(rune('A'+i%26)) + string(rune('a'+i/26)),
			Failed:        ch == 'F',
			PassedOnRetry: ch == 'R',
		}
	}
	return obs
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// TestClassification covers the cases listed in Section 10.5 for both versions.
func TestClassification(t *testing.T) {
	recent := func(pattern string) []Observation { return history(pattern, now, time.Hour) }

	sameCommitConflict := recent(repeat("P", 10))
	sameCommitConflict[4].Failed = true
	sameCommitConflict[4].CommitSHA = "shared"
	sameCommitConflict[5].CommitSHA = "shared" // passed on the same commit

	tests := []struct {
		name string
		obs  []Observation
		v1   Classification
		v2   Classification
	}{
		{"always passes", recent(repeat("P", 30)), Stable, Stable},
		{"always fails", recent(repeat("F", 30)), Broken, Broken},
		{"alternates", recent(repeat("PF", 15)), Flaky, Flaky},
		{"single recent retry-pass", recent(repeat("P", 29) + "R"), Flaky, Flaky},
		{"same-commit conflict", sameCommitConflict, Flaky, Flaky},
		{"too few samples", recent("PFPF"), InsufficientData, InsufficientData},
		{"broke recently", recent(repeat("P", 20) + repeat("F", 10)), Broken, Broken},
		// One flip, no retries, no conflicts: not flaky by any signal, and one
		// failure short of BrokenStreak. The next failure makes it broken.
		{"just started failing", recent(repeat("P", 26) + "FFFF"), Stable, Stable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := V1(tt.obs, defaults, now).Classification; got != tt.v1 {
				t.Errorf("v1 = %s, want %s", got, tt.v1)
			}
			if got := V2(tt.obs, defaults, now).Classification; got != tt.v2 {
				t.Errorf("v2 = %s, want %s", got, tt.v2)
			}
		})
	}
}

// TestV2DecaysOldFlakiness is the behaviour the rollout demo relies on: a test
// that was flaky days ago but has passed cleanly since stays "flaky" under v1
// (the old failures are still inside its 30-result window) but not under v2.
func TestV2DecaysOldFlakiness(t *testing.T) {
	// 30 results, one every 5 hours (~6 days). Flaky for the oldest 10, clean since.
	fixed := history("PRPFPRFPRP"+repeat("P", 20), now, 5*time.Hour)

	if got := V1(fixed, defaults, now).Classification; got != Flaky {
		t.Errorf("v1 = %s, want flaky (old failures still in the window)", got)
	}
	v2 := V2(fixed, defaults, now)
	if v2.Classification != Stable {
		t.Errorf("v2 = %s (score %.3f), want stable (old evidence has decayed)", v2.Classification, v2.FlakinessScore)
	}
	if v1 := V1(fixed, defaults, now); v2.FlakinessScore >= v1.FlakinessScore {
		t.Errorf("v2 score %.3f should be lower than v1 score %.3f", v2.FlakinessScore, v1.FlakinessScore)
	}
}

func TestV2IgnoresSingleOldRetryPass(t *testing.T) {
	// One transient retry-pass, ~5 days old (2.5 half-lives -> weight ~0.18 < 0.5).
	obs := history("R"+repeat("P", 29), now, 4*time.Hour)

	if got := V1(obs, defaults, now).Classification; got != Flaky {
		t.Errorf("v1 = %s, want flaky (any retry-pass counts)", got)
	}
	if got := V2(obs, defaults, now).Classification; got != Stable {
		t.Errorf("v2 = %s, want stable (a single old retry-pass has faded)", got)
	}
}

func TestScoreFormula(t *testing.T) {
	// 10 alternating results: every consecutive pair flips -> flip_rate 1,
	// no retries, every commit distinct -> no conflicts. Score = 0.5 * 1.
	r := V1(history(repeat("PF", 5), now, time.Hour), defaults, now)
	if r.FlipRate != 1 || r.RetryPassRate != 0 || r.CommitConflictRate != 0 {
		t.Errorf("signals = %.2f/%.2f/%.2f, want 1/0/0", r.FlipRate, r.RetryPassRate, r.CommitConflictRate)
	}
	if r.FlakinessScore != 0.5 {
		t.Errorf("score = %v, want 0.5", r.FlakinessScore)
	}
	if r.PassCount != 5 || r.FailCount != 5 {
		t.Errorf("pass/fail = %d/%d, want 5/5", r.PassCount, r.FailCount)
	}
}

func TestWindowKeepsMostRecentResults(t *testing.T) {
	// 40 failures followed by 30 passes: only the 30 passes are in the window.
	r := V1(history(repeat("F", 40)+repeat("P", 30), now, time.Hour), defaults, now)
	if r.SampleSize != 30 || r.FailCount != 0 || r.Classification != Stable {
		t.Errorf("sample=%d fails=%d class=%s, want 30/0/stable", r.SampleSize, r.FailCount, r.Classification)
	}
	if !r.LastResultAt.Equal(now) {
		t.Errorf("LastResultAt = %v, want %v", r.LastResultAt, now)
	}
}

// With every result at age zero all v2 weights are 1, so v2's signals and
// score must match v1's exactly - a check that the weighted formulas really
// do reduce to the plain ones.
func TestV2MatchesV1WhenNothingHasAged(t *testing.T) {
	obs := history("PFRPPFPRFP"+repeat("P", 10), now, 0)
	v1, v2 := V1(obs, defaults, now), V2(obs, defaults, now)
	for name, pair := range map[string][2]float64{
		"flip":     {v1.FlipRate, v2.FlipRate},
		"retry":    {v1.RetryPassRate, v2.RetryPassRate},
		"conflict": {v1.CommitConflictRate, v2.CommitConflictRate},
		"score":    {v1.FlakinessScore, v2.FlakinessScore},
	} {
		if math.Abs(pair[0]-pair[1]) > 1e-12 {
			t.Errorf("%s: v1=%v v2=%v, want equal", name, pair[0], pair[1])
		}
	}
}

func TestForVersion(t *testing.T) {
	for _, v := range []string{"v1", "v2"} {
		if _, err := ForVersion(v); err != nil {
			t.Errorf("ForVersion(%q): %v", v, err)
		}
	}
	if _, err := ForVersion("v3"); err == nil {
		t.Error("ForVersion(\"v3\") should fail")
	}
}
