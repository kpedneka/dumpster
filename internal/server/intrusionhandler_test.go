package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/intrusion"
	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/theme"
	thememem "github.com/kunalpednekar/dumpster/internal/theme/memory"
)

func seedTwoQualifyingCommunities(t *testing.T, deps Deps, userID, kbID uuid.UUID) {
	t.Helper()
	repo := deps.Themes.(*thememem.Repository)
	repo.SeedCommunityMembers(userID, kbID, []theme.CommunityMembers{
		{CommunityID: 1, Entities: []theme.EntityRef{{Text: "loop"}, {Text: "main"}, {Text: "i"}}},
		{CommunityID: 2, Entities: []theme.EntityRef{{Text: "Hogwarts"}, {Text: "wand"}, {Text: "spell"}}},
	})
}

func TestIntrusionRecompute_RunsScoresAndPersists(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	seedTwoQualifyingCommunities(t, deps, userID, k.ID)

	// The exact judge answers don't matter for this test -- deps.Themes'
	// seeded data determines which entity is actually the intruder for
	// each set, and buildSets is deterministic, so this response has to be
	// discovered rather than hardcoded. Compute it the same way Run does.
	deps.IntrusionTester = intrusion.NewTester(llmmock.NewGenerator("SET: 1\nINTRUDER: wand\nSET: 2\nINTRUDER: loop\n"))
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/intrusion-test", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got intrusionResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ComputedAt == nil {
		t.Fatal("ComputedAt should be set after a successful recompute")
	}
	if got.TestedCount != 2 {
		t.Fatalf("TestedCount = %d, want 2", got.TestedCount)
	}
	if len(got.Communities) != 2 {
		t.Fatalf("len(Communities) = %d, want 2", len(got.Communities))
	}

	// Verify the persisted result is actually readable back via GET.
	getReq := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/intrusion-test", nil, userID)
	getW := httptest.NewRecorder()
	router.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GET status: got %d, want 200, body: %s", getW.Code, getW.Body.String())
	}
	var gotAfterGet intrusionResultResponse
	if err := json.Unmarshal(getW.Body.Bytes(), &gotAfterGet); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if gotAfterGet.TestedCount != 2 || gotAfterGet.Score != got.Score {
		t.Errorf("GET result = %+v, want it to match the persisted recompute result %+v", gotAfterGet, got)
	}
}

func TestIntrusionGet_NoResultYet_ReturnsZeroValue(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/intrusion-test", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got intrusionResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ComputedAt != nil {
		t.Errorf("ComputedAt = %v, want nil (never run)", got.ComputedAt)
	}
	if len(got.Communities) != 0 {
		t.Errorf("Communities = %+v, want empty", got.Communities)
	}
}

func TestIntrusionRecompute_NotEnoughCommunities_StillReturns200(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	repo := deps.Themes.(*thememem.Repository)
	// Only one qualifying community -- nothing to draw an intruder from.
	repo.SeedCommunityMembers(userID, k.ID, []theme.CommunityMembers{
		{CommunityID: 1, Entities: []theme.EntityRef{{Text: "loop"}, {Text: "main"}, {Text: "i"}}},
	})
	deps.IntrusionTester = intrusion.NewTester(llmmock.NewGenerator("should not be called"))
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/intrusion-test", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got intrusionResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.TestedCount != 0 {
		t.Errorf("TestedCount = %d, want 0 (not enough communities to test)", got.TestedCount)
	}
	if got.ComputedAt == nil {
		t.Error("ComputedAt should still be set, even with nothing tested -- this was a real (if empty) run")
	}
}

func TestIntrusionRecompute_NoCommunitiesComputedYet_Returns422(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	// No SeedCommunityMembers call: communities have never been computed.
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/intrusion-test", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d, want 422", w.Code)
	}
}

func TestIntrusion_KBNotFound_Returns404(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	userID := uuid.New()
	router := NewRouter(deps)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := authedRequest(t, deps, method, "/kbs/"+uuid.New().String()+"/intrusion-test", nil, userID)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s status: got %d, want 404", method, w.Code)
		}
	}
}

func TestIntrusionRoutes_NotRegistered_WhenDepsNil(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.Intrusion = nil
	deps.IntrusionTester = nil
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/intrusion-test", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404 (route unregistered when Intrusion/IntrusionTester is nil)", w.Code)
	}
}
