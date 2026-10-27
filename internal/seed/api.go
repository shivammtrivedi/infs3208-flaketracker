package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"flaketracker/internal/ingest"
)

// APIStats counts how ingest answered each POSTed run.
type APIStats struct {
	Created    int64 // 202: new run stored
	Duplicates int64 // 200: already ingested (idempotent replay)
	Failed     int64 // gave up after retries
}

// errUnauthorized aborts the whole load early: if the API key is wrong, every
// single request would fail, so there's no point sending the other 899.
var errUnauthorized = errors.New("ingest rejected the API key (401): check --api-key / INGEST_API_KEY")

// PostAPI generates data and POSTs every run to ingest's public endpoint -
// the same path a real CI pipeline uses. It's slower than LoadDB but
// exercises validation, auth and idempotency end to end, and with a high
// --concurrency it doubles as a load generator.
//
// Concurrency model (a classic Go "worker pool"):
//   - this goroutine runs the generator and pushes each run into a channel;
//   - `concurrency` worker goroutines pull runs off the channel and POST them;
//   - the channel's small buffer gives back-pressure: if the workers fall
//     behind, sending blocks, so the generator never races ahead and fills memory.
func PostAPI(ctx context.Context, cfg Config, baseURL, apiKey string, concurrency int) (Summary, APIStats, error) {
	// WithCancelCause gives us a cancel function that also records WHY we
	// cancelled, so the caller gets "bad API key" rather than just "context canceled".
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	// The default HTTP transport keeps only 2 idle connections per host;
	// raising it lets every worker reuse its own keep-alive connection
	// instead of opening a new TCP connection per request.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = concurrency
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	url := strings.TrimRight(baseURL, "/") + "/ingest/v1/runs"

	// A channel is a typed, goroutine-safe queue. `chan *ingest.RunPayload`
	// carries pointers to payloads; the buffer size is how many can wait.
	jobs := make(chan *ingest.RunPayload, concurrency*2)

	// Workers bump these counters concurrently. atomic.Int64 makes each
	// increment safe without a mutex (plain `n++` from many goroutines is a
	// data race and can lose updates).
	var created, duplicates, failed atomic.Int64

	// A WaitGroup lets this goroutine wait until every worker has finished.
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Ranging over a channel keeps receiving until the channel is
			// closed (by close(jobs) below) and drained.
			for p := range jobs {
				status, err := postWithRetry(ctx, client, url, apiKey, p)
				switch {
				case err != nil:
					failed.Add(1)
					if errors.Is(err, errUnauthorized) {
						cancel(err)
					}
				case status == http.StatusAccepted:
					created.Add(1)
				case status == http.StatusOK:
					duplicates.Add(1)
				}
			}
		}()
	}

	sum, genErr := Generate(cfg, func(p *ingest.RunPayload) error {
		// select waits on several channel operations at once and runs
		// whichever is ready first: either a worker takes the job, or the
		// context is cancelled (bad key, Ctrl+C) and we stop generating.
		select {
		case jobs <- p:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	})
	close(jobs) // tells the workers no more jobs are coming
	wg.Wait()

	stats := APIStats{Created: created.Load(), Duplicates: duplicates.Load(), Failed: failed.Load()}
	if genErr != nil {
		return sum, stats, genErr
	}
	if stats.Failed > 0 {
		return sum, stats, fmt.Errorf("%d of %d runs could not be posted (see errors above)", stats.Failed, sum.Runs)
	}
	return sum, stats, nil
}

// postWithRetry POSTs one run, retrying network errors and 5xx/429 responses.
//
// Retrying a POST is normally risky (you might create the same thing twice),
// but here it's safe by design: ingest treats (repository, ci_run_id,
// ci_run_attempt) as an idempotency key, so if a first attempt actually
// succeeded but the response got lost, the retry just comes back as
// "200 duplicate". This is the same property the reliability demo relies on.
func postWithRetry(ctx context.Context, client *http.Client, url, apiKey string, p *ingest.RunPayload) (int, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}

	const maxTries = 3
	var lastErr error
	for try := 1; try <= maxTries; try++ {
		status, respBody, err := post(ctx, client, url, apiKey, body)
		switch {
		case err == nil && (status == http.StatusAccepted || status == http.StatusOK):
			return status, nil
		case status == http.StatusUnauthorized:
			return status, errUnauthorized
		case err == nil && status >= 400 && status < 500 && status != http.StatusTooManyRequests:
			// A 4xx means the request itself is wrong; sending it again won't help.
			return status, fmt.Errorf("run %s attempt %d rejected with %d: %s", p.CIRunID, p.CIRunAttempt, status, respBody)
		case err != nil:
			lastErr = err
		default:
			lastErr = fmt.Errorf("status %d: %s", status, respBody)
		}

		if try < maxTries {
			// Back off a little longer each time; give up early if cancelled.
			select {
			case <-time.After(time.Duration(try) * 500 * time.Millisecond):
			case <-ctx.Done():
				return 0, context.Cause(ctx)
			}
		}
	}
	fmt.Printf("seed: giving up on run %s attempt %d: %v\n", p.CIRunID, p.CIRunAttempt, lastErr)
	return 0, lastErr
}

// post sends one request and returns the status code plus (up to 1 KB of) the
// response body, for error messages.
func post(ctx context.Context, client *http.Client, url, apiKey string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	// Read (and discard) anything left, so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, bytes.TrimSpace(snippet), nil
}
