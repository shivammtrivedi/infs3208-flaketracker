// Package clients holds the small HTTP clients one FlakeTracker service uses
// to call another service's internal API. Under database-per-service these
// calls are the ONLY way data crosses a service boundary - no service ever
// connects to another service's database.
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"flaketracker/internal/httpx"
)

// StatusError means the other service answered, but not with 200 OK.
//
// It's a distinct type (rather than a plain fmt.Errorf string) so callers can
// tell "the analyzer says that test doesn't exist" (404 - pass it on to the
// user) apart from "the analyzer is down" (a network error - report 503).
// Callers check for it with errors.As.
type StatusError struct {
	Service string // "ingest" or "analyzer"
	Code    int    // HTTP status code it returned
	Message string // its {"error": "..."} message, or the start of the body
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s returned %d: %s", e.Service, e.Code, e.Message)
}

// base is what both clients share: which service, where it is, and an
// http.Client with a timeout.
type base struct {
	service string
	baseURL string
	http    *http.Client
}

func newBase(service, baseURL string, timeout time.Duration) base {
	return base{
		service: service,
		baseURL: strings.TrimRight(baseURL, "/"),
		// Always set a timeout on outgoing calls. Go's default client waits
		// forever, so one hung downstream service would hang the caller too -
		// the opposite of failure isolation.
		http: &http.Client{Timeout: timeout},
	}
}

// get GETs path (plus optional query parameters) and decodes a 200 JSON
// response into out.
//
// `out any` plus passing a pointer (e.g. &page) is how Go writes one decoder
// for many types: encoding/json fills in whatever the pointer points at.
// Query values go through url.Values.Encode, which escapes them - user input
// forwarded by api can't inject extra parameters or break the URL.
func (b base) get(ctx context.Context, path string, query url.Values, out any) error {
	u := b.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	resp, err := b.do(ctx, u)
	if err != nil && retryable(ctx, err) {
		// One retry, after a short pause. When a pod is deleted (the
		// reliability demo), there's a brief window in which a request can
		// still reach it and fail at the connection level; the retry gets
		// routed to a healthy replica instead of surfacing as a 503. It's
		// safe because every internal call is a read-only GET.
		select {
		case <-time.After(100 * time.Millisecond):
			resp, err = b.do(ctx, u)
		case <-ctx.Done():
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", b.service, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &StatusError{Service: b.service, Code: resp.StatusCode, Message: msg}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode %s: %w", b.service, path, err)
	}
	return nil
}

// do sends one GET, forwarding the caller's request ID so the downstream
// service's log lines carry the same ID as ours.
func (b base) do(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if id := httpx.RequestIDFrom(ctx); id != "" {
		req.Header.Set(httpx.RequestIDHeader, id)
	}
	return b.http.Do(req)
}

// retryable reports whether a failed call is worth one more try: a
// connection-level failure (refused, reset, closed) - but not a timeout,
// since the downstream is then probably overloaded and retrying would double
// the wait, and not if the caller has given up (cancelled context).
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	return true
}

// IsStatus reports whether err is a StatusError with the given code.
func IsStatus(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == code
}
