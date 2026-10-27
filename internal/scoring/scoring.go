// Package scoring implements FlakeTracker's flakiness scoring algorithms
// (Section 10 of the project plan) as pure functions: a test's recent results
// go in, a score and a classification come out. Nothing here touches the
// database or reads the clock (the caller passes "now"), which is what makes
// every rule straightforward to unit test.
//
// Both versions compute the same three signals from the same window of
// results. The only difference is how much each result counts:
//
//   - v1 counts every result in the window equally, and treats ANY retry-pass
//     or same-commit conflict in the window as proof of flakiness.
//   - v2 weights each result by its age (exponential decay with a half-life),
//     and needs a minimum amount of *weighted* evidence. Old evidence fades,
//     so a one-off transient failure or a test that was recently fixed drops
//     off the leaderboard instead of staying "flaky" until it scrolls out of
//     the window.
package scoring

import (
	"fmt"
	"math"
	"time"
)

// Observation is one non-skipped test result, as scoring needs it.
type Observation struct {
	StartedAt     time.Time // when the CI run started (orders the window, drives v2's decay)
	CommitSHA     string
	Failed        bool // final status was "failed" (otherwise "passed")
	PassedOnRetry bool // failed at least once, then passed, in the same run
}

// Params are the tunable knobs. They come from the ConfigMap, so they can be
// changed without rebuilding an image.
type Params struct {
	WindowSize        int     // how many most-recent results to look at (default 30)
	MinSamples        int     // fewer results than this -> insufficient_data (default 5)
	BrokenStreak      int     // this many consecutive final failures -> broken (default 5)
	FlakyThreshold    float64 // score at or above this -> flaky (default 0.15)
	HalfLifeDays      float64 // v2: a result's weight halves every this many days (default 1.5)
	EvidenceThreshold float64 // v2: weighted retry-pass/conflict evidence needed -> flaky (default 0.5)
}

// Classification is the verdict for one test.
type Classification string

const (
	Stable           Classification = "stable"
	Flaky            Classification = "flaky"
	Broken           Classification = "broken"
	InsufficientData Classification = "insufficient_data"
)

// Result is everything the analyzer stores in test_stats for one test.
type Result struct {
	WindowSize         int
	SampleSize         int
	PassCount          int
	FailCount          int
	FlipRate           float64
	RetryPassRate      float64
	CommitConflictRate float64
	FlakinessScore     float64 // 0.0 – 1.0
	Classification     Classification
	AlgorithmVersion   string
	LastResultAt       time.Time // zero if there are no results
}

// Algorithm is the signature both versions share. Declaring it as a named
// function type means the analyzer can hold "whichever algorithm this image
// was built with" in one variable and call it without caring which it is.
type Algorithm func(obs []Observation, p Params, now time.Time) Result

// ForVersion returns the algorithm for a version string ("v1" or "v2").
func ForVersion(v string) (Algorithm, error) {
	switch v {
	case "v1":
		return V1, nil
	case "v2":
		return V2, nil
	}
	return nil, fmt.Errorf("unknown algorithm version %q (want v1 or v2)", v)
}

// V1 scores obs (non-skipped results, oldest first) with every result
// weighted equally. Any retry-pass or same-commit conflict in the window
// classifies the test as flaky.
func V1(obs []Observation, p Params, now time.Time) Result {
	equal := func(Observation) float64 { return 1 }
	return score("v1", obs, p, equal, 0)
}

// V2 scores obs with each result weighted by 0.5^(age / half-life): a result
// from right now counts 1, one half-life ago 0.5, two half-lives ago 0.25...
// A test is flaky if its weighted score reaches FlakyThreshold, or if its
// weighted retry-pass + conflict evidence reaches EvidenceThreshold (0.5 by
// default = one event within the last half-life, or several older ones).
func V2(obs []Observation, p Params, now time.Time) Result {
	decay := func(o Observation) float64 {
		ageDays := now.Sub(o.StartedAt).Hours() / 24
		if ageDays < 0 {
			ageDays = 0 // clock skew: never weight a "future" result above 1
		}
		return math.Pow(0.5, ageDays/p.HalfLifeDays)
	}
	return score("v2", obs, p, decay, p.EvidenceThreshold)
}

// score is the shared implementation. weight decides how much each result
// counts; minEvidence is how much weighted retry-pass/conflict evidence is
// needed to call a test flaky on that basis alone (0 means "any at all").
//
// With every weight equal to 1, each weighted rate below reduces to exactly
// the plain v1 definition in Section 10.2 (e.g. flips ÷ (n − 1)).
func score(version string, obs []Observation, p Params, weight func(Observation) float64, minEvidence float64) Result {
	// Keep only the most recent WindowSize results. obs[i:] is a slice
	// expression meaning "from index i to the end" - no copying happens, it's
	// a view onto the same underlying array.
	if len(obs) > p.WindowSize {
		obs = obs[len(obs)-p.WindowSize:]
	}

	r := Result{WindowSize: p.WindowSize, SampleSize: len(obs), AlgorithmVersion: version}
	for _, o := range obs {
		if o.Failed {
			r.FailCount++
		} else {
			r.PassCount++
		}
	}
	if len(obs) > 0 {
		r.LastResultAt = obs[len(obs)-1].StartedAt
	}

	// Rule 1: too little history to judge.
	if len(obs) < p.MinSamples {
		r.Classification = InsufficientData
		return r
	}

	w := make([]float64, len(obs))
	for i, o := range obs {
		w[i] = weight(o)
	}

	// flip_rate: how often the outcome changes between consecutive results.
	// Each transition is weighted by the newer of its two results.
	var flips, flipTotal float64
	for i := 1; i < len(obs); i++ {
		flipTotal += w[i]
		if obs[i].Failed != obs[i-1].Failed {
			flips += w[i]
		}
	}
	r.FlipRate = ratio(flips, flipTotal)

	// retry_pass_rate: share of results that failed and then passed on retry.
	var retryPasses, total float64
	for i, o := range obs {
		total += w[i]
		if o.PassedOnRetry {
			retryPasses += w[i]
		}
	}
	r.RetryPassRate = ratio(retryPasses, total)

	// commit_conflict_rate: share of commits on which the test both passed
	// and failed - same code, different outcome, flaky by definition. Each
	// commit is weighted by its most recent result.
	type commitOutcome struct {
		passed, failed bool
		weight         float64
	}
	byCommit := map[string]*commitOutcome{}
	var commitOrder []string // maps iterate in random order; this keeps the float sums deterministic
	for i, o := range obs {
		c := byCommit[o.CommitSHA]
		if c == nil {
			c = &commitOutcome{}
			byCommit[o.CommitSHA] = c
			commitOrder = append(commitOrder, o.CommitSHA)
		}
		if o.Failed {
			c.failed = true
		} else {
			c.passed = true
		}
		c.weight = math.Max(c.weight, w[i])
	}
	var conflicts, commitTotal float64
	for _, sha := range commitOrder {
		c := byCommit[sha]
		commitTotal += c.weight
		if c.passed && c.failed {
			conflicts += c.weight
		}
	}
	r.CommitConflictRate = ratio(conflicts, commitTotal)

	r.FlakinessScore = clamp(0.5*r.FlipRate + 0.3*r.RetryPassRate + 0.2*r.CommitConflictRate)

	// Rules 2–4, in order. A Go `switch` with no value tests each case's
	// condition top to bottom and runs only the first one that's true.
	evidence := retryPasses + conflicts
	switch {
	case isBroken(obs, p.BrokenStreak):
		r.Classification = Broken
	case r.FlakinessScore >= p.FlakyThreshold || (evidence > 0 && evidence >= minEvidence):
		r.Classification = Flaky
	default:
		r.Classification = Stable
	}
	return r
}

// isBroken reports whether the last `streak` results all failed. Consistent
// failure points to a real bug, not flakiness. (A retry-pass has a final
// status of "passed", so "all failed" already excludes retry-passes, as
// Section 10.3 requires.)
func isBroken(obs []Observation, streak int) bool {
	if len(obs) < streak {
		return false
	}
	for _, o := range obs[len(obs)-streak:] {
		if !o.Failed {
			return false
		}
	}
	return true
}

// ratio divides, returning 0 instead of NaN when there's nothing to divide by.
func ratio(part, whole float64) float64 {
	if whole == 0 {
		return 0
	}
	return part / whole
}

func clamp(x float64) float64 {
	return math.Max(0, math.Min(1, x))
}
