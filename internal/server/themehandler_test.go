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
	deps.ThemeSummarizer = theme.NewSummarizer(llmmock.NewGenerator("COMMUNITY: 0\nLABEL: Distributed Systems\nSUMMARY: These entities relate to building resilient software."))
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
	if got.Note != "" {
		t.Errorf("Note = %q, want empty (every community became a theme, nothing more to hint at)", got.Note)
	}
}

func TestThemeRecompute_MoreCommunitiesThanThemes_IncludesNoteWithTheModelsReason(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.ThemeSummarizer = theme.NewSummarizer(llmmock.NewGenerator(
		"COMMUNITY: 0\nLABEL: Distributed Systems\nSUMMARY: These entities relate to building resilient software.\nNOTE: It's mentioned far more often than the other groups reviewed.",
	))
	repo := deps.Themes.(*thememem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	// Three communities exist, but the mock LLM only returns a theme for
	// one of them -- the note should reflect that more community
	// structure exists than what's being surfaced.
	repo.SeedCommunityMembers(userID, k.ID, []theme.CommunityMembers{
		{CommunityID: 0, Entities: []theme.EntityRef{{Text: "Kubernetes", Type: "concept"}}},
		{CommunityID: 1, Entities: []theme.EntityRef{{Text: "X", Type: "concept"}}},
		{CommunityID: 2, Entities: []theme.EntityRef{{Text: "Y", Type: "concept"}}},
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
	// The note is the model's own sentence verbatim, not wrapped in any
	// further templated framing -- see moreThemesNote's doc comment for
	// why (a wrapped clause produced a garbled, redundant sentence).
	want := "It's mentioned far more often than the other groups reviewed."
	if got.Note != want {
		t.Errorf("Note = %q, want %q", got.Note, want)
	}
}

func TestThemeGet_NeverIncludesNote(t *testing.T) {
	// The note is a one-time artifact of a specific recompute response --
	// not persisted -- so GET (including immediately after a recompute
	// that did include one) should never surface it.
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.ThemeSummarizer = theme.NewSummarizer(llmmock.NewGenerator(
		"COMMUNITY: 0\nLABEL: Distributed Systems\nSUMMARY: s.\nNOTE: reason",
	))
	repo := deps.Themes.(*thememem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	repo.SeedCommunityMembers(userID, k.ID, []theme.CommunityMembers{
		{CommunityID: 0, Entities: []theme.EntityRef{{Text: "X", Type: "concept"}}},
		{CommunityID: 1, Entities: []theme.EntityRef{{Text: "Y", Type: "concept"}}},
	})
	router := NewRouter(deps)

	postReq := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/themes", nil, userID)
	router.ServeHTTP(httptest.NewRecorder(), postReq)

	getReq := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/themes", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, getReq)

	var got themeResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Note != "" {
		t.Errorf("Note = %q, want empty on GET even though the recompute that produced these themes had one", got.Note)
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
	deps.ThemeSummarizer = theme.NewSummarizer(llmmock.NewGenerator("COMMUNITY: 0\nLABEL: A Theme\nSUMMARY: A summary."))
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
