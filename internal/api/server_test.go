package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"flaketracker/internal/clients"
	"flaketracker/internal/httpx"
	"flaketracker/internal/model"
	"flaketracker/internal/quarantine"
)

// ---- fakes ----

// fakeStore is an in-memory QuarantineStore that follows the same rules as
// the real one (one active quarantine per test).
type fakeStore struct {
	mu     sync.Mutex
	nextID int64
	rows   []model.Quarantine
}

func (f *fakeStore) active(id int64) int {
	for i, q := range f.rows {
		if q.TestCaseID == id && q.ReleasedAt == nil {
			return i
		}
	}
	return -1
}

func (f *fakeStore) List(_ context.Context, repo string) ([]model.Quarantine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []model.Quarantine{}
	for _, q := range f.rows {
		if q.ReleasedAt == nil && (repo == "" || q.Repository == repo) {
			out = append(out, q)
		}
	}
	return out, nil
}

func (f *fakeStore) Active(_ context.Context, id int64) (*model.Quarantine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := f.active(id); i >= 0 {
		q := f.rows[i]
		return &q, nil
	}
	return nil, nil
}

func (f *fakeStore) ActiveFor(_ context.Context, ids []int64) (map[int64]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[int64]bool{}
	for _, id := range ids {
		if f.active(id) >= 0 {
			m[id] = true
		}
	}
	return m, nil
}

func (f *fakeStore) CountActive(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, q := range f.rows {
		if q.ReleasedAt == nil {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) Create(_ context.Context, q model.Quarantine) (model.Quarantine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active(q.TestCaseID) >= 0 {
		return model.Quarantine{}, quarantine.ErrAlreadyActive
	}
	f.nextID++
	q.ID, q.CreatedAt = f.nextID, time.Now()
	f.rows = append(f.rows, q)
	return q, nil
}

func (f *fakeStore) Release(_ context.Context, id int64) (model.Quarantine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.active(id)
	if i < 0 {
		return model.Quarantine{}, quarantine.ErrNotActive
	}
	now := time.Now()
	f.rows[i].ReleasedAt = &now
	return f.rows[i], nil
}

// fakeAnalyzer serves a tiny version of the analyzer's internal API: two
// scored tests (IDs 1 and 2).
func fakeAnalyzer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	tests := []model.TestStats{
		{TestCaseID: 1, Repository: "acme/web", Suite: "a.spec.ts", Name: "flaky one", Classification: "flaky", FlakinessScore: 0.4, AlgorithmVersion: "v1"},
		{TestCaseID: 2, Repository: "acme/web", Suite: "a.spec.ts", Name: "stable one", Classification: "stable", AlgorithmVersion: "v1"},
	}
	mux.HandleFunc("GET /internal/v1/tests", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("classification") == "weird" {
			httpx.WriteError(w, http.StatusBadRequest, "classification must be one of ...")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, model.TestList{Tests: tests, Total: 2, Limit: 50})
	})
	mux.HandleFunc("GET /internal/v1/tests/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, ts := range tests {
			if r.PathValue("id") == strconv.FormatInt(ts.TestCaseID, 10) {
				httpx.WriteJSON(w, http.StatusOK, ts)
				return
			}
		}
		httpx.WriteError(w, http.StatusNotFound, "test not found or not scored yet")
	})
	mux.HandleFunc("GET /internal/v1/summary", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, model.AnalyzerSummary{TestsTracked: 2, AlgorithmVersion: "v2", Commit: "abc123"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeIngest knows test 1 and 2 only.
func fakeIngest(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/v1/tests/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if id != 1 && id != 2 {
			httpx.WriteError(w, http.StatusNotFound, "test not found")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, model.TestIdentity{TestCaseID: id, Repository: "acme/web", Suite: "a.spec.ts", Name: "some test"})
	})
	mux.HandleFunc("GET /internal/v1/summary", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, model.IngestSummary{RunsIngested: 900, ResultsIngested: 135000})
	})
	mux.HandleFunc("GET /internal/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, model.RunList{Runs: []model.Run{}, Total: 0, Limit: 50})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// deadURL returns the address of a server that has already shut down, so
// any call to it fails with "connection refused" - a stand-in for a crashed service.
func deadURL() string {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	return srv.URL
}

// newTestServer wires the api handlers to the given downstream URLs.
func newTestServer(ingestURL, analyzerURL string, store *fakeStore) http.Handler {
	s := NewServer(clients.NewIngest(ingestURL, time.Second), clients.NewAnalyzer(analyzerURL, time.Second), store)
	mux := http.NewServeMux()
	s.Register(mux)
	return mux
}

// do sends one request through the handler and returns the status and decoded JSON body.
func do(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder() // records what the handler writes, instead of a real network connection
	h.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: response is not a JSON object: %q", method, path, rec.Body.String())
	}
	return rec.Code, out
}

// ---- tests ----

func TestLeaderboardMergesQuarantineFlags(t *testing.T) {
	store := &fakeStore{}
	h := newTestServer(fakeIngest(t).URL, fakeAnalyzer(t).URL, store)

	if code, _ := do(t, h, "POST", "/api/v1/quarantine", `{"test_case_id": 1, "reason": "flaky on CI"}`); code != http.StatusCreated {
		t.Fatalf("create quarantine: status %d", code)
	}
	code, body := do(t, h, "GET", "/api/v1/tests", "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	rows := body["tests"].([]any)
	flags := map[float64]bool{}
	for _, row := range rows {
		m := row.(map[string]any)
		flags[m["test_case_id"].(float64)] = m["quarantined"].(bool)
		if m["classification"] == nil {
			t.Error("embedded TestStats fields should be flattened into each row")
		}
	}
	if !flags[1] || flags[2] {
		t.Errorf("quarantined flags = %v, want test 1 only", flags)
	}
}

func TestDownstreamFailuresAreIsolated(t *testing.T) {
	store := &fakeStore{}
	h := newTestServer(fakeIngest(t).URL, deadURL(), store) // analyzer is down

	code, body := do(t, h, "GET", "/api/v1/tests", "")
	if code != http.StatusServiceUnavailable || body["error"] != "analyzer unavailable" {
		t.Errorf("leaderboard with analyzer down: %d %v, want 503 analyzer unavailable", code, body)
	}
	// Endpoints that don't need the analyzer keep working.
	if code, _ := do(t, h, "GET", "/api/v1/quarantine", ""); code != http.StatusOK {
		t.Errorf("quarantine list with analyzer down: %d, want 200", code)
	}
	if code, _ := do(t, h, "GET", "/api/v1/runs", ""); code != http.StatusOK {
		t.Errorf("runs with analyzer down: %d, want 200", code)
	}
}

func TestSummaryDegradesGracefully(t *testing.T) {
	h := newTestServer(fakeIngest(t).URL, deadURL(), &fakeStore{})

	code, body := do(t, h, "GET", "/api/v1/summary", "")
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200 even with analyzer down", code)
	}
	if body["analyzer"] != nil {
		t.Errorf("analyzer = %v, want null", body["analyzer"])
	}
	if body["ingest"] == nil || body["quarantined"] == nil {
		t.Errorf("ingest/quarantined should still be present: %v", body)
	}
	if d := body["degraded"].([]any); len(d) != 1 || d[0] != "analyzer" {
		t.Errorf("degraded = %v, want [analyzer]", d)
	}
}

func TestDownstream400And404PassThrough(t *testing.T) {
	h := newTestServer(fakeIngest(t).URL, fakeAnalyzer(t).URL, &fakeStore{})

	if code, body := do(t, h, "GET", "/api/v1/tests?classification=weird", ""); code != http.StatusBadRequest || body["error"] == nil {
		t.Errorf("bad classification: %d %v, want 400 with message", code, body)
	}
	if code, _ := do(t, h, "GET", "/api/v1/tests/99", ""); code != http.StatusNotFound {
		t.Errorf("unknown test: %d, want 404", code)
	}
	if code, _ := do(t, h, "GET", "/api/v1/tests/abc", ""); code != http.StatusBadRequest {
		t.Errorf("non-numeric id: %d, want 400", code)
	}
}

func TestQuarantineLifecycle(t *testing.T) {
	store := &fakeStore{}
	h := newTestServer(fakeIngest(t).URL, fakeAnalyzer(t).URL, store)

	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"missing reason", "POST", "/api/v1/quarantine", `{"test_case_id": 2, "reason": "  "}`, http.StatusBadRequest},
		{"bad id", "POST", "/api/v1/quarantine", `{"test_case_id": 0, "reason": "x"}`, http.StatusBadRequest},
		{"bad json", "POST", "/api/v1/quarantine", `{not json`, http.StatusBadRequest},
		{"unknown test", "POST", "/api/v1/quarantine", `{"test_case_id": 99, "reason": "x"}`, http.StatusNotFound},
		{"create", "POST", "/api/v1/quarantine", `{"test_case_id": 2, "reason": "times out on CI"}`, http.StatusCreated},
		{"create again", "POST", "/api/v1/quarantine", `{"test_case_id": 2, "reason": "again"}`, http.StatusConflict},
		{"release", "DELETE", "/api/v1/quarantine/2", "", http.StatusOK},
		{"release again", "DELETE", "/api/v1/quarantine/2", "", http.StatusNotFound},
		{"re-quarantine after release", "POST", "/api/v1/quarantine", `{"test_case_id": 2, "reason": "back again"}`, http.StatusCreated},
	}
	for _, tc := range cases {
		if code, body := do(t, h, tc.method, tc.path, tc.body); code != tc.want {
			t.Errorf("%s: status %d (%v), want %d", tc.name, code, body, tc.want)
		}
	}

	// The detail view shows the active quarantine, with identity copied from ingest.
	_, body := do(t, h, "GET", "/api/v1/tests/2", "")
	q, ok := body["quarantine"].(map[string]any)
	if !ok || q["reason"] != "back again" || q["repository"] != "acme/web" || q["created_by"] != "dashboard" {
		t.Errorf("test detail quarantine = %v", body["quarantine"])
	}
}

func TestVersionReportsAnalyzerBuild(t *testing.T) {
	h := newTestServer(fakeIngest(t).URL, fakeAnalyzer(t).URL, &fakeStore{})
	_, body := do(t, h, "GET", "/api/v1/version", "")
	a, ok := body["analyzer"].(map[string]any)
	if !ok || a["algorithm_version"] != "v2" || a["commit"] != "abc123" {
		t.Errorf("version = %v", body)
	}

	h = newTestServer(fakeIngest(t).URL, deadURL(), &fakeStore{})
	if _, body := do(t, h, "GET", "/api/v1/version", ""); body["analyzer"] != nil || body["api"] == nil {
		t.Errorf("version with analyzer down = %v, want api present and analyzer null", body)
	}
}
