package clients

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"flaketracker/internal/model"
)

// Analyzer calls the analyzer service's internal API (http://analyzer in the cluster).
type Analyzer struct {
	base
}

// NewAnalyzer returns a client for the analyzer service at baseURL.
func NewAnalyzer(baseURL string, timeout time.Duration) *Analyzer {
	return &Analyzer{newBase("analyzer", baseURL, timeout)}
}

// Tests returns one page of the leaderboard. query may carry "repo",
// "classification", "q", "limit", "offset".
func (c *Analyzer) Tests(ctx context.Context, query url.Values) (model.TestList, error) {
	var out model.TestList
	err := c.get(ctx, "/internal/v1/tests", query, &out)
	return out, err
}

// Test returns one test's stats (StatusError 404 if it isn't scored yet).
func (c *Analyzer) Test(ctx context.Context, id int64) (model.TestStats, error) {
	var out model.TestStats
	err := c.get(ctx, fmt.Sprintf("/internal/v1/tests/%d", id), nil, &out)
	return out, err
}

// Summary returns the analyzer's dashboard counts and build info.
func (c *Analyzer) Summary(ctx context.Context) (model.AnalyzerSummary, error) {
	var out model.AnalyzerSummary
	err := c.get(ctx, "/internal/v1/summary", nil, &out)
	return out, err
}
