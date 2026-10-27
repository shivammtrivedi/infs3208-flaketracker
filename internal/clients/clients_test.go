package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"flaketracker/internal/httpx"
	"flaketracker/internal/model"
)

// TestRetriesOnceAfterConnectionDrop simulates a pod dying mid-request: the
// first connection is closed without any response. The client should retry
// once and succeed.
func TestRetriesOnceAfterConnectionDrop(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// Hijack takes over the raw TCP connection so we can close it
			// without writing a response - exactly what a killed pod looks like.
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		httpx.WriteJSON(w, http.StatusOK, model.IngestSummary{RunsIngested: 7})
	}))
	defer srv.Close()

	s, err := NewIngest(srv.URL, time.Second).Summary(context.Background())
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if s.RunsIngested != 7 || calls.Load() != 2 {
		t.Errorf("runs=%d calls=%d, want 7 after exactly 2 calls", s.RunsIngested, calls.Load())
	}
}

func TestDoesNotRetryTimeoutsOrStatusErrors(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			select { // hang until the client gives up
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		}))
		defer srv.Close()

		if _, err := NewIngest(srv.URL, 100*time.Millisecond).Summary(context.Background()); err == nil {
			t.Fatal("want a timeout error")
		}
		if calls.Load() != 1 {
			t.Errorf("calls = %d, want 1 (timeouts are not retried)", calls.Load())
		}
	})

	t.Run("500", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			httpx.WriteError(w, http.StatusInternalServerError, "boom")
		}))
		defer srv.Close()

		_, err := NewIngest(srv.URL, time.Second).Summary(context.Background())
		if !IsStatus(err, http.StatusInternalServerError) {
			t.Fatalf("err = %v, want StatusError 500", err)
		}
		if calls.Load() != 1 {
			t.Errorf("calls = %d, want 1 (the service answered; retrying won't help)", calls.Load())
		}
	})
}

func TestForwardsRequestID(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(httpx.RequestIDHeader)
		httpx.WriteJSON(w, http.StatusOK, model.IngestSummary{})
	}))
	defer srv.Close()

	ctx := httpx.ContextWithRequestID(context.Background(), "abc123")
	if _, err := NewIngest(srv.URL, time.Second).Summary(ctx); err != nil {
		t.Fatal(err)
	}
	if got != "abc123" {
		t.Errorf("downstream saw X-Request-ID %q, want abc123", got)
	}
}
