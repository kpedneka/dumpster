package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/community"
	communitymem "github.com/kunalpednekar/dumpster/internal/community/memory"
)

// seedTriangleBridgeGraph seeds kbID with two triangles (A,B,C and D,E,F,
// each fully connected internally with weight 1.0) joined by one much
// weaker bridge edge (C-D, weight 0.1) — the standard fixture for
// verifying Louvain finds two separate communities rather than merging
// everything into one.
func seedTriangleBridgeGraph(repo *communitymem.Repository, userID, kbID uuid.UUID) (a, b, c, d, e, f uuid.UUID) {
	a, b, c, d, e, f = uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	nodes := []uuid.UUID{a, b, c, d, e, f}
	edges := []community.WeightedEdge{
		{A: a, B: b, Weight: 1.0},
		{A: b, B: c, Weight: 1.0},
		{A: a, B: c, Weight: 1.0},
		{A: d, B: e, Weight: 1.0},
		{A: e, B: f, Weight: 1.0},
		{A: d, B: f, Weight: 1.0},
		{A: c, B: d, Weight: 0.1},
	}
	repo.SeedGraph(userID, kbID, nodes, edges)
	return
}

func TestCommunityRecompute_ComputesAndPersists(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	repo := deps.Communities.(*communitymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	seedTriangleBridgeGraph(repo, userID, k.ID)
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/communities", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got communityResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.NodeCount != 6 {
		t.Errorf("NodeCount = %d, want 6", got.NodeCount)
	}
	if got.EdgeCount != 7 {
		t.Errorf("EdgeCount = %d, want 7", got.EdgeCount)
	}
	if got.CommunityCount != 2 {
		t.Errorf("CommunityCount = %d, want 2 (two triangles joined by a weak bridge)", got.CommunityCount)
	}
	if got.ComputedAt == nil {
		t.Error("ComputedAt should be set after a successful recompute")
	}
}

func TestCommunityRecompute_TooManyEntities_Returns422(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.MaxCommunityGraphEntities = 2
	repo := deps.Communities.(*communitymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	seedTriangleBridgeGraph(repo, userID, k.ID) // 6 nodes, over the cap of 2
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/communities", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d, want 422, body: %s", w.Code, w.Body.String())
	}

	// The graph must never have been built/computed over — GetResult should
	// still report no result, proving the pre-check short-circuited before
	// any computation was attempted.
	if _, err := repo.GetResult(context.TODO(), userID, k.ID); err != community.ErrNoResult {
		t.Errorf("GetResult after a rejected recompute: got err %v, want ErrNoResult", err)
	}
}

func TestCommunityGet_NoResultYet_ReturnsZeroValue(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/communities", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got communityResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ComputedAt != nil {
		t.Errorf("ComputedAt = %v, want nil before any recompute has run", got.ComputedAt)
	}
}

func TestCommunityGet_AfterRecompute_ReturnsSummary(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	repo := deps.Communities.(*communitymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	seedTriangleBridgeGraph(repo, userID, k.ID)
	router := NewRouter(deps)

	postReq := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/communities", nil, userID)
	router.ServeHTTP(httptest.NewRecorder(), postReq)

	getReq := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/communities", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, getReq)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got communityResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ComputedAt == nil || got.CommunityCount != 2 {
		t.Errorf("GET after recompute = %+v, want ComputedAt set and CommunityCount 2", got)
	}
}

func TestCommunity_KBNotFound_Returns404(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	postReq := authedRequest(t, deps, http.MethodPost, "/kbs/"+uuid.New().String()+"/communities", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, postReq)
	if w.Code != http.StatusNotFound {
		t.Errorf("POST unknown KB: got %d, want 404", w.Code)
	}

	getReq := authedRequest(t, deps, http.MethodGet, "/kbs/"+uuid.New().String()+"/communities", nil, userID)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, getReq)
	if w2.Code != http.StatusNotFound {
		t.Errorf("GET unknown KB: got %d, want 404", w2.Code)
	}
}

func TestCommunity_TenantIsolation(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	owner, other := uuid.New(), uuid.New()
	k, _ := kbRepo.Create(context.TODO(), owner, "kb1")

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/communities", nil, other)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant recompute: got %d, want 404", w.Code)
	}
}
