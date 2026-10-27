package clients

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"flaketracker/internal/model"
)

// Ingest calls the ingest service's internal API (http://ingest in the cluster).
type Ingest struct {
	base // embedded: Ingest gets base's fields and its get method
}

// NewIngest returns a client for the ingest service at baseURL. Each call
// fails if ingest hasn't answered within timeout.
func NewIngest(baseURL string, timeout time.Duration) *Ingest {
	return &Ingest{newBase("ingest", baseURL, timeout)}
}

// Results fetches one page of the results feed: up to limit results with an
// ID greater than afterID. (Used by the analyzer.)
func (c *Ingest) Results(ctx context.Context, afterID int64, limit int) (model.FeedPage, error) {
	var page model.FeedPage
	q := url.Values{"after_id": {strconv.FormatInt(afterID, 10)}, "limit": {strconv.Itoa(limit)}}
	err := c.get(ctx, "/internal/v1/results", q, &page)
	return page, err
}

// Repos lists repositories.
func (c *Ingest) Repos(ctx context.Context) (model.RepoList, error) {
	var out model.RepoList
	err := c.get(ctx, "/internal/v1/repos", nil, &out)
	return out, err
}

// Test returns one test's identity (StatusError 404 if ingest doesn't know it).
func (c *Ingest) Test(ctx context.Context, id int64) (model.TestIdentity, error) {
	var out model.TestIdentity
	err := c.get(ctx, fmt.Sprintf("/internal/v1/tests/%d", id), nil, &out)
	return out, err
}

// History returns a test's recent results. query may carry "limit".
func (c *Ingest) History(ctx context.Context, id int64, query url.Values) (model.TestHistory, error) {
	var out model.TestHistory
	err := c.get(ctx, fmt.Sprintf("/internal/v1/tests/%d/history", id), query, &out)
	return out, err
}

// Runs lists runs. query may carry "repo", "branch", "limit", "offset".
func (c *Ingest) Runs(ctx context.Context, query url.Values) (model.RunList, error) {
	var out model.RunList
	err := c.get(ctx, "/internal/v1/runs", query, &out)
	return out, err
}

// Run returns one run with its results.
func (c *Ingest) Run(ctx context.Context, id int64) (model.RunDetail, error) {
	var out model.RunDetail
	err := c.get(ctx, fmt.Sprintf("/internal/v1/runs/%d", id), nil, &out)
	return out, err
}

// Summary returns ingest's headline counts.
func (c *Ingest) Summary(ctx context.Context) (model.IngestSummary, error) {
	var out model.IngestSummary
	err := c.get(ctx, "/internal/v1/summary", nil, &out)
	return out, err
}
