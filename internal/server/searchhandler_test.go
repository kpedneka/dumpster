package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/search"
	searchmock "github.com/kunalpednekar/dumpster/internal/search/mock"
)

func TestSearch(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")

	docID := uuid.New()
	chunkID := uuid.New()
	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary: "the answer",
		Citations: []search.Citation{
			{Number: 1, DocumentID: docID, ChunkID: chunkID, CharStart: 0, CharEnd: 42},
		},
	})
	router := NewRouter(deps)

	body := strings.NewReader(`{"query":"what is the answer?"}`)
	req := authedRequest(t, http.MethodPost, "/kbs/"+k.ID.String()+"/search", body, userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", w.Code, w.Body)
	}

	var got SearchResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Summary != "the answer" {
		t.Errorf("summary: got %q, want %q", got.Summary, "the answer")
	}
	if len(got.Citations) != 1 {
		t.Fatalf("citations: got %d, want 1", len(got.Citations))
	}
	if got.Citations[0].Number != 1 {
		t.Errorf("citation number: got %d, want 1", got.Citations[0].Number)
	}
	if got.Citations[0].DocumentID != docID.String() {
		t.Errorf("citation document_id mismatch")
	}
	if got.Citations[0].CharStart != 0 || got.Citations[0].CharEnd != 42 {
		t.Errorf("citation range mismatch")
	}
}

func TestSearch_MissingQuery(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	router := NewRouter(deps)

	req := authedRequest(t, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

func TestSearch_KBNotFound(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	req := authedRequest(t, http.MethodPost, "/kbs/"+uuid.New().String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", w.Code)
	}
}

func TestSearch_Unauthorized(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)

	req := httptest.NewRequest(http.MethodPost, "/kbs/"+uuid.New().String()+"/search",
		strings.NewReader(`{"query":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}

func TestSearch_TenantIsolation(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	user1, user2 := uuid.New(), uuid.New()

	k, _ := kbRepo.Create(context.TODO(), user1, "kb1")

	// user2 tries to search user1's KB
	req := authedRequest(t, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"secret"}`), user2)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant search: got %d, want 404", w.Code)
	}
}

func TestSearch_ServiceError(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewErrorSearcher("index unavailable")
	router := NewRouter(deps)

	req := authedRequest(t, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", w.Code)
	}
}
