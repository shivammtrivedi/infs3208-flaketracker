// cmd/seed generates deterministic demo data and loads it into the ingest
// service (Section 11.1 of the project plan). Run `go run ./cmd/seed --help`
// for every flag; they're also documented in the README.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"flaketracker/internal/db"
	"flaketracker/internal/seed"
)

// usageHeader is printed by --help above the auto-generated flag list.
// Backtick strings are "raw" string literals: newlines and quotes are kept
// exactly as written, with no escaping needed.
const usageHeader = `seed: generate deterministic CI test results and load them into ingest.

Modes:
  --mode=db   Bulk-insert straight into ingest-db (fast initial load).
              Connects using DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD,
              which must point at ingest's database (never analyzer-db/api-db).
  --mode=api  POST every run to ingest's public endpoint, exactly like CI does
              (exercises validation, auth and idempotency; usable as a load generator).

The same --seed and --end always produce identical data. Re-running the same
--seed in api mode is safe: ingest answers "200 duplicate" for every run.

Examples:
  go run ./cmd/seed --mode=db
  go run ./cmd/seed --mode=db --truncate
  go run ./cmd/seed --mode=api --url=http://localhost:8080 --api-key=ingestkey
  go run ./cmd/seed --mode=api --seed=7 --repos=1 --runs-per-repo=20 --concurrency=16

Flags:
`

func main() {
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usageHeader)
		flag.PrintDefaults()
	}

	mode := flag.String("mode", "", `where to load data: "db" or "api" (required)`)
	seedValue := flag.Uint64("seed", 42, "random seed; the same seed (and --end) reproduces the same data")
	repos := flag.Int("repos", 3, "number of repositories")
	testsPerRepo := flag.Int("tests-per-repo", 150, "test cases per repository")
	runsPerRepo := flag.Int("runs-per-repo", 300, "CI runs per repository, re-runs included")
	days := flag.Int("days", 60, "spread runs evenly over this many days")
	end := flag.String("end", "", "end of the generated time range, RFC 3339 (e.g. 2026-10-12T00:00:00Z); default: now")
	url := flag.String("url", "http://localhost:8080", "api mode: ingest base URL (in the cluster: the Ingress IP)")
	// Deliberately no env-var default here: flag.PrintDefaults would print
	// the default value in --help, which would leak the key to the terminal.
	apiKey := flag.String("api-key", "", "api mode: ingest API key (default: $INGEST_API_KEY)")
	concurrency := flag.Int("concurrency", 8, "api mode: number of parallel requests")
	truncate := flag.Bool("truncate", false, "db mode: wipe ingest-db's tables before loading (ingest-db only - reset analyzer-db separately)")
	flag.Parse()

	cfg := seed.Config{
		Seed:         *seedValue,
		Repos:        *repos,
		TestsPerRepo: *testsPerRepo,
		RunsPerRepo:  *runsPerRepo,
		Days:         *days,
		End:          time.Now().UTC(),
	}
	if *end != "" {
		t, err := time.Parse(time.RFC3339, *end)
		if err != nil {
			fail("--end must be RFC 3339, e.g. 2026-10-12T00:00:00Z: %v", err)
		}
		cfg.End = t
	}
	if cfg.Repos < 1 || cfg.TestsPerRepo < 1 || cfg.RunsPerRepo < 1 || cfg.Days < 1 {
		fail("--repos, --tests-per-repo, --runs-per-repo and --days must all be at least 1")
	}

	// Ctrl+C cancels ctx, which stops generation and in-flight requests cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	started := time.Now()
	var sum seed.Summary
	var err error

	switch *mode {
	case "db":
		pool, perr := db.NewPool(ctx)
		if perr != nil {
			fail("connect to ingest-db: %v", perr)
		}
		defer pool.Close()
		if *truncate {
			fmt.Println("seed: truncating ingest-db tables first")
		}
		sum, err = seed.LoadDB(ctx, pool, cfg, *truncate)

	case "api":
		key := *apiKey
		if key == "" {
			key = os.Getenv("INGEST_API_KEY")
		}
		if key == "" {
			fail("api mode needs --api-key or INGEST_API_KEY")
		}
		if *concurrency < 1 {
			fail("--concurrency must be at least 1")
		}
		var stats seed.APIStats
		sum, stats, err = seed.PostAPI(ctx, cfg, *url, key, *concurrency)
		fmt.Printf("seed: api: %d created (202), %d duplicates (200), %d failed\n", stats.Created, stats.Duplicates, stats.Failed)

	default:
		fail(`--mode is required: "db" or "api" (see --help)`)
	}

	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("seed: generated %d repos, %d tests (%d stable, %d flaky, %d broken, %d fixed), %d runs (%d re-runs), %d results\n",
		sum.Repos, sum.Tests, sum.Stable, sum.Flaky, sum.Broken, sum.Fixed, sum.Runs, sum.Reruns, sum.Results)
	fmt.Printf("seed: %s mode finished in %s\n", *mode, time.Since(started).Round(100*time.Millisecond))
}

// fail prints an error and exits with status 2 (the usual exit code for
// command-line usage errors). `args ...any` makes this variadic: it accepts
// any number of extra arguments, passed straight through to Fprintf.
func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "seed: "+format+"\n", args...)
	os.Exit(2)
}
