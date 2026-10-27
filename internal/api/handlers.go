package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"flaketracker/internal/clients"
	"flaketracker/internal/httpx"
	"flaketracker/internal/model"
	"flaketracker/internal/quarantine"
	"flaketracker/internal/version"
)

// ---- Response types that only the public API returns ----

// Summary is the overview dashboard (F1). Each part comes from a different
// service, so each part can be missing (null) on its own: if the analyzer is
// down, the run counts and quarantine count still show, and Degraded says
// what's missing. Pointer fields are how Go expresses "may be null" in JSON.
type Summary struct {
	Analyzer    *model.AnalyzerSummary `json:"analyzer"`
	Ingest      *model.IngestSummary   `json:"ingest"`
	Quarantined *int                   `json:"quarantined"`
	Degraded    []string               `json:"degraded"` // services that didn't answer
}

// LeaderboardRow is analyzer's stats for one test plus api's quarantine flag.
// Embedding model.TestStats flattens its fields into the same JSON object.
type LeaderboardRow struct {
	model.TestStats
	Quarantined bool `json:"quarantined"`
}

// Leaderboard is one page of the leaderboard (F2).
type Leaderboard struct {
	Tests  []LeaderboardRow `json:"tests"`
	Total  int              `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

// TestDetail is one test's stats plus its active quarantine, if any (F3).
type TestDetail struct {
	model.TestStats
	Quarantine *model.Quarantine `json:"quarantine"`
}

// BuildInfo is what the dashboard footer shows to prove which version is live.
type BuildInfo struct {
	API      Build  `json:"api"`
	Analyzer *Build `json:"analyzer"` // null if the analyzer didn't answer
}

// Build describes one service's running build.
type Build struct {
	Version   string `json:"version,omitempty"`
	Commit    string `json:"commit"`
	Algorithm string `json:"algorithm_version,omitempty"`
}

// ---- F1: overview ----

// summary serves GET /api/v1/summary. It asks analyzer, ingest and its own
// database at the same time rather than one after another, so the response
// takes as long as the slowest of the three, not the sum of all three.
func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out Summary

	// A WaitGroup counts running goroutines: Add before starting each one,
	// Done (deferred) when it finishes, and Wait blocks until the count is 0.
	// Each goroutine writes only its own variable, so no locking is needed
	// for those; `degraded` is shared, so a Mutex guards it.
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		degraded []string
	)
	markDegraded := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		degraded = append(degraded, name)
	}

	wg.Add(3)
	go func() {
		defer wg.Done()
		if a, err := s.analyzer.Summary(ctx); err == nil {
			out.Analyzer = &a
		} else {
			markDegraded("analyzer")
		}
	}()
	go func() {
		defer wg.Done()
		if in, err := s.ingest.Summary(ctx); err == nil {
			out.Ingest = &in
		} else {
			markDegraded("ingest")
		}
	}()
	go func() {
		defer wg.Done()
		if n, err := s.quarantines.CountActive(ctx); err == nil {
			out.Quarantined = &n
		} else {
			markDegraded("api-db")
		}
	}()
	wg.Wait()

	out.Degraded = degraded
	if out.Degraded == nil {
		out.Degraded = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// version serves GET /api/v1/version for the UI footer. During the rollout
// demo the analyzer's algorithm_version flips from v1 to v2 (and back on
// rollback) - the visible proof of which image is serving.
func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	info := BuildInfo{API: Build{Version: version.Version, Commit: version.Commit}}
	if a, err := s.analyzer.Summary(r.Context()); err == nil {
		info.Analyzer = &Build{Commit: a.Commit, Algorithm: a.AlgorithmVersion}
	}
	httpx.WriteJSON(w, http.StatusOK, info)
}

// repos serves GET /api/v1/repos (filter dropdowns).
func (s *Server) repos(w http.ResponseWriter, r *http.Request) {
	list, err := s.ingest.Repos(r.Context())
	if err != nil {
		s.downstreamError(w, r, "ingest", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

// ---- F2 / F3: leaderboard and test detail ----

// listTests serves GET /api/v1/tests?repo=&classification=&q=&limit=&offset=.
// The analyzer validates the parameters and does the ranking; api adds a
// quarantined flag to each row from api-db.
func (s *Server) listTests(w http.ResponseWriter, r *http.Request) {
	list, err := s.analyzer.Tests(r.Context(), forward(r, "repo", "classification", "q", "limit", "offset"))
	if err != nil {
		s.downstreamError(w, r, "analyzer", err)
		return
	}

	ids := make([]int64, len(list.Tests))
	for i, t := range list.Tests {
		ids[i] = t.TestCaseID
	}
	quarantined, err := s.quarantines.ActiveFor(r.Context(), ids)
	if err != nil {
		s.internalError(w, r, "load quarantine flags", err)
		return
	}

	out := Leaderboard{Tests: make([]LeaderboardRow, len(list.Tests)), Total: list.Total, Limit: list.Limit, Offset: list.Offset}
	for i, t := range list.Tests {
		out.Tests[i] = LeaderboardRow{TestStats: t, Quarantined: quarantined[t.TestCaseID]}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// getTest serves GET /api/v1/tests/{id}: analyzer's stats + api's quarantine.
func (s *Server) getTest(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	stats, err := s.analyzer.Test(r.Context(), id)
	if err != nil {
		s.downstreamError(w, r, "analyzer", err)
		return
	}
	q, err := s.quarantines.Active(r.Context(), id)
	if err != nil {
		s.internalError(w, r, "load quarantine", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, TestDetail{TestStats: stats, Quarantine: q})
}

// history serves GET /api/v1/tests/{id}/history?limit= (timeline + duration chart).
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	h, err := s.ingest.History(r.Context(), id, forward(r, "limit"))
	if err != nil {
		s.downstreamError(w, r, "ingest", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h)
}

// ---- F5: run explorer ----

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	list, err := s.ingest.Runs(r.Context(), forward(r, "repo", "branch", "limit", "offset"))
	if err != nil {
		s.downstreamError(w, r, "ingest", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	run, err := s.ingest.Run(r.Context(), id)
	if err != nil {
		s.downstreamError(w, r, "ingest", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, run)
}

// ---- F4: quarantine ----

// listQuarantines serves GET /api/v1/quarantine?repo= - answered entirely
// from api-db, so it keeps working even with ingest and analyzer both down.
// CI pipelines call this to know which tests to skip.
func (s *Server) listQuarantines(w http.ResponseWriter, r *http.Request) {
	list, err := s.quarantines.List(r.Context(), r.URL.Query().Get("repo"))
	if err != nil {
		s.internalError(w, r, "list quarantines", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"quarantines": list})
}

// quarantineRequest is the JSON body of POST /api/v1/quarantine.
type quarantineRequest struct {
	TestCaseID int64  `json:"test_case_id"`
	Reason     string `json:"reason"`
	CreatedBy  string `json:"created_by"` // optional, defaults to "dashboard"
}

const (
	maxReasonLen    = 500
	maxCreatedByLen = 100
)

// createQuarantine serves POST /api/v1/quarantine.
func (s *Server) createQuarantine(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10) // 16 KB is plenty for this body
	var req quarantineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	req.CreatedBy = strings.TrimSpace(req.CreatedBy)
	if req.CreatedBy == "" {
		req.CreatedBy = "dashboard"
	}
	switch {
	case req.TestCaseID < 1:
		httpx.WriteError(w, http.StatusBadRequest, "test_case_id must be a positive integer")
		return
	case req.Reason == "":
		httpx.WriteError(w, http.StatusBadRequest, "reason is required")
		return
	case len(req.Reason) > maxReasonLen:
		httpx.WriteError(w, http.StatusBadRequest, "reason must be at most 500 characters")
		return
	case len(req.CreatedBy) > maxCreatedByLen:
		httpx.WriteError(w, http.StatusBadRequest, "created_by must be at most 100 characters")
		return
	}

	// Ask ingest (the owner of test identities) whether this test exists,
	// and copy its repository/suite/name into our own record.
	t, err := s.ingest.Test(r.Context(), req.TestCaseID)
	if clients.IsStatus(err, http.StatusNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "unknown test_case_id")
		return
	}
	if err != nil {
		s.downstreamError(w, r, "ingest", err)
		return
	}

	q, err := s.quarantines.Create(r.Context(), model.Quarantine{
		TestCaseID: t.TestCaseID,
		Repository: t.Repository,
		Suite:      t.Suite,
		Name:       t.Name,
		Reason:     req.Reason,
		CreatedBy:  req.CreatedBy,
	})
	if errors.Is(err, quarantine.ErrAlreadyActive) {
		httpx.WriteError(w, http.StatusConflict, "test is already quarantined")
		return
	}
	if err != nil {
		s.internalError(w, r, "create quarantine", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, q)
}

// releaseQuarantine serves DELETE /api/v1/quarantine/{test_case_id}.
func (s *Server) releaseQuarantine(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "test_case_id")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	q, err := s.quarantines.Release(r.Context(), id)
	if errors.Is(err, quarantine.ErrNotActive) {
		httpx.WriteError(w, http.StatusNotFound, "test is not quarantined")
		return
	}
	if err != nil {
		s.internalError(w, r, "release quarantine", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, q)
}

// Compile-time check that the real store satisfies the interface: assigning
// to the blank identifier `_` costs nothing at runtime, but the build fails
// here (with a clear message) if the two ever drift apart.
var _ QuarantineStore = (*quarantine.Store)(nil)
