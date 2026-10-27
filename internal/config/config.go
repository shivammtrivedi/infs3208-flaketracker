// Package config reads service configuration from environment variables
// (the "12-factor app" approach: the same container image runs locally, in
// Compose, and in Kubernetes, and only the environment changes).
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"flaketracker/internal/scoring"
)

// DB holds PostgreSQL connection settings for ONE database.
//
// FlakeTracker uses database-per-service: ingest, analyzer and api each have
// their own private Postgres instance. The code is identical for all three -
// each service simply gets different DB_* values in its environment (its
// own Kubernetes Deployment/Job env block, or its own Compose service), so a
// service physically cannot connect to another service's database.
type DB struct {
	Host     string
	Port     string
	Name     string
	User     string
	Password string
}

// LoadDB reads Postgres settings from the environment. The second argument
// to getenv is the fallback used when the variable isn't set.
func LoadDB() DB {
	return DB{
		Host:     getenv("DB_HOST", "localhost"),
		Port:     getenv("DB_PORT", "5432"),
		Name:     getenv("DB_NAME", "flaketracker"),
		User:     getenv("DB_USER", "flaketracker"),
		Password: getenv("DB_PASSWORD", ""),
	}
}

// DSN returns a postgres:// connection string usable by both pgx and
// golang-migrate.
//
// `func (d DB) DSN()` declares a method on the DB type - Go's equivalent of
// a Python method taking `self`, except the receiver `d` is named explicitly.
// It's a value receiver (DB, not *DB) because it only reads the fields.
//
// Building the URL with net/url (rather than gluing strings together) makes
// sure a password containing characters like '@' or '/' is escaped
// correctly instead of silently producing a broken connection string.
func (d DB) DSN() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(d.User, d.Password),
		Host:     d.Host + ":" + d.Port,
		Path:     "/" + d.Name,
		RawQuery: "sslmode=disable", // traffic stays inside the cluster network
	}
	return u.String()
}

// Ingest holds settings specific to the ingest service.
type Ingest struct {
	Port    string
	APIKey  string
	FeedLag time.Duration // the results feed only hands out results at least this old
}

// LoadIngest reads the ingest service's settings from the environment.
func LoadIngest() (Ingest, error) {
	var p parser
	cfg := Ingest{
		Port:    getenv("PORT", "8080"),
		APIKey:  getenv("INGEST_API_KEY", ""),
		FeedLag: p.duration("FEED_LAG", 10*time.Second),
	}
	return cfg, p.err()
}

// API holds settings for the api service.
type API struct {
	Port              string
	IngestURL         string        // ingest's internal API (http://ingest in the cluster)
	AnalyzerURL       string        // analyzer's internal API (http://analyzer in the cluster)
	DownstreamTimeout time.Duration // give up on a downstream call after this long and return 503
}

// LoadAPI reads the api service's settings from the environment.
func LoadAPI() (API, error) {
	var p parser
	cfg := API{
		Port:              getenv("PORT", "8080"),
		IngestURL:         getenv("INGEST_URL", "http://localhost:8080"),
		AnalyzerURL:       getenv("ANALYZER_URL", "http://localhost:8081"),
		DownstreamTimeout: p.duration("DOWNSTREAM_TIMEOUT", 3*time.Second),
	}
	p.check(cfg.DownstreamTimeout >= 100*time.Millisecond, "DOWNSTREAM_TIMEOUT must be at least 100ms")
	return cfg, p.err()
}

// Analyzer holds settings for the analyzer service.
type Analyzer struct {
	Port         string
	IngestURL    string        // base URL of ingest's internal API (http://ingest in the cluster)
	Interval     time.Duration // how often the leader runs a cycle
	FeedPageSize int           // results requested per feed page
	FeedMaxPages int           // cap on pages pulled per cycle, so one cycle can't run forever
	RescoreBatch int           // cap on tests re-scored per cycle
	Scoring      scoring.Params
}

// LoadAnalyzer reads the analyzer's settings from the environment and checks
// they make sense together.
func LoadAnalyzer() (Analyzer, error) {
	var p parser
	cfg := Analyzer{
		Port:         getenv("PORT", "8080"),
		IngestURL:    getenv("INGEST_URL", "http://localhost:8080"),
		Interval:     p.duration("ANALYZER_INTERVAL", 15*time.Second),
		FeedPageSize: p.int("FEED_PAGE_SIZE", 5000),
		FeedMaxPages: p.int("FEED_MAX_PAGES", 50),
		RescoreBatch: p.int("RESCORE_BATCH", 1000),
		Scoring: scoring.Params{
			WindowSize:        p.int("WINDOW_SIZE", 30),
			MinSamples:        p.int("MIN_SAMPLES", 5),
			BrokenStreak:      p.int("BROKEN_STREAK", 5),
			FlakyThreshold:    p.float("FLAKY_THRESHOLD", 0.15),
			HalfLifeDays:      p.float("HALF_LIFE_DAYS", 1.5),
			EvidenceThreshold: p.float("EVIDENCE_THRESHOLD", 0.5),
		},
	}

	s := cfg.Scoring
	p.check(cfg.Interval >= time.Second, "ANALYZER_INTERVAL must be at least 1s")
	p.check(cfg.FeedPageSize >= 1 && cfg.FeedPageSize <= 10000, "FEED_PAGE_SIZE must be between 1 and 10000")
	p.check(cfg.FeedMaxPages >= 1, "FEED_MAX_PAGES must be at least 1")
	p.check(cfg.RescoreBatch >= 1, "RESCORE_BATCH must be at least 1")
	p.check(s.WindowSize >= 1, "WINDOW_SIZE must be at least 1")
	p.check(s.MinSamples >= 1 && s.MinSamples <= s.WindowSize, "MIN_SAMPLES must be between 1 and WINDOW_SIZE")
	p.check(s.BrokenStreak >= 1 && s.BrokenStreak <= s.WindowSize, "BROKEN_STREAK must be between 1 and WINDOW_SIZE")
	p.check(s.FlakyThreshold > 0 && s.FlakyThreshold <= 1, "FLAKY_THRESHOLD must be in (0, 1]")
	p.check(s.HalfLifeDays > 0, "HALF_LIFE_DAYS must be greater than 0")
	p.check(s.EvidenceThreshold >= 0, "EVIDENCE_THRESHOLD must not be negative")
	return cfg, p.err()
}

// parser reads typed values from the environment and collects every problem
// it finds, so a misconfigured pod reports all of its bad variables in one
// log line instead of making you fix them one restart at a time.
//
// Its methods use a pointer receiver (*parser) because they append to p.errs;
// with a value receiver each call would modify a throwaway copy.
type parser struct{ errs []error }

func (p *parser) int(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s=%q is not an integer", key, raw))
		return def
	}
	return n
}

func (p *parser) float(key string, def float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s=%q is not a number", key, raw))
		return def
	}
	return f
}

// duration parses Go duration syntax such as "15s", "2m" or "1h30m".
func (p *parser) duration(key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s=%q is not a duration like 15s or 2m", key, raw))
		return def
	}
	return d
}

func (p *parser) check(ok bool, msg string) {
	if !ok {
		p.errs = append(p.errs, errors.New(msg))
	}
}

// err combines every collected problem into one error (nil if there were none).
func (p *parser) err() error {
	return errors.Join(p.errs...)
}

// getenv returns the environment variable `key`, or `fallback` if it's unset
// or empty. Lowercase name = unexported, so only code inside this package can
// call it (Go uses capitalisation instead of public/private keywords).
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
