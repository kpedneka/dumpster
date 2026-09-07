package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/relation"
	relationmem "github.com/kunalpednekar/dumpster/internal/relation/memory"
)

func seedRelationCandidates(t *testing.T, deps Deps, userID, kbID uuid.UUID) (chunkID, entityA, entityB uuid.UUID) {
	t.Helper()
	repo := deps.Relations.(*relationmem.Repository)
	chunkID, entityA, entityB = uuid.New(), uuid.New(), uuid.New()
	repo.SeedCandidates(userID, kbID, []relation.ChunkCandidates{
		{
			ChunkID: chunkID,
			Text:    "Naomi Reyes founded Thistlewood.",
			Pairs:   []relation.EdgePair{{EntityAID: entityA, TextA: "Naomi Reyes", EntityBID: entityB, TextB: "Thistlewood"}},
		},
	})
	return chunkID, entityA, entityB
}

func TestRelationExtract_ReviewsAndPersistsFoundRelation(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	chunkID, entityA, entityB := seedRelationCandidates(t, deps, userID, k.ID)
	deps.RelationExtractor = relation.NewExtractor(llmmock.NewGenerator("CHUNK: 1\nPAIR: 1\nRELATION: founded\n"))
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/relations", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got relationExtractionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ChunksReviewed != 1 || got.PairsReviewed != 1 {
		t.Errorf("got %+v, want 1 chunk, 1 pair reviewed", got)
	}
	if got.RelationsFound != 1 || got.NoneCount != 0 {
		t.Errorf("got %+v, want 1 relation found, 0 none", got)
	}

	repo := deps.Relations.(*relationmem.Repository)
	relType, ok := repo.Resolved(userID, chunkID, entityA, entityB)
	if !ok || relType != "founded" {
		t.Errorf("Resolved = (%q, %v), want (\"founded\", true)", relType, ok)
	}
}

func TestRelationExtract_NoneFound_MarksReviewedNotError(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	seedRelationCandidates(t, deps, userID, k.ID)
	deps.RelationExtractor = relation.NewExtractor(llmmock.NewGenerator("CHUNK: 1\nPAIR: 1\nRELATION: NONE\n"))
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/relations", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got relationExtractionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.NoneCount != 1 || got.RelationsFound != 0 {
		t.Errorf("got %+v, want 0 relations found, 1 none", got)
	}
}

func TestRelationExtract_NoCandidates_ReturnsZeroValueNotError(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	// No SeedCandidates call: nothing to review yet.
	deps.RelationExtractor = relation.NewExtractor(llmmock.NewErrorGenerator("should not be called"))
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/relations", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got relationExtractionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ChunksReviewed != 0 {
		t.Errorf("ChunksReviewed = %d, want 0", got.ChunksReviewed)
	}
}

func TestRelationExtract_SecondRunSkipsAlreadyResolvedPairs(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	seedRelationCandidates(t, deps, userID, k.ID)
	deps.RelationExtractor = relation.NewExtractor(llmmock.NewGenerator("CHUNK: 1\nPAIR: 1\nRELATION: founded\n"))
	router := NewRouter(deps)

	first := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/relations", nil, userID)
	router.ServeHTTP(httptest.NewRecorder(), first)

	// Second run: the pair is already resolved, so there's nothing left to
	// review -- even though the generator would error if called again.
	deps.RelationExtractor = relation.NewExtractor(llmmock.NewErrorGenerator("should not be called again"))
	router = NewRouter(deps)
	second := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/relations", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, second)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got relationExtractionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ChunksReviewed != 0 {
		t.Errorf("ChunksReviewed = %d, want 0 (already resolved on the first run)", got.ChunksReviewed)
	}
}

func TestRelation_KBNotFound_Returns404(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	userID := uuid.New()
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+uuid.New().String()+"/relations", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestRelationRoutes_NotRegistered_WhenDepsNil(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.Relations = nil
	deps.RelationExtractor = nil
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/relations", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404 (route unregistered when Relations/RelationExtractor is nil)", w.Code)
	}
}
