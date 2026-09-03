package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/theme"
	thememem "github.com/kunalpednekar/dumpster/internal/theme/memory"
)

func TestThemeRecompute_GeneratesAndPersists(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.ThemeSummarizer = theme.NewSummarizer(llmmock.NewGenerator("LABEL: Distributed Systems\nSUMMARY: These entities relate to building resilient software."))
	repo := deps.Themes.(*thememem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	repo.SeedCommunityMembers(userID, k.ID, []theme.CommunityMembers{
		{CommunityID: 0, Entities: []theme.EntityRef{{Text: "Kubernetes", Type: "concept"}, {Text: "etcd", Type: "concept"}}},
	})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/themes", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got themeResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ComputedAt == nil {
		t.Fatal("ComputedAt should be set after a successful recompute")
	}
	if len(got.Themes) != 1 {
		t.Fatalf("got %d themes, want 1", len(got.Themes))
	}
	if got.Themes[0].Label != "Distributed Systems" {
		t.Errorf("Label = %q, want %q", got.Themes[0].Label, "Distributed Systems")
	}
	if got.Themes[0].EntityCount != 2 {
		t.Errorf("EntityCount = %d, want 2", got.Themes[0].EntityCount)
	}
}

func TestThemeRecompute_NoCommunities_Returns422(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	// No SeedCommunityMembers call: communities have never been computed.
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/themes", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d, want 422, body: %s", w.Code, w.Body.String())
	}
}

func TestThemeRecompute_SummarizerError_Returns500(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.ThemeSummarizer = theme.NewSummarizer(llmmock.NewErrorGenerator("rate limited"))
	repo := deps.Themes.(*thememem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	repo.SeedCommunityMembers(userID, k.ID, []theme.CommunityMembers{
		{CommunityID: 0, Entities: []theme.EntityRef{{Text: "X", Type: "concept"}}},
	})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/themes", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500, body: %s", w.Code, w.Body.String())
	}
}

func TestThemeGet_NoResultYet_ReturnsZeroValue(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/themes", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got themeResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ComputedAt != nil {
		t.Errorf("ComputedAt = %v, want nil before any recompute has run", got.ComputedAt)
	}
	if got.Themes == nil || len(got.Themes) != 0 {
		t.Errorf("Themes = %v, want an empty (not nil) slice", got.Themes)
	}
}

func TestThemeGet_AfterRecompute_ReturnsThemes(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.ThemeSummarizer = theme.NewSummarizer(llmmock.NewGenerator("LABEL: A Theme\nSUMMARY: A summary."))
	repo := deps.Themes.(*thememem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	repo.SeedCommunityMembers(userID, k.ID, []theme.CommunityMembers{
		{CommunityID: 0, Entities: []theme.EntityRef{{Text: "X", Type: "concept"}}},
	})
	router := NewRouter(deps)

	postReq := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/themes", nil, userID)
	router.ServeHTTP(httptest.NewRecorder(), postReq)

	getReq := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/themes", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, getReq)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got themeResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ComputedAt == nil || len(got.Themes) != 1 {
		t.Errorf("GET after recompute = %+v, want ComputedAt set and 1 theme", got)
	}
}

func TestTheme_KBNotFound_Returns404(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	postReq := authedRequest(t, deps, http.MethodPost, "/kbs/"+uuid.New().String()+"/themes", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, postReq)
	if w.Code != http.StatusNotFound {
		t.Errorf("POST unknown KB: got %d, want 404", w.Code)
	}

	getReq := authedRequest(t, deps, http.MethodGet, "/kbs/"+uuid.New().String()+"/themes", nil, userID)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, getReq)
	if w2.Code != http.StatusNotFound {
		t.Errorf("GET unknown KB: got %d, want 404", w2.Code)
	}
}

func TestTheme_TenantIsolation(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	owner, other := uuid.New(), uuid.New()
	k, _ := kbRepo.Create(context.TODO(), owner, "kb1")

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/themes", nil, other)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant recompute: got %d, want 404", w.Code)
	}
}

func TestTheme_RoutesNotRegisteredWhenDepsAreNil(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.Themes = nil
	deps.ThemeSummarizer = nil
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/themes", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404 (route unregistered when Themes/ThemeSummarizer is nil)", w.Code)
	}
}
