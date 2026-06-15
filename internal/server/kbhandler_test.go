package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/kb"
	kbmem "github.com/kunalpednekar/dumpster/internal/kb/memory"
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
	_, _ = kbRepo.Create(context.TODO(), userID, "alpha")
	_, _ = kbRepo.Create(context.TODO(), userID, "beta")
	_, _ = kbRepo.Create(context.TODO(), uuid.New(), "other")

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

	created, _ := kbRepo.Create(context.TODO(), userID, "test kb")

	req := authedRequest(t, http.MethodGet, "/kbs/"+created.ID.String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var got kb.KnowledgeBase
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
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

	created, _ := kbRepo.Create(context.TODO(), userID, "old name")

	req := authedRequest(t, http.MethodPatch, "/kbs/"+created.ID.String(),
		strings.NewReader(`{"name":"new name"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", w.Code, w.Body)
	}

	var got kb.KnowledgeBase
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "new name" {
		t.Errorf("name: got %q, want %q", got.Name, "new name")
	}
}

func TestKBDelete(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	created, _ := kbRepo.Create(context.TODO(), userID, "to delete")

	req := authedRequest(t, http.MethodDelete, "/kbs/"+created.ID.String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204", w.Code)
	}
}

// --- Finding 5: error discrimination (ErrNotFound → 404, other errors → 500) ---

// stubKBRepo delegates to an underlying memory repo but overrides specific
// methods to return a configurable error, enabling storage-error testing.
type stubKBRepo struct {
	*kbmem.Repository
	getErr    error
	renameErr error
	deleteErr error
}

func (r *stubKBRepo) Get(ctx context.Context, userID, id uuid.UUID) (*kb.KnowledgeBase, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.Repository.Get(ctx, userID, id)
}

func (r *stubKBRepo) Rename(ctx context.Context, userID, id uuid.UUID, name string) (*kb.KnowledgeBase, error) {
	if r.renameErr != nil {
		return nil, r.renameErr
	}
	return r.Repository.Rename(ctx, userID, id, name)
}

func (r *stubKBRepo) Delete(ctx context.Context, userID, id uuid.UUID) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	return r.Repository.Delete(ctx, userID, id)
}

func TestKBGet_StorageError_Returns500(t *testing.T) {
	stub := &stubKBRepo{Repository: kbmem.New(), getErr: errors.New("connection refused")}
	deps, _, docRepo, obj, pub := defaultDeps()
	deps.KBs = stub
	router := NewRouter(testDeps(stub, docRepo, obj, pub))
	userID := uuid.New()

	req := authedRequest(t, http.MethodGet, "/kbs/"+uuid.New().String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("storage error: got %d, want 500", w.Code)
	}
}

func TestKBGet_ErrNotFound_Returns404(t *testing.T) {
	stub := &stubKBRepo{Repository: kbmem.New(), getErr: kb.ErrNotFound}
	deps, _, docRepo, obj, pub := defaultDeps()
	router := NewRouter(testDeps(stub, docRepo, obj, pub))
	userID := uuid.New()
	_ = deps

	req := authedRequest(t, http.MethodGet, "/kbs/"+uuid.New().String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("ErrNotFound: got %d, want 404", w.Code)
	}
}

func TestKBRename_StorageError_Returns500(t *testing.T) {
	base := kbmem.New()
	userID := uuid.New()
	created, _ := base.Create(context.TODO(), userID, "kb")
	stub := &stubKBRepo{Repository: base, renameErr: errors.New("db timeout")}
	deps, _, docRepo, obj, pub := defaultDeps()
	router := NewRouter(testDeps(stub, docRepo, obj, pub))
	_ = deps

	req := authedRequest(t, http.MethodPatch, "/kbs/"+created.ID.String(),
		strings.NewReader(`{"name":"new"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("storage error: got %d, want 500", w.Code)
	}
}

func TestKBDelete_StorageError_Returns500(t *testing.T) {
	base := kbmem.New()
	userID := uuid.New()
	created, _ := base.Create(context.TODO(), userID, "kb")
	stub := &stubKBRepo{Repository: base, deleteErr: errors.New("db timeout")}
	deps, _, docRepo, obj, pub := defaultDeps()
	router := NewRouter(testDeps(stub, docRepo, obj, pub))
	_ = deps

	req := authedRequest(t, http.MethodDelete, "/kbs/"+created.ID.String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("storage error: got %d, want 500", w.Code)
	}
}

func TestKBTenantIsolation(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	user1, user2 := uuid.New(), uuid.New()

	kb1, _ := kbRepo.Create(context.TODO(), user1, "user1 kb")

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
