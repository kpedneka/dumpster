package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	statsmem "github.com/kunalpednekar/dumpster/internal/stats/memory"
)

// TestStats_ReturnsCurrentTotals verifies the public GET /stats endpoint
// reflects whatever the underlying repository currently holds.
func TestStats_ReturnsCurrentTotals(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	repo := statsmem.New()
	if err := repo.RecordDocumentIndexed(context.Background(), 1000); err != nil {
		t.Fatalf("seed RecordDocumentIndexed: %v", err)
	}
	if err := repo.RecordQueryExecuted(context.Background(), 250); err != nil {
		t.Fatalf("seed RecordQueryExecuted: %v", err)
	}
	deps.Stats = repo
	router := NewRouter(deps)

	req := unauthRequest(http.MethodGet, "/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got statsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := statsResponse{
		DocumentsIndexed:     1,
		AvgDocumentSizeBytes: 1000,
		QueriesExecuted:      1,
		AvgQueryDurationMs:   250,
	}
	if got != want {
		t.Errorf("GET /stats = %+v, want %+v", got, want)
	}
}

// TestStats_DoesNotMintASession verifies the endpoint bypasses the anonymous
// session middleware entirely — no request carries a session cookie, and
// none should be set on the response either.
func TestStats_DoesNotMintASession(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)

	req := unauthRequest(http.MethodGet, "/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "session_id" {
			t.Errorf("GET /stats set a session_id cookie; want none for an unauthenticated public endpoint")
		}
	}
}

// TestStats_NotRegisteredWhenStatsRepoNil verifies the endpoint is entirely
// absent (404, not an empty/zero response) when Deps.Stats is nil.
func TestStats_NotRegisteredWhenStatsRepoNil(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	deps.Stats = nil
	router := NewRouter(deps)

	req := unauthRequest(http.MethodGet, "/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when Stats is nil", w.Code)
	}
}
