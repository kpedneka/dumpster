package worker_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	canonicalmem "github.com/kunalpednekar/dumpster/internal/canonical/memory"
	"github.com/kunalpednekar/dumpster/internal/crosslink"
	crosslinkmem "github.com/kunalpednekar/dumpster/internal/crosslink/memory"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	entitymem "github.com/kunalpednekar/dumpster/internal/entity/memory"
	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

func TestCanonicalizationHandler_Handle_LinksMentionsToCanonicalEntity(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()

	job, userID := seedEdgeJob(t, docs, entities, 1, 2) // reuses graphedge's seed helper: 1 chunk, 2 "Ada Lovelace" mentions
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewCanonicalizationHandler(docs, entities, canonicalRepo)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	mentions, err := entities.ListByDocument(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mentions) != 2 {
		t.Fatalf("expected 2 seeded mentions, got %d", len(mentions))
	}
	if mentions[0].CanonicalEntityID == nil || mentions[1].CanonicalEntityID == nil {
		t.Fatal("expected both mentions to be linked to a canonical entity")
	}
	if *mentions[0].CanonicalEntityID != *mentions[1].CanonicalEntityID {
		t.Error("both mentions share the same text/type and should resolve to the same canonical entity")
	}

	ce, err := canonicalRepo.Get(ctx, userID, *mentions[0].CanonicalEntityID)
	if err != nil {
		t.Fatal(err)
	}
	if ce.MentionCount != 2 {
		t.Errorf("MentionCount: got %d, want 2", ce.MentionCount)
	}
	if ce.DocumentCount != 1 {
		t.Errorf("DocumentCount: got %d, want 1", ce.DocumentCount)
	}
}

func TestCanonicalizationHandler_Handle_RerunDoesNotDoubleCount(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()

	job, userID := seedEdgeJob(t, docs, entities, 1, 1)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewCanonicalizationHandler(docs, entities, canonicalRepo)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("second Handle: %v", err)
	}

	mentions, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	ce, err := canonicalRepo.Get(ctx, userID, *mentions[0].CanonicalEntityID)
	if err != nil {
		t.Fatal(err)
	}
	if ce.MentionCount != 1 {
		t.Errorf("re-run (already-linked mention) must not double-count: MentionCount got %d, want 1", ce.MentionCount)
	}
}

func TestCanonicalizationHandler_Handle_ZeroMentions_NoOp(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()

	job, userID := seedEdgeJob(t, docs, entities, 1, 0)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewCanonicalizationHandler(docs, entities, canonicalRepo)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle with zero mentions: %v", err)
	}
}

func TestCanonicalizationHandler_Handle_UnknownDocument(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()

	h := worker.NewCanonicalizationHandler(docs, entities, canonicalRepo)
	userID := uuid.New()
	job := &queue.Job{ID: uuid.New(), Type: queue.JobTypeCanonicalization, DocumentID: uuid.New(), UserID: userID}

	if err := h.Handle(auth.WithUserID(context.Background(), userID), job); err == nil {
		t.Fatal("expected error for unknown document")
	}
}

func TestCanonicalizationHandler_OnFailed_DoesNotErrorOrPanic(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	h := worker.NewCanonicalizationHandler(docs, entities, canonicalRepo)

	job := &queue.Job{ID: uuid.New(), Type: queue.JobTypeCanonicalization, DocumentID: uuid.New(), UserID: uuid.New()}
	h.OnFailed(context.Background(), job) // must not panic
}

func TestCanonicalizationHandler_Handle_CrossLinkConfirmsCandidate(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	crosslinkRepo := crosslinkmem.New()

	job, userID := seedEdgeJob(t, docs, entities, 1, 1)
	ctx := auth.WithUserID(context.Background(), userID)
	doc, err := docs.Get(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}

	entityAID, entityBID := uuid.New(), uuid.New()
	crosslinkRepo.SeedCandidates(userID, doc.KBID, []crosslink.Candidate{
		{
			EntityAID: entityAID, EntityAText: "Rosalind Kade", ChunkAID: uuid.New(), ChunkAText: "Rosalind Kade led the expedition.",
			EntityBID: entityBID, EntityBText: "the Harbor Beacon Leveler", ChunkBID: uuid.New(), ChunkBText: "The Harbor Beacon Leveler was her final instrument.",
		},
	})
	extractor := crosslink.NewExtractor(llmmock.NewGenerator("CANDIDATE: 1\nRELATION: designed"))

	h := worker.NewCanonicalizationHandler(docs, entities, canonicalRepo).
		WithCrossLink(crosslinkRepo, extractor, 0)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	rel, ok := crosslinkRepo.Resolved(userID, entityAID, entityBID)
	if !ok {
		t.Fatal("expected the seeded candidate to be resolved")
	}
	if rel != "designed" {
		t.Errorf("RelationType: got %q, want %q", rel, "designed")
	}
}

func TestCanonicalizationHandler_Handle_CrossLinkZeroCandidates_NoOp(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	crosslinkRepo := crosslinkmem.New()

	job, userID := seedEdgeJob(t, docs, entities, 1, 1)
	ctx := auth.WithUserID(context.Background(), userID)

	extractor := crosslink.NewExtractor(llmmock.NewGenerator("should never be called"))
	h := worker.NewCanonicalizationHandler(docs, entities, canonicalRepo).
		WithCrossLink(crosslinkRepo, extractor, 0)

	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle with no candidates: %v", err)
	}
}

func TestCanonicalizationHandler_Handle_CrossLinkExtractError_Propagates(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	crosslinkRepo := crosslinkmem.New()

	job, userID := seedEdgeJob(t, docs, entities, 1, 1)
	ctx := auth.WithUserID(context.Background(), userID)
	doc, err := docs.Get(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}

	crosslinkRepo.SeedCandidates(userID, doc.KBID, []crosslink.Candidate{
		{EntityAID: uuid.New(), EntityAText: "A", ChunkAID: uuid.New(), ChunkAText: "a",
			EntityBID: uuid.New(), EntityBText: "B", ChunkBID: uuid.New(), ChunkBText: "b"},
	})
	extractor := crosslink.NewExtractor(llmmock.NewErrorGenerator("generator unavailable"))

	h := worker.NewCanonicalizationHandler(docs, entities, canonicalRepo).
		WithCrossLink(crosslinkRepo, extractor, 0)
	if err := h.Handle(ctx, job); err == nil {
		t.Fatal("expected an error when the extractor's generator fails")
	}
}

func TestCanonicalizationHandler_ImplementsHandler(t *testing.T) {
	var _ worker.Handler = (*worker.CanonicalizationHandler)(nil)
}
