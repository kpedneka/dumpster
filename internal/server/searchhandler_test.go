package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/search"
	searchmock "github.com/kunalpednekar/dumpster/internal/search/mock"
)

func TestSearch(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	doc, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: k.ID, UserID: userID, Filename: "notes.txt", ContentType: "text/plain",
	})

	chunkID := uuid.New()
	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary: "the answer",
		Citations: []search.Citation{
			{Number: 1, DocumentID: doc.ID, ChunkID: chunkID, CharStart: 0, CharEnd: 42, Text: "the source text"},
		},
	})
	router := NewRouter(deps)

	body := strings.NewReader(`{"query":"what is the answer?"}`)
	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search", body, userID)
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
	if got.Citations[0].DocumentID != doc.ID.String() {
		t.Errorf("citation document_id mismatch")
	}
	if got.Citations[0].CharStart != 0 || got.Citations[0].CharEnd != 42 {
		t.Errorf("citation range mismatch")
	}
	if got.Citations[0].Text != "the source text" {
		t.Errorf("citation text: got %q, want %q", got.Citations[0].Text, "the source text")
	}
	if got.Citations[0].FileName != "notes.txt" {
		t.Errorf("citation file_name: got %q, want %q", got.Citations[0].FileName, "notes.txt")
	}
	if got.Citations[0].Locator != nil {
		t.Errorf("citation locator: got %+v, want nil for a plain-text chunk", got.Citations[0].Locator)
	}
}

// TestSearch_CitationLocator_PDFPage verifies that a citation resolved from a
// PDF-derived chunk (PageNumber set) carries a page locator, mirroring how
// search engines cite the source page rather than a byte range within it.
func TestSearch_CitationLocator_PDFPage(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	doc, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: k.ID, UserID: userID, Filename: "paper.pdf", ContentType: "application/pdf",
	})

	page := 4
	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary: "the answer",
		Citations: []search.Citation{
			{Number: 1, DocumentID: doc.ID, ChunkID: uuid.New(), CharStart: 0, CharEnd: 10, Text: "conclusion text", PageNumber: &page},
		},
	})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"what is the conclusion?"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var got SearchResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Citations[0].FileName != "paper.pdf" {
		t.Errorf("citation file_name: got %q, want %q", got.Citations[0].FileName, "paper.pdf")
	}
	if got.Citations[0].Locator == nil {
		t.Fatalf("citation locator: got nil, want a page locator")
	}
	if got.Citations[0].Locator.Type != "page" || got.Citations[0].Locator.Value != 4 {
		t.Errorf("citation locator: got %+v, want {type:page value:4}", got.Citations[0].Locator)
	}
}

// TestSearch_CitationLocator_UnknownDocument verifies a citation still
// renders (with an empty file name) if its document can't be resolved,
// rather than failing the whole search response.
func TestSearch_CitationLocator_UnknownDocument(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")

	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary: "the answer",
		Citations: []search.Citation{
			{Number: 1, DocumentID: uuid.New(), ChunkID: uuid.New(), CharStart: 0, CharEnd: 10, Text: "text"},
		},
	})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
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
	if got.Citations[0].FileName != "" {
		t.Errorf("citation file_name: got %q, want empty for an unresolvable document", got.Citations[0].FileName)
	}
}

func TestSearch_MissingQuery(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
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

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+uuid.New().String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", w.Code)
	}
}

// TestSearch_NoSession verifies that a request with no session cookie
// receives a fresh session (middleware never 401s) and returns 404 since
// the KB doesn't belong to the newly-minted session.
func TestSearch_NoSession(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)

	req := unauthRequest(http.MethodPost, "/kbs/"+uuid.New().String()+"/search",
		strings.NewReader(`{"query":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("no-session search: got %d, want 404", w.Code)
	}
}

func TestSearch_TenantIsolation(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	user1, user2 := uuid.New(), uuid.New()

	k, _ := kbRepo.Create(context.TODO(), user1, "kb1")

	// user2 tries to search user1's KB
	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
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

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", w.Code)
	}
}

func TestSearch_RecordsLatencyMetric(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "the answer"})
	inst, metricsHandler := mustInstruments(t)
	deps.Instruments = inst
	router := NewRouter(deps)

	body := strings.NewReader(`{"query":"what is the answer?"}`)
	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search", body, userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", w.Code, w.Body)
	}

	got := scrapeMetrics(t, metricsHandler)
	if !hasHistogramCount(got, "search_latency_ms", 1) {
		t.Errorf("expected search_latency_ms sample, got:\n%s", got)
	}
}

func TestSearch_RecordsLatencyMetric_OnError(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewErrorSearcher("index unavailable")
	inst, metricsHandler := mustInstruments(t)
	deps.Instruments = inst
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", w.Code)
	}

	got := scrapeMetrics(t, metricsHandler)
	if !hasHistogramCount(got, "search_latency_ms", 1) {
		t.Errorf("expected search_latency_ms sample even on error, got:\n%s", got)
	}
}
