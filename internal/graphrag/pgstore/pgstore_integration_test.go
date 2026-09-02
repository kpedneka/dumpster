//go:build integration

// Integration tests for graphrag/pgstore. These exercise real SQL against a
// real Postgres database — the bug this package's TraversalLeg had (its own
// exclusion clause guaranteed zero rows, unconditionally) lived entirely in
// SQL that an in-memory fake never runs, so no unit test could have caught
// it or can prove it stays fixed.
//
// Excluded from the default `go test ./...` run and the coverage gate by
// this build tag. Run explicitly against a migrated database:
//
//	TEST_DATABASE_URL="postgres://..." go test -tags=integration ./internal/graphrag/pgstore/...
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
	"github.com/kunalpednekar/dumpster/internal/document"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	"github.com/kunalpednekar/dumpster/internal/entity"
	entitypg "github.com/kunalpednekar/dumpster/internal/entity/pgstore"
	"github.com/kunalpednekar/dumpster/internal/graphedge"
	graphedgepg "github.com/kunalpednekar/dumpster/internal/graphedge/pgstore"
	"github.com/kunalpednekar/dumpster/internal/graphrag/pgstore"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
)

// testPool connects to TEST_DATABASE_URL, a migrated Postgres database
// dedicated to running these tests against (see `make migrate`). Skips
// rather than fails when that env var isn't set, so this build-tagged suite
// degrades gracefully when nobody's pointed it at a database.
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

// TestTraversalLeg_TwoHopAcrossDocuments is the regression test A1 exists
// for: two documents, each mentioning a shared real entity, where the
// answer is only reachable by hopping through that shared identity —
// entity_edges alone never connects them, since each edge is chunk-scoped
// and mentions are per-chunk with no dedup. Before this fix, TraversalLeg's
// own exclusion clause guaranteed zero rows for every query, on every KB,
// unconditionally; this seeds a real two-document scenario and proves a
// chunk from the second document comes back.
func TestTraversalLeg_TwoHopAcrossDocuments(t *testing.T) {
	pool := testPool(t)
	txRunner := rls.New(pool)
	sessions := sessionpg.New(pool)
	kbs := kbpg.New(txRunner)
	docs := docpg.New(txRunner)
	chunks := chunkpg.New(txRunner)
	entities := entitypg.New(txRunner)
	edges := graphedgepg.New(txRunner)
	canonicalRepo := canonicalpg.New(txRunner)
	graph := pgstore.New(txRunner)

	sess, err := sessions.Create(context.Background())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	userID := sess.ID
	ctx := auth.WithUserID(context.Background(), userID)

	kb, err := kbs.Create(ctx, userID, "traversal-test-kb")
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
		{DocumentID: docB.ID, KBID: kb.ID, UserID: userID, Ordinal: 0, Text: "Ada Lovelace flagged a gap for Platform Security.", CharStart: 0, CharEnd: 50},
	}); err != nil {
		t.Fatalf("create chunks: %v", err)
	}
	storedA, err := chunks.ListByDocument(ctx, userID, docA.ID)
	if err != nil || len(storedA) != 1 {
		t.Fatalf("list chunks A: %v (%d)", err, len(storedA))
	}
	storedB, err := chunks.ListByDocument(ctx, userID, docB.ID)
	if err != nil || len(storedB) != 1 {
		t.Fatalf("list chunks B: %v (%d)", err, len(storedB))
	}

	if err := entities.BulkCreate(ctx, []*entity.Entity{
		{DocumentID: docA.ID, KBID: kb.ID, UserID: userID, ChunkID: storedA[0].ID, Type: "person", Text: "Ada Lovelace", Start: 0, End: 12},
		{DocumentID: docA.ID, KBID: kb.ID, UserID: userID, ChunkID: storedA[0].ID, Type: "person", Text: "Charles Babbage", Start: 30, End: 45},
		{DocumentID: docB.ID, KBID: kb.ID, UserID: userID, ChunkID: storedB[0].ID, Type: "person", Text: "Ada Lovelace", Start: 0, End: 12},
		{DocumentID: docB.ID, KBID: kb.ID, UserID: userID, ChunkID: storedB[0].ID, Type: "org", Text: "Platform Security", Start: 32, End: 49},
	}); err != nil {
		t.Fatalf("create entities: %v", err)
	}

	allA, err := entities.ListByDocument(ctx, userID, docA.ID)
	if err != nil || len(allA) != 2 {
		t.Fatalf("list entities A: %v (%d)", err, len(allA))
	}
	allB, err := entities.ListByDocument(ctx, userID, docB.ID)
	if err != nil || len(allB) != 2 {
		t.Fatalf("list entities B: %v (%d)", err, len(allB))
	}

	var adaA, charlesA, adaB, platformB *entity.Entity
	for _, e := range allA {
		switch e.Text {
		case "Ada Lovelace":
			adaA = e
		case "Charles Babbage":
			charlesA = e
		}
	}
	for _, e := range allB {
		switch e.Text {
		case "Ada Lovelace":
			adaB = e
		case "Platform Security":
			platformB = e
		}
	}
	if adaA == nil || charlesA == nil || adaB == nil || platformB == nil {
		t.Fatalf("setup: missing expected entities: A=%+v B=%+v", allA, allB)
	}

	if err := edges.BulkCreate(ctx, []*graphedge.Edge{
		graphedge.NewEdge(docA.ID, kb.ID, userID, storedA[0].ID, adaA.ID, charlesA.ID),
		graphedge.NewEdge(docB.ID, kb.ID, userID, storedB[0].ID, adaB.ID, platformB.ID),
	}); err != nil {
		t.Fatalf("create edges: %v", err)
	}

	// Canonicalize both documents' mentions so the two "Ada Lovelace"
	// mentions, in separate documents, resolve to one shared identity.
	if err := canonical.ResolveNew(ctx, canonicalRepo, entities, userID, append(allA, allB...)); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}

	// Seed on "Charles Babbage" (only in doc A) and traverse two hops:
	// Charles -> Ada(A) -> [canonical expansion] -> Ada(B) -> Platform
	// Security -> doc B's chunk. Before the fix, this unconditionally
	// returns zero rows regardless of what's seeded.
	results, err := graph.TraversalLeg(ctx, kb.ID, "Charles Babbage", 10)
	if err != nil {
		t.Fatalf("TraversalLeg: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("TraversalLeg returned zero rows; expected doc B's chunk via the shared Ada Lovelace identity")
	}

	foundDocB := false
	for _, r := range results {
		if r.Chunk.DocumentID == docA.ID {
			t.Errorf("TraversalLeg returned the seed document's own chunk (%v); hop-2 must exclude seed chunks", r.Chunk.ID)
		}
		if r.Chunk.DocumentID == docB.ID {
			foundDocB = true
		}
	}
	if !foundDocB {
		t.Fatalf("TraversalLeg did not return a chunk from document B; got %d result(s) from elsewhere", len(results))
	}
}
