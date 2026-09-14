package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
)

func TestKBList_Pagination(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	for i := range 5 {
		_, _ = kbRepo.Create(context.TODO(), userID, fmt.Sprintf("kb%d", i))
	}

	// First page: limit=3
	req := authedRequest(t, deps, http.MethodGet, "/kbs?limit=3", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var page1 KBPage
	if err := json.NewDecoder(w.Body).Decode(&page1); err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 3 {
		t.Fatalf("page1 count: got %d, want 3", len(page1.Items))
	}
	if page1.NextCursor == "" {
		t.Fatal("page1: expected next_cursor to be set")
	}

	// Second page using cursor
	req2 := authedRequest(t, deps, http.MethodGet, "/kbs?limit=3&after="+page1.NextCursor, nil, userID)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	var page2 KBPage
	if err := json.NewDecoder(w2.Body).Decode(&page2); err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 2 {
		t.Fatalf("page2 count: got %d, want 2", len(page2.Items))
	}
	if page2.NextCursor != "" {
		t.Errorf("page2: expected no next_cursor on last page, got %q", page2.NextCursor)
	}

	// All IDs must be unique across both pages
	seen := map[string]bool{}
	for _, k := range append(page1.Items, page2.Items...) {
		if seen[k.ID.String()] {
			t.Errorf("duplicate ID %s across pages", k.ID)
		}
		seen[k.ID.String()] = true
	}
}

func TestDocList_Pagination(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")

	for i := range 4 {
		_, _ = docRepo.Create(context.TODO(), &document.Document{
			KBID:        k.ID,
			UserID:      userID,
			Filename:    fmt.Sprintf("file%d.txt", i),
			S3Key:       fmt.Sprintf("key%d", i),
			ContentType: "text/plain",
			Status:      document.StatusPending,
		})
	}

	// First page: limit=2
	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/documents?limit=2", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var page1 DocumentPage
	if err := json.NewDecoder(w.Body).Decode(&page1); err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 2 {
		t.Fatalf("page1 count: got %d, want 2", len(page1.Items))
	}
	if page1.NextCursor == "" {
		t.Fatal("page1: expected next_cursor")
	}

	// Second page using cursor
	req2 := authedRequest(t, deps, http.MethodGet,
		"/kbs/"+k.ID.String()+"/documents?limit=2&after="+page1.NextCursor, nil, userID)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	var page2 DocumentPage
	if err := json.NewDecoder(w2.Body).Decode(&page2); err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 2 {
		t.Fatalf("page2 count: got %d, want 2", len(page2.Items))
	}
	if page2.NextCursor != "" {
		t.Errorf("page2: expected no next_cursor on last page, got %q", page2.NextCursor)
	}
}

func TestKBList_NoNextCursorOnLastPage(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	for i := range 3 {
		_, _ = kbRepo.Create(context.TODO(), userID, fmt.Sprintf("kb%d", i))
	}

	req := authedRequest(t, deps, http.MethodGet, "/kbs", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var page KBPage
	if err := json.NewDecoder(w.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Errorf("count: got %d, want 3", len(page.Items))
	}
	if page.NextCursor != "" {
		t.Errorf("unexpected next_cursor when all items fit: %q", page.NextCursor)
	}
}

func TestOpenAPISpec(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)

	req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type: got %q, want application/yaml", ct)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty OpenAPI spec body")
	}
}

func TestOpenAPISpec_NoAuthRequired(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)

	// No auth header — must still return 200
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("openapi.yaml should not require auth: got %d", w.Code)
	}
}
