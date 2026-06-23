package worker_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/entity"
	entitymem "github.com/kunalpednekar/dumpster/internal/entity/memory"
	graphedgemem "github.com/kunalpednekar/dumpster/internal/graphedge/memory"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

// seedEdgeJob creates a document (at StatusIndexed) and a set of entity rows
// distributed across numChunks chunks (n per chunk), returning a matching
// edge-extraction Job. It is the edge-handler analogue of seedEntityJob.
func seedEdgeJob(t *testing.T, docs *docmem.Repository, entities *entitymem.Repository, numChunks, entitiesPerChunk int) (*queue.Job, uuid.UUID) {
	t.Helper()
	userID := uuid.New()
	ctx := auth.WithUserID(context.Background(), userID)

	doc, err := docs.Create(ctx, &document.Document{
		KBID:        uuid.New(),
		UserID:      userID,
		Filename:    "test.txt",
		S3Key:       "uploads/" + uuid.New().String(),
		ContentType: "text/plain",
		Status:      document.StatusIndexed,
	})
	if err != nil {
		t.Fatal(err)
	}

	for c := range numChunks {
		chunkID := uuid.New()
		es := make([]*entity.Entity, entitiesPerChunk)
		for i := range es {
			es[i] = &entity.Entity{
				DocumentID: doc.ID,
				KBID:       doc.KBID,
				ChunkID:    chunkID,
				UserID:     userID,
				Type:       entity.Type("person"),
				Text:       "Ada Lovelace",
				Start:      i * 10,
				End:        i*10 + 5,
				Score:      0.9,
			}
		}
		if entitiesPerChunk > 0 {
			if err := entities.BulkCreate(ctx, es); err != nil {
				t.Fatalf("seedEdgeJob chunk %d: %v", c, err)
			}
		}
	}

	return &queue.Job{
		ID:          uuid.New(),
		Type:        queue.JobTypeEdgeExtraction,
		DocumentID:  doc.ID,
		UserID:      userID,
		Attempts:    0,
		MaxAttempts: 3,
	}, userID
}

func TestEdgeHandler_Handle_PersistsEdgesForCooccurringEntities(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	edges := graphedgemem.New()

	// 3 entities in 1 chunk → C(3,2)=3 edges.
	job, userID := seedEdgeJob(t, docs, entities, 1, 3)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewEdgeHandler(docs, entities, edges)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Get any entity to check edges on.
	allEntities, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	if len(allEntities) != 3 {
		t.Fatalf("expected 3 seeded entities, got %d", len(allEntities))
	}

	edgesForE0, err := edges.ListByEntity(ctx, userID, allEntities[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	// allEntities[0] co-occurs with the other 2 → 2 edges.
	if len(edgesForE0) != 2 {
		t.Errorf("edges for entity 0: got %d, want 2", len(edgesForE0))
	}
	// Verify canonical ordering and user scoping on sampled edge.
	for _, e := range edgesForE0 {
		if e.UserID != userID {
			t.Errorf("edge UserID: got %v, want %v", e.UserID, userID)
		}
		if e.EntityAID.String() >= e.EntityBID.String() {
			t.Errorf("edge not canonically ordered: A=%v B=%v", e.EntityAID, e.EntityBID)
		}
	}
}

func TestEdgeHandler_Handle_EdgesAreScopedToChunk(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	edges := graphedgemem.New()

	// 2 chunks, 2 entities each → C(2,2)×2 chunks = 1+1 = 2 edges total.
	job, userID := seedEdgeJob(t, docs, entities, 2, 2)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewEdgeHandler(docs, entities, edges)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	allEntities, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	var totalEdges int
	seen := map[uuid.UUID]bool{}
	for _, e := range allEntities {
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		got, _ := edges.ListByEntity(ctx, userID, e.ID)
		totalEdges += len(got)
	}
	// Each entity appears on exactly 1 edge (its pair in its chunk), so total
	// deduplicated edge count seen across all entities = 2 edges × 2 endpoints = 4
	// list entries, but unique edge rows = 2. We verify via entity 0's count.
	allEdgesForAny, _ := edges.ListByEntity(ctx, userID, allEntities[0].ID)
	if len(allEdgesForAny) != 1 {
		t.Errorf("entity[0] should be in exactly 1 edge (its chunk-pair), got %d", len(allEdgesForAny))
	}
}

func TestEdgeHandler_Handle_RerunIsIdempotent(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	edges := graphedgemem.New()

	job, userID := seedEdgeJob(t, docs, entities, 1, 3)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewEdgeHandler(docs, entities, edges)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("first Handle: %v", err)
	}

	allEntities, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	first, _ := edges.ListByEntity(ctx, userID, allEntities[0].ID)

	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	second, _ := edges.ListByEntity(ctx, userID, allEntities[0].ID)

	if len(first) != len(second) {
		t.Errorf("re-run: edge count changed from %d to %d", len(first), len(second))
	}
}

func TestEdgeHandler_Handle_ZeroEntities_NoOp(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	edges := graphedgemem.New()

	// 0 entities → no edges, no error.
	job, userID := seedEdgeJob(t, docs, entities, 1, 0)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewEdgeHandler(docs, entities, edges)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle with zero entities: %v", err)
	}
}

func TestEdgeHandler_Handle_OneEntityPerChunk_NoEdges(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	edges := graphedgemem.New()

	// 2 chunks with 1 entity each → no pairs within any chunk, 0 edges.
	job, userID := seedEdgeJob(t, docs, entities, 2, 1)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewEdgeHandler(docs, entities, edges)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	allEntities, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	for _, e := range allEntities {
		got, _ := edges.ListByEntity(ctx, userID, e.ID)
		if len(got) != 0 {
			t.Errorf("entity %v: expected 0 edges (no co-occurring partner in same chunk), got %d", e.ID, len(got))
		}
	}
}

func TestEdgeHandler_Handle_UnknownDocument(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	edges := graphedgemem.New()

	h := worker.NewEdgeHandler(docs, entities, edges)
	userID := uuid.New()
	job := &queue.Job{ID: uuid.New(), Type: queue.JobTypeEdgeExtraction, DocumentID: uuid.New(), UserID: userID}

	if err := h.Handle(auth.WithUserID(context.Background(), userID), job); err == nil {
		t.Fatal("expected error for unknown document")
	}
}

func TestEdgeHandler_OnFailed_DoesNotErrorOrPanic(t *testing.T) {
	docs := docmem.New()
	entities := entitymem.New()
	edges := graphedgemem.New()
	h := worker.NewEdgeHandler(docs, entities, edges)

	job := &queue.Job{ID: uuid.New(), Type: queue.JobTypeEdgeExtraction, DocumentID: uuid.New(), UserID: uuid.New()}
	h.OnFailed(context.Background(), job) // must not panic
}

func TestEdgeHandler_ImplementsHandler(t *testing.T) {
	var _ worker.Handler = (*worker.EdgeHandler)(nil)
}
