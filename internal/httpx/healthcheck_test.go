package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Errorf("checked %s, want /readyz", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ready.Close()
	if err := Healthcheck(ready.URL); err != nil {
		t.Errorf("ready service reported unhealthy: %v", err)
	}

	notReady := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer notReady.Close()
	if err := Healthcheck(notReady.URL); err == nil {
		t.Error("a 503 from /readyz should be unhealthy")
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()
	if err := Healthcheck(url); err == nil {
		t.Error("an unreachable service should be unhealthy")
	}
}
