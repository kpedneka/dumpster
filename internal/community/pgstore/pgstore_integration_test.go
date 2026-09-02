//go:build integration

// Integration tests for community/pgstore. KBGraph's join/aggregation SQL
// (collapsing chunk-scoped entity_edges onto canonical entity pairs, summing
// weight across every contributing mention pair) and SaveResult's batched
// UPDATE + upsert are exactly the kind of logic a memory fake can't
// exercise — this seeds a real multi-document scenario through the actual
// repository write paths and checks the resulting graph and round-trip.
//
// Excluded from the default `go test ./...` run and the coverage gate by
// this build tag. Run explicitly against a migrated database:
//
//	TEST_DATABASE_URL="postgres://..." go test -tags=integration ./internal/community/pgstore/...
package pgstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/canonical"
	canonicalpg "github.com/kunalpednekar/dumpster/internal/canonical/pgstore"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkpg "github.com/kunalpednekar/dumpster/internal/chunk/pgstore"
	"github.com/kunalpednekar/dumpster/internal/community"
	"github.com/kunalpednekar/dumpster/internal/community/pgstore"
	"github.com/kunalpednekar/dumpster/internal/document"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	"github.com/kunalpednekar/dumpster/internal/entity"
	entitypg "github.com/kunalpednekar/dumpster/internal/entity/pgstore"
	"github.com/kunalpednekar/dumpster/internal/graphedge"
	graphedgepg "github.com/kunalpednekar/dumpster/internal/graphedge/pgstore"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
)

// testPool connects to TEST_DATABASE_URL, a migrated Postgres database
// dedicated to running these tests against (see `make migrate`). Skips
// rather than fails when that env var isn't set.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestKBGraph_CollapsesAcrossChunksAndDocuments seeds two documents sharing
// a canonical entity ("Ada Lovelace") whose edges appear in two separate
// chunks — proving KBGraph (a) collapses raw per-chunk mention edges onto
// canonical entity pairs regardless of which document/chunk they came from,
// and (b) sums co_occurrence_count across every contributing mention pair
// rather than keeping them as separate rows.
func TestKBGraph_CollapsesAcrossChunksAndDocuments(t *testing.T) {
	pool := testPool(t)
	txRunner := rls.New(pool)
	sessions := sessionpg.New(pool)
	kbs := kbpg.New(txRunner)
	docs := docpg.New(txRunner)
	chunks := chunkpg.New(txRunner)
	entities := entitypg.New(txRunner)
	edges := graphedgepg.New(txRunner)
	canonicalRepo := canonicalpg.New(txRunner)
	communities := pgstore.New(txRunner)

	sess, err := sessions.Create(context.Background())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	userID := sess.ID
	ctx := auth.WithUserID(context.Background(), userID)

	kb, err := kbs.Create(ctx, userID, "community-test-kb")
	if err != nil {
		t.Fatalf("create kb: %v", err)
	}

	docA, err := docs.Create(ctx, &document.Document{
		KBID: kb.ID, UserID: userID, Filename: "docA.txt",
		S3Key: "test/" + uuid.New().String(), ContentType: "text/plain",
		Status: document.StatusIndexed,
	})
	if err != nil {
		t.Fatalf("create docA: %v", err)
	}
	docB, err := docs.Create(ctx, &document.Document{
		KBID: kb.ID, UserID: userID, Filename: "docB.txt",
		S3Key: "test/" + uuid.New().String(), ContentType: "text/plain",
		Status: document.StatusIndexed,
	})
	if err != nil {
		t.Fatalf("create docB: %v", err)
	}

	if err := chunks.BulkCreate(ctx, []*chunk.Chunk{
		{DocumentID: docA.ID, KBID: kb.ID, UserID: userID, Ordinal: 0, Text: "Ada Lovelace worked closely with Charles Babbage.", CharStart: 0, CharEnd: 50},
		{DocumentID: docB.ID, KBID: kb.ID, UserID: userID, Ordinal: 0, Text: "Ada Lovelace briefed Charles Babbage on the analytical engine.", CharStart: 0, CharEnd: 60},
		{DocumentID: docB.ID, KBID: kb.ID, UserID: userID, Ordinal: 1, Text: "Ada Lovelace flagged a gap for Platform Security.", CharStart: 60, CharEnd: 110},
	}); err != nil {
		t.Fatalf("create chunks: %v", err)
	}
	storedA, err := chunks.ListByDocument(ctx, userID, docA.ID)
	if err != nil || len(storedA) != 1 {
		t.Fatalf("list chunks A: %v (%d)", err, len(storedA))
	}
	storedB, err := chunks.ListByDocument(ctx, userID, docB.ID)
	if err != nil || len(storedB) != 2 {
		t.Fatalf("list chunks B: %v (%d)", err, len(storedB))
	}

	if err := entities.BulkCreate(ctx, []*entity.Entity{
		{DocumentID: docA.ID, KBID: kb.ID, UserID: userID, ChunkID: storedA[0].ID, Type: "person", Text: "Ada Lovelace", Start: 0, End: 12},
		{DocumentID: docA.ID, KBID: kb.ID, UserID: userID, ChunkID: storedA[0].ID, Type: "person", Text: "Charles Babbage", Start: 30, End: 45},
		{DocumentID: docB.ID, KBID: kb.ID, UserID: userID, ChunkID: storedB[0].ID, Type: "person", Text: "Ada Lovelace", Start: 0, End: 12},
		{DocumentID: docB.ID, KBID: kb.ID, UserID: userID, ChunkID: storedB[0].ID, Type: "person", Text: "Charles Babbage", Start: 21, End: 36},
		{DocumentID: docB.ID, KBID: kb.ID, UserID: userID, ChunkID: storedB[1].ID, Type: "person", Text: "Ada Lovelace", Start: 0, End: 12},
		{DocumentID: docB.ID, KBID: kb.ID, UserID: userID, ChunkID: storedB[1].ID, Type: "org", Text: "Platform Security", Start: 32, End: 49},
	}); err != nil {
		t.Fatalf("create entities: %v", err)
	}

	allA, err := entities.ListByDocument(ctx, userID, docA.ID)
	if err != nil || len(allA) != 2 {
		t.Fatalf("list entities A: %v (%d)", err, len(allA))
	}
	allB, err := entities.ListByDocument(ctx, userID, docB.ID)
	if err != nil || len(allB) != 4 {
		t.Fatalf("list entities B: %v (%d)", err, len(allB))
	}

	byChunkAndText := func(ents []*entity.Entity, chunkID uuid.UUID, text string) *entity.Entity {
		for _, e := range ents {
			if e.ChunkID == chunkID && e.Text == text {
				return e
			}
		}
		return nil
	}
	adaA := byChunkAndText(allA, storedA[0].ID, "Ada Lovelace")
	charlesA := byChunkAndText(allA, storedA[0].ID, "Charles Babbage")
	adaB0 := byChunkAndText(allB, storedB[0].ID, "Ada Lovelace")
	charlesB0 := byChunkAndText(allB, storedB[0].ID, "Charles Babbage")
	adaB1 := byChunkAndText(allB, storedB[1].ID, "Ada Lovelace")
	platformB1 := byChunkAndText(allB, storedB[1].ID, "Platform Security")
	if adaA == nil || charlesA == nil || adaB0 == nil || charlesB0 == nil || adaB1 == nil || platformB1 == nil {
		t.Fatalf("setup: missing expected entities: A=%+v B=%+v", allA, allB)
	}

	if err := edges.BulkCreate(ctx, []*graphedge.Edge{
		graphedge.NewEdge(docA.ID, kb.ID, userID, storedA[0].ID, adaA.ID, charlesA.ID),
		graphedge.NewEdge(docB.ID, kb.ID, userID, storedB[0].ID, adaB0.ID, charlesB0.ID),
		graphedge.NewEdge(docB.ID, kb.ID, userID, storedB[1].ID, adaB1.ID, platformB1.ID),
	}); err != nil {
		t.Fatalf("create edges: %v", err)
	}

	if err := canonical.ResolveNew(ctx, canonicalRepo, entities, userID, append(allA, allB...)); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}

	// Re-fetch: ResolveNew updated CanonicalEntityID on the DB rows, but the
	// in-memory structs captured above (allA/allB and the byChunkAndText
	// lookups derived from them) still have it nil.
	allA, err = entities.ListByDocument(ctx, userID, docA.ID)
	if err != nil {
		t.Fatalf("re-list entities A: %v", err)
	}
	allB, err = entities.ListByDocument(ctx, userID, docB.ID)
	if err != nil {
		t.Fatalf("re-list entities B: %v", err)
	}
	adaA = byChunkAndText(allA, storedA[0].ID, "Ada Lovelace")
	charlesA = byChunkAndText(allA, storedA[0].ID, "Charles Babbage")
	platformB1 = byChunkAndText(allB, storedB[1].ID, "Platform Security")
	if adaA == nil || charlesA == nil || platformB1 == nil ||
		adaA.CanonicalEntityID == nil || charlesA.CanonicalEntityID == nil || platformB1.CanonicalEntityID == nil {
		t.Fatalf("post-canonicalize refetch: missing canonical links: Ada=%+v Charles=%+v Platform=%+v", adaA, charlesA, platformB1)
	}

	graph, err := communities.KBGraph(ctx, userID, kb.ID)
	if err != nil {
		t.Fatalf("KBGraph: %v", err)
	}
	if len(graph.Nodes) != 3 {
		t.Fatalf("nodes: got %d, want 3 (Ada, Charles, Platform Security)", len(graph.Nodes))
	}
	if len(graph.Edges) != 2 {
		t.Fatalf("edges: got %d, want 2 (Ada-Charles collapsed, Ada-PlatformSecurity)", len(graph.Edges))
	}

	var adaCharlesWeight, adaPlatformWeight float64
	var adaCharlesFound, adaPlatformFound bool
	// Resolve canonical ids for Ada/Charles/Platform Security via the
	// already-canonicalized mentions to check the weight on the right pair,
	// since KBGraph itself only returns opaque canonical entity ids.
	adaCanon := *adaA.CanonicalEntityID
	charlesCanon := *charlesA.CanonicalEntityID
	platformCanon := *platformB1.CanonicalEntityID
	if adaCanon == charlesCanon || adaCanon == platformCanon || charlesCanon == platformCanon {
		t.Fatalf("expected three distinct canonical entities, got Ada=%v Charles=%v Platform=%v", adaCanon, charlesCanon, platformCanon)
	}
	for _, e := range graph.Edges {
		switch {
		case (e.A == adaCanon && e.B == charlesCanon) || (e.A == charlesCanon && e.B == adaCanon):
			adaCharlesWeight = e.Weight
			adaCharlesFound = true
		case (e.A == adaCanon && e.B == platformCanon) || (e.A == platformCanon && e.B == adaCanon):
			adaPlatformWeight = e.Weight
			adaPlatformFound = true
		default:
			t.Errorf("unexpected edge: %+v", e)
		}
	}
	if !adaCharlesFound {
		t.Fatal("expected an Ada-Charles edge, found none")
	}
	if adaCharlesWeight != 2 {
		t.Errorf("Ada-Charles weight: got %v, want 2 (one mention pair per document, collapsed and summed)", adaCharlesWeight)
	}
	if !adaPlatformFound {
		t.Fatal("expected an Ada-PlatformSecurity edge, found none")
	}
	if adaPlatformWeight != 1 {
		t.Errorf("Ada-PlatformSecurity weight: got %v, want 1", adaPlatformWeight)
	}

	// SaveResult + GetResult round-trip: assign every node to community 0,
	// then confirm both the per-entity column and the run summary persist
	// and read back correctly.
	assignments := map[uuid.UUID]int{adaCanon: 0, charlesCanon: 0, platformCanon: 0}
	run := community.Result{Modularity: 0.42, CommunityCount: 1, NodeCount: 3, EdgeCount: 2}
	if err := communities.SaveResult(ctx, userID, kb.ID, assignments, run); err != nil {
		t.Fatalf("SaveResult: %v", err)
	}

	got, err := communities.GetResult(ctx, userID, kb.ID)
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if got.CommunityCount != 1 || got.NodeCount != 3 || got.EdgeCount != 2 {
		t.Errorf("GetResult: got %+v, want CommunityCount=1 NodeCount=3 EdgeCount=2", got)
	}
	if got.Modularity < 0.41 || got.Modularity > 0.43 {
		t.Errorf("GetResult.Modularity: got %v, want ~0.42", got.Modularity)
	}

	// Verify SaveResult actually wrote community_id onto the canonical_entities
	// row, not just the kb_community_runs summary.
	var wroteCommunityID int
	if err := pool.QueryRow(ctx,
		`SELECT community_id FROM canonical_entities WHERE id = $1`, adaCanon,
	).Scan(&wroteCommunityID); err != nil {
		t.Fatalf("query canonical_entities.community_id: %v", err)
	}
	if wroteCommunityID != 0 {
		t.Errorf("canonical_entities.community_id for Ada: got %d, want 0", wroteCommunityID)
	}

	count, err := communities.CountCanonicalEntities(ctx, userID, kb.ID)
	if err != nil {
		t.Fatalf("CountCanonicalEntities: %v", err)
	}
	if count != 3 {
		t.Errorf("CountCanonicalEntities: got %d, want 3", count)
	}
}
