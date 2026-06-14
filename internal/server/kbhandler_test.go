package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/kb"
)

func TestKBCreate(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	req := authedRequest(t, http.MethodPost, "/kbs", strings.NewReader(`{"name":"my kb"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want %d — body: %s", w.Code, http.StatusCreated, w.Body)
	}

	var got kb.KnowledgeBase
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "my kb" {
		t.Errorf("name: got %q, want %q", got.Name, "my kb")
	}
	if got.UserID != userID {
		t.Errorf("user_id mismatch")
	}
}

func TestKBCreate_MissingName(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	req := authedRequest(t, http.MethodPost, "/kbs", strings.NewReader(`{}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

func TestKBCreate_Unauthorized(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)

	req := httptest.NewRequest(http.MethodPost, "/kbs", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}

func TestKBList(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	// Create two KBs for the user and one for another user to confirm isolation.
	kbRepo.Create(nil, userID, "alpha")    //nolint:errcheck
	kbRepo.Create(nil, userID, "beta")     //nolint:errcheck
	kbRepo.Create(nil, uuid.New(), "other") //nolint:errcheck

	req := authedRequest(t, http.MethodGet, "/kbs", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var got []*kb.KnowledgeBase
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("count: got %d, want 2", len(got))
	}
}

func TestKBGet(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	created, _ := kbRepo.Create(nil, userID, "test kb")

	req := authedRequest(t, http.MethodGet, "/kbs/"+created.ID.String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var got kb.KnowledgeBase
	json.NewDecoder(w.Body).Decode(&got) //nolint:errcheck
	if got.ID != created.ID {
		t.Errorf("id mismatch")
	}
}

func TestKBGet_NotFound(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	req := authedRequest(t, http.MethodGet, "/kbs/"+uuid.New().String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", w.Code)
	}
}

func TestKBRename(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	created, _ := kbRepo.Create(nil, userID, "old name")

	req := authedRequest(t, http.MethodPatch, "/kbs/"+created.ID.String(),
		strings.NewReader(`{"name":"new name"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", w.Code, w.Body)
	}

	var got kb.KnowledgeBase
	json.NewDecoder(w.Body).Decode(&got) //nolint:errcheck
	if got.Name != "new name" {
		t.Errorf("name: got %q, want %q", got.Name, "new name")
	}
}

func TestKBDelete(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	created, _ := kbRepo.Create(nil, userID, "to delete")

	req := authedRequest(t, http.MethodDelete, "/kbs/"+created.ID.String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204", w.Code)
	}
}

func TestKBTenantIsolation(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	user1, user2 := uuid.New(), uuid.New()

	kb1, _ := kbRepo.Create(nil, user1, "user1 kb")

	// user2 should get 404 trying to access user1's KB
	req := authedRequest(t, http.MethodGet, "/kbs/"+kb1.ID.String(), nil, user2)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant get: got %d, want 404", w.Code)
	}

	// user2 should get 404 trying to delete user1's KB
	req = authedRequest(t, http.MethodDelete, "/kbs/"+kb1.ID.String(), nil, user2)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant delete: got %d, want 404", w.Code)
	}
}
