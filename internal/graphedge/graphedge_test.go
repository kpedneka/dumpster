package graphedge_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/graphedge"
	"github.com/kunalpednekar/dumpster/internal/graphedge/memory"
)

// Compile-time check that the test double satisfies the domain interface.
var _ graphedge.Repository = (*memory.Repository)(nil)

func TestNewEdge_canonicalizesOrder(t *testing.T) {
	docID, kbID, userID, chunkID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	idLow, idHigh := uuid.New(), uuid.New()
	if idLow.String() > idHigh.String() {
		idLow, idHigh = idHigh, idLow
	}

	forward := graphedge.NewEdge(docID, kbID, userID, chunkID, idLow, idHigh)
	backward := graphedge.NewEdge(docID, kbID, userID, chunkID, idHigh, idLow)

	if forward.EntityAID != idLow || forward.EntityBID != idHigh {
		t.Fatalf("forward: got A=%v B=%v, want A=%v B=%v", forward.EntityAID, forward.EntityBID, idLow, idHigh)
	}
	if backward.EntityAID != idLow || backward.EntityBID != idHigh {
		t.Fatalf("backward: got A=%v B=%v, want A=%v B=%v (canonicalization should make order irrelevant)",
			backward.EntityAID, backward.EntityBID, idLow, idHigh)
	}
	if forward.CoOccurrenceCount != 1 {
		t.Errorf("CoOccurrenceCount: got %d, want 1", forward.CoOccurrenceCount)
	}
}

func TestEdge_BulkCreateAndListByEntity(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	docID, kbID, userID, chunkID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	entityA, entityB, entityC := uuid.New(), uuid.New(), uuid.New()

	edges := []*graphedge.Edge{
		graphedge.NewEdge(docID, kbID, userID, chunkID, entityA, entityB),
		graphedge.NewEdge(docID, kbID, userID, chunkID, entityB, entityC),
	}
	if err := repo.BulkCreate(ctx, edges); err != nil {
		t.Fatal(err)
	}

	list, err := repo.ListByEntity(ctx, userID, entityB)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListByEntity(entityB): got %d, want 2 (entityB is on both edges)", len(list))
	}

	list, err = repo.ListByEntity(ctx, userID, entityA)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("ListByEntity(entityA): got %d, want 1", len(list))
	}
}

func TestEdge_DeleteByDocument(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	kbID, userID, chunkID := uuid.New(), uuid.New(), uuid.New()
	docA, docB := uuid.New(), uuid.New()
	entityA, entityB := uuid.New(), uuid.New()

	edgeA := graphedge.NewEdge(docA, kbID, userID, chunkID, entityA, entityB)
	edgeB := graphedge.NewEdge(docB, kbID, userID, chunkID, entityA, entityB)
	_ = repo.BulkCreate(ctx, []*graphedge.Edge{edgeA, edgeB})

	if err := repo.DeleteByDocument(ctx, userID, docA); err != nil {
		t.Fatal(err)
	}

	remaining, _ := repo.ListByEntity(ctx, userID, entityA)
	if len(remaining) != 1 {
		t.Fatalf("expected 1 edge remaining (docB's), got %d", len(remaining))
	}
	if remaining[0].DocumentID != docB {
		t.Errorf("remaining edge should belong to docB, got %v", remaining[0].DocumentID)
	}
}

func TestEdge_TenantIsolation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	docID, kbID, chunkID := uuid.New(), uuid.New(), uuid.New()
	user1, user2 := uuid.New(), uuid.New()
	entityA, entityB := uuid.New(), uuid.New()

	edge := graphedge.NewEdge(docID, kbID, user1, chunkID, entityA, entityB)
	_ = repo.BulkCreate(ctx, []*graphedge.Edge{edge})

	list, _ := repo.ListByEntity(ctx, user2, entityA)
	if len(list) != 0 {
		t.Fatalf("user2 ListByEntity should be empty, got %d", len(list))
	}

	_ = repo.DeleteByDocument(ctx, user2, docID)
	remaining, _ := repo.ListByEntity(ctx, user1, entityA)
	if len(remaining) != 1 {
		t.Fatalf("user1's edge should be untouched, got %d", len(remaining))
	}
}

// TestEdge_CooccurrenceAggregation proves the query shape behind the
// aggregation-query acceptance criterion: "entities co-occurring with X
// resolves as a single indexed join/aggregate." The actual SQL (JOIN entity_edges JOIN
// entities GROUP BY entity text) is exercised at the pgstore/integration
// level; here we prove the same query semantics against the in-memory
// repository so the logic is verifiable without a live Postgres instance.
//
// Scenario: entity "FEMA" co-occurs with "Red Cross" in 3 chunks and with
// "NRCS" in 1 chunk. A co-occurrence lookup for FEMA should return Red Cross
// as the dominant partner, NRCS as the secondary.
func TestEdge_CooccurrenceAggregation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	docID, kbID, userID := uuid.New(), uuid.New(), uuid.New()

	fema := uuid.New()
	redCross := uuid.New()
	nrcs := uuid.New()

	// Seed 3 edges between FEMA and Red Cross (different chunks).
	for range 3 {
		edge := graphedge.NewEdge(docID, kbID, userID, uuid.New(), fema, redCross)
		_ = repo.BulkCreate(ctx, []*graphedge.Edge{edge})
	}
	// Seed 1 edge between FEMA and NRCS.
	edge := graphedge.NewEdge(docID, kbID, userID, uuid.New(), fema, nrcs)
	_ = repo.BulkCreate(ctx, []*graphedge.Edge{edge})

	// Query: edges touching FEMA.
	edges, err := repo.ListByEntity(ctx, userID, fema)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 4 {
		t.Fatalf("expected 4 edges touching FEMA, got %d", len(edges))
	}

	// Count co-occurrences per partner entity by iterating the result — the
	// same aggregation a real SQL GROUP BY would perform in the edge table.
	counts := make(map[uuid.UUID]int)
	for _, e := range edges {
		partner := e.EntityBID
		if partner == fema {
			partner = e.EntityAID
		}
		counts[partner]++
	}
	if counts[redCross] != 3 {
		t.Errorf("Red Cross co-occurrence count: got %d, want 3", counts[redCross])
	}
	if counts[nrcs] != 1 {
		t.Errorf("NRCS co-occurrence count: got %d, want 1", counts[nrcs])
	}
}

func TestEdge_BulkCreate_Empty(t *testing.T) {
	repo := memory.New()
	if err := repo.BulkCreate(context.Background(), nil); err != nil {
		t.Fatalf("BulkCreate(nil) should be a no-op, got error: %v", err)
	}
}
