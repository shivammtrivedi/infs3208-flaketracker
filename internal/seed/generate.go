// Package seed generates realistic, deterministic CI test-result data for
// FlakeTracker (Section 11.1 of the project plan) and loads it into the
// ingest service - either through ingest's public HTTP API (api.go) or by
// bulk-inserting straight into ingest-db (db.go).
//
// Why generate data at all? The demo must not depend on a live CI pipeline
// (decision D7), and a generator lets us control exactly how many tests are
// stable, flaky, broken or recently fixed - so the analyzer's output in
// Phase 4 can be checked against a known answer.
//
// This file is pure logic: no network, no database. It turns a Config into a
// stream of ingest.RunPayload values. Keeping I/O out of it is what makes it
// deterministic and easy to unit test.
package seed

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"flaketracker/internal/ingest"
)

// Config controls how much data is generated. The same Config - including
// Seed and End - always produces exactly the same data.
type Config struct {
	Seed         uint64    // seeds the random number generator
	Repos        int       // number of repositories
	TestsPerRepo int       // test cases per repository
	RunsPerRepo  int       // CI runs per repository (re-runs included)
	Days         int       // runs are spread evenly over this many days...
	End          time.Time // ...ending at this moment
}

// Summary describes what was generated, for printing at the end.
type Summary struct {
	Repos, Tests, Runs, Results  int
	Stable, Flaky, Broken, Fixed int
	Reruns                       int
}

// kind is the behaviour profile given to each generated test.
type kind int

// `iota` auto-numbers a block of constants (0, 1, 2, 3) - Go's usual way of
// writing an enum-like set when the actual values don't matter.
const (
	kindStable kind = iota
	kindFlaky
	kindBroken
	kindFixed
)

// Tuning knobs for the simulated CI behaviour. The shares and the stable
// failure rate come straight from Section 11.1; the rest are chosen to look
// like a real Playwright suite.
const (
	shareFlaky  = 0.10
	shareBroken = 0.03
	shareFixed  = 0.02

	maxAttempts         = 3     // matches Playwright `retries: 2` in CI (Section 11.2)
	stableFailProb      = 0.002 // ~0.2% genuine transient failures for healthy tests
	stableRetryFailProb = 0.1   // a transient failure almost always passes on retry
	skipProb            = 0.003 // the odd test.skip()
	rerunProb           = 0.15  // chance a run that ended with failures is re-run on the same commit
	newCommitProb       = 0.7   // otherwise a run reuses its branch's latest commit (nightly / manual re-trigger)
	mainBranchShare     = 0.6
	testsPerSuite       = 10
	timeoutMs           = 5000

	// pcgStream is the PCG generator's second seed word. It's a fixed,
	// arbitrary constant so that --seed alone decides the output.
	pcgStream = 0x9e3779b97f4a7c15
)

var (
	repoNames = []string{"acme/checkout-web", "acme/payments-api", "acme/inventory-service"}
	branches  = []string{"feature/new-checkout", "feature/login-redesign", "fix/cart-totals", "chore/bump-deps"}

	areas   = []string{"checkout", "cart", "login", "search", "profile", "payments", "inventory", "orders", "shipping", "admin", "notifications", "reports", "settings", "auth", "api"}
	verbs   = []string{"creates", "updates", "deletes", "renders", "validates", "loads", "submits", "rejects", "filters", "sorts", "exports", "retries", "caches", "paginates", "redirects"}
	objects = []string{"an order", "a cart item", "the user profile", "a payment", "an invoice", "search results", "the session", "a coupon", "an address", "a product", "a report", "a notification", "the password", "an access token", "a shipment"}

	flakyErrors = []string{
		"Timeout 5000ms exceeded waiting for locator('[data-test=submit]')",
		"Timeout 5000ms exceeded waiting for navigation",
		"expect(locator).toBeVisible() failed: element is not visible",
		"Error: element is detached from the DOM",
		"net::ERR_CONNECTION_RESET",
	}
	brokenErrors = []string{
		"expect(received).toBe(expected): expected 200, received 500",
		"TypeError: Cannot read properties of undefined (reading 'total')",
		"expect(locator).toHaveText() failed: expected \"$42.00\", received \"$0.00\"",
	}
	transientError = "net::ERR_NETWORK_CHANGED"
)

// testProfile is one generated test case plus the rules for how it behaves.
type testProfile struct {
	suite, name   string
	kind          kind
	failProb      float64   // first-attempt failure probability while misbehaving
	retryFailProb float64   // probability a retry fails too, while misbehaving
	changeAt      time.Time // broken: starts failing here; fixed: stops being flaky here
	baseMs        int       // typical duration of a passing attempt
	errMsg        string    // how this test fails (real flaky tests tend to fail the same way)
}

// Generate produces the data described by cfg and calls emit once per CI run,
// in a fixed order.
//
// Taking a callback (emit) instead of returning a giant slice means callers
// can stream: the API loader hands each run to an HTTP worker as soon as it
// exists, and memory stays flat no matter how large the config. In Go,
// functions are ordinary values, so passing one in like this is common -
// think of it as a Python generator turned inside out. If emit returns an
// error, generation stops and that error is returned.
func Generate(cfg Config, emit func(*ingest.RunPayload) error) (Summary, error) {
	// One random number generator, used strictly in sequence. That ordering
	// is what makes the output reproducible: same seed, same sequence of
	// "random" numbers, same data.
	rng := rand.New(rand.NewPCG(cfg.Seed, pcgStream))

	sum := Summary{Repos: cfg.Repos}
	start := cfg.End.Add(-time.Duration(cfg.Days) * 24 * time.Hour)

	for r := 0; r < cfg.Repos; r++ {
		tests := makeTests(rng, cfg, start)
		sum.Tests += len(tests)
		for _, t := range tests {
			switch t.kind {
			case kindStable:
				sum.Stable++
			case kindFlaky:
				sum.Flaky++
			case kindBroken:
				sum.Broken++
			case kindFixed:
				sum.Fixed++
			}
		}

		if err := generateRuns(rng, cfg, r, tests, start, &sum, emit); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// repoName returns a stable name for the i-th repository.
func repoName(i int) string {
	if i < len(repoNames) {
		return repoNames[i]
	}
	return fmt.Sprintf("acme/service-%d", i+1)
}

// makeTests builds one repository's test cases. Kinds are assigned by exact
// quota (then shuffled) rather than by coin flip, so the population mix is
// always exactly what Section 11.1 asks for - which makes Phase 4's "do the
// classifications match the seeded mix?" check meaningful.
func makeTests(rng *rand.Rand, cfg Config, start time.Time) []testProfile {
	n := cfg.TestsPerRepo
	quota := func(share float64) int { return int(math.Round(share * float64(n))) }

	// make([]kind, n) allocates a slice of n elements, each set to the zero
	// value - 0, which is kindStable. We then overwrite the first few.
	kinds := make([]kind, n)
	i := 0
	for _, q := range []struct {
		k     kind
		count int
	}{{kindFlaky, quota(shareFlaky)}, {kindBroken, quota(shareBroken)}, {kindFixed, quota(shareFixed)}} {
		for c := 0; c < q.count && i < n; c++ {
			kinds[i] = q.k
			i++
		}
	}
	rng.Shuffle(n, func(a, b int) { kinds[a], kinds[b] = kinds[b], kinds[a] })

	days := func(d float64) time.Duration { return time.Duration(d * 24 * float64(time.Hour)) }

	tests := make([]testProfile, n)
	for j := range tests {
		suiteIdx, inSuite := j/testsPerSuite, j%testsPerSuite
		t := testProfile{
			suite: suiteName(suiteIdx),
			// inSuite is 0–9 and verbs has 15 entries, so every name within a
			// suite starts with a different verb - names are unique per suite,
			// which ingest's (repository, suite, name) unique key requires.
			name:   verbs[inSuite%len(verbs)] + " " + objects[(inSuite+suiteIdx*3)%len(objects)],
			kind:   kinds[j],
			baseMs: 150 + rng.IntN(2500),
			errMsg: transientError,
		}

		switch t.kind {
		case kindFlaky:
			t.failProb = 0.05 + rng.Float64()*0.35    // 5–40% (Section 11.1)
			t.retryFailProb = 0.2 + rng.Float64()*0.8 // low = usually passes on retry; high = rarely does
			t.errMsg = flakyErrors[rng.IntN(len(flakyErrors))]
		case kindBroken:
			// Starts failing every time somewhere between 3 and 30 days ago.
			t.changeAt = cfg.End.Add(-days(3 + rng.Float64()*27))
			t.errMsg = brokenErrors[rng.IntN(len(brokenErrors))]
		case kindFixed:
			// Flaky until 1–4 days ago, stable since. The fix is deliberately
			// recent: it has to land INSIDE the analyzer's 30-result window
			// (~6 days at the default 5 runs/day), otherwise v1 and v2 would
			// both just see a clean window and score it identically.
			t.failProb = 0.2 + rng.Float64()*0.2
			t.retryFailProb = 0.3 + rng.Float64()*0.5
			t.changeAt = cfg.End.Add(-days(1 + rng.Float64()*3))
			t.errMsg = flakyErrors[rng.IntN(len(flakyErrors))]
		}
		tests[j] = t
	}
	return tests
}

// suiteName returns the file name of the i-th test suite in a repository.
func suiteName(i int) string {
	area := areas[i%len(areas)]
	if round := i / len(areas); round > 0 {
		return fmt.Sprintf("tests/%s-%d.spec.ts", area, round+1)
	}
	return "tests/" + area + ".spec.ts"
}

// generateRuns produces one repository's CI runs, oldest first, and emits each.
func generateRuns(rng *rand.Rand, cfg Config, repoIdx int, tests []testProfile, start time.Time, sum *Summary, emit func(*ingest.RunPayload) error) error {
	repo := repoName(repoIdx)
	step := cfg.End.Sub(start) / time.Duration(cfg.RunsPerRepo)
	latestCommit := map[string]string{} // branch -> its most recent commit SHA
	runNumber := 0

	// prev is a pointer to the previous run's payload (nil before the first
	// run). A nil check on a pointer is Go's way of saying "is there one yet?".
	var prev *ingest.RunPayload
	prevFailed := false

	for i := 0; i < cfg.RunsPerRepo; i++ {
		// Evenly spaced slots with some jitter, so runs never go backwards in time.
		at := start.Add(time.Duration(i)*step + time.Duration(rng.Float64()*0.8*float64(step)))

		var p *ingest.RunPayload
		if prev != nil && prevFailed && rng.Float64() < rerunProb {
			// A developer hit "Re-run jobs" in GitHub Actions: same run ID and
			// commit, next attempt number. This is what produces "same commit,
			// different outcome" evidence for commit_conflict_rate.
			p = &ingest.RunPayload{
				Repository:   repo,
				CIRunID:      prev.CIRunID,
				CIRunAttempt: prev.CIRunAttempt + 1,
				RunURL:       prev.RunURL,
				Branch:       prev.Branch,
				CommitSHA:    prev.CommitSHA,
				StartedAt:    at,
			}
			sum.Reruns++
		} else {
			runNumber++
			branch := "main"
			if rng.Float64() >= mainBranchShare {
				branch = branches[rng.IntN(len(branches))]
			}
			// `commit, ok := m[key]` is Go's two-value map lookup: ok reports
			// whether the key was present, instead of raising like Python's dict[key].
			commit, ok := latestCommit[branch]
			if !ok || rng.Float64() < newCommitProb {
				commit = randomSHA(rng)
				latestCommit[branch] = commit
			}
			// The run ID depends only on seed, repo and run number - not on
			// time - so re-running the seeder with the same --seed replays the
			// exact same runs, and ingest answers "200 duplicate" for each.
			id := fmt.Sprintf("seed%d-r%d-%d", cfg.Seed, repoIdx+1, runNumber)
			p = &ingest.RunPayload{
				Repository:   repo,
				CIRunID:      id,
				CIRunAttempt: 1,
				RunURL:       fmt.Sprintf("https://github.com/%s/actions/runs/%s", repo, id),
				Branch:       branch,
				CommitSHA:    commit,
				StartedAt:    at,
			}
		}

		p.Results = make([]ingest.ResultPayload, len(tests))
		prevFailed = false
		for j := range tests {
			attempts := tests[j].simulate(rng, at)
			if attempts[len(attempts)-1].Status == ingest.StatusFailed {
				prevFailed = true
			}
			p.Results[j] = ingest.ResultPayload{Suite: tests[j].suite, Name: tests[j].name, Attempts: attempts}
		}

		sum.Runs++
		sum.Results += len(tests)
		if err := emit(p); err != nil {
			return err
		}
		prev = p
	}
	return nil
}

// failureOdds returns this test's first-attempt and retry failure
// probabilities at a given moment.
func (t *testProfile) failureOdds(at time.Time) (first, retry float64) {
	switch t.kind {
	case kindFlaky:
		return t.failProb, t.retryFailProb
	case kindBroken:
		if !at.Before(t.changeAt) {
			return 1, 1 // genuinely broken: every attempt fails
		}
	case kindFixed:
		if at.Before(t.changeAt) {
			return t.failProb, t.retryFailProb
		}
	}
	return stableFailProb, stableRetryFailProb
}

// simulate plays out one test in one CI run: up to maxAttempts tries,
// stopping at the first pass - exactly what Playwright does with retries.
func (t *testProfile) simulate(rng *rand.Rand, at time.Time) []ingest.AttemptPayload {
	if rng.Float64() < skipProb {
		return []ingest.AttemptPayload{{Status: ingest.StatusSkipped}}
	}

	firstFail, retryFail := t.failureOdds(at)

	var attempts []ingest.AttemptPayload
	for a := 0; a < maxAttempts; a++ {
		failChance := firstFail
		if a > 0 {
			failChance = retryFail
		}
		if rng.Float64() >= failChance {
			return append(attempts, ingest.AttemptPayload{Status: ingest.StatusPassed, DurationMs: t.duration(rng)})
		}
		attempts = append(attempts, ingest.AttemptPayload{Status: ingest.StatusFailed, DurationMs: t.failedDuration(rng), Error: t.errMsg})
	}
	return attempts
}

// duration returns a passing attempt's duration: the test's typical time ±20%.
func (t *testProfile) duration(rng *rand.Rand) int {
	return int(float64(t.baseMs) * (0.8 + rng.Float64()*0.4))
}

// failedDuration returns a failing attempt's duration. Timeouts take the full
// timeout, which makes flaky tests show up as spikes on the duration chart.
func (t *testProfile) failedDuration(rng *rand.Rand) int {
	if strings.HasPrefix(t.errMsg, "Timeout") {
		return timeoutMs + rng.IntN(50)
	}
	return t.duration(rng)
}

// randomSHA returns a 40-hex-character fake git commit SHA.
func randomSHA(rng *rand.Rand) string {
	return fmt.Sprintf("%016x%016x%08x", rng.Uint64(), rng.Uint64(), rng.Uint32())
}
