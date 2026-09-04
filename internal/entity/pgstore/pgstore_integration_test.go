//go:build integration

// Integration test for entity/pgstore's BulkCreate, specifically covering
// its switch from a per-row Exec loop to CopyFrom (Postgres's COPY wire
// protocol) -- see BulkCreate's own doc for why. CopyFrom is a genuinely
// different code path than a normal INSERT (different wire protocol
// entirely), so this exists to give real evidence -- against a real
// Postgres, not the in-memory fake -- that every field still round-trips
// correctly and, critically, that row-level security tenant isolation
// still applies to rows written via COPY exactly as it does for a normal
// INSERT.
//
// Excluded from the default `go test ./...` run and the coverage gate by
// this build tag. Run explicitly against a migrated database:
//
//	TEST_DATABASE_URL="postgres://..." go test -tags=integration ./internal/entity/pgstore/...
package pgstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkpg "github.com/kunalpednekar/dumpster/internal/chunk/pgstore"
	"github.com/kunalpednekar/dumpster/internal/document"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/entity/pgstore"
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

func TestBulkCreate_RoundTripsAllFieldsViaCopyFrom(t *testing.T) {
	pool := testPool(t)
	txRunner := rls.New(pool)
	sessions := sessionpg.New(pool)
	kbs := kbpg.New(txRunner)
	docs := docpg.New(txRunner)
	chunks := chunkpg.New(txRunner)
	entities := pgstore.New(txRunner)

	ctx := context.Background()
	sess, err := sessions.Create(ctx)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	userID := sess.ID
	userCtx := auth.WithUserID(ctx, userID)

	k, err := kbs.Create(userCtx, userID, "entity-bulkcreate-test-kb")
	if err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc, err := docs.Create(userCtx, &document.Document{
		KBID: k.ID, UserID: userID, Filename: "doc.txt",
		S3Key: "test/" + uuid.New().String(), ContentType: "text/plain",
		Status: document.StatusIndexed,
	})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := chunks.BulkCreate(userCtx, []*chunk.Chunk{
		{DocumentID: doc.ID, KBID: k.ID, UserID: userID, Ordinal: 0, Text: "Ada Lovelace worked with Charles Babbage.", CharStart: 0, CharEnd: 42},
	}); err != nil {
		t.Fatalf("create chunk: %v", err)
	}
	storedChunks, err := chunks.ListByDocument(userCtx, userID, doc.ID)
	if err != nil || len(storedChunks) != 1 {
		t.Fatalf("list chunks: %v (%d)", err, len(storedChunks))
	}
	chunkID := storedChunks[0].ID

	want := []*entity.Entity{
		{DocumentID: doc.ID, KBID: k.ID, UserID: userID, ChunkID: chunkID, Type: "person", Text: "Ada Lovelace", Start: 0, End: 12, Score: 0.93},
		{DocumentID: doc.ID, KBID: k.ID, UserID: userID, ChunkID: chunkID, Type: "person", Text: "Charles Babbage", Start: 22, End: 37, Score: 0.87},
	}
	if err := entities.BulkCreate(userCtx, want); err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	got, err := entities.ListByDocument(userCtx, userID, doc.ID)
	if err != nil {
		t.Fatalf("ListByDocument: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entities, want %d", len(got), len(want))
	}

	byText := make(map[string]*entity.Entity, len(got))
	for _, e := range got {
		byText[e.Text] = e
	}
	for _, w := range want {
		g, ok := byText[w.Text]
		if !ok {
			t.Fatalf("missing entity %q in round-tripped result", w.Text)
		}
		if g.ChunkID != w.ChunkID || g.DocumentID != w.DocumentID || g.KBID != w.KBID || g.UserID != w.UserID {
			t.Errorf("entity %q: foreign keys don't match: got %+v, want %+v", w.Text, g, w)
		}
		if g.Type != w.Type || g.Start != w.Start || g.End != w.End || g.Score != w.Score {
			t.Errorf("entity %q: fields don't match: got type=%s start=%d end=%d score=%v, want type=%s start=%d end=%d score=%v",
				w.Text, g.Type, g.Start, g.End, g.Score, w.Type, w.Start, w.End, w.Score)
		}
		if g.ID == uuid.Nil {
			t.Errorf("entity %q: expected a DB-generated ID, got the zero UUID", w.Text)
		}
	}
}

// TestBulkCreate_TenantIsolationSurvivesCopyFrom is the critical
// correctness check for this change: COPY is a genuinely different wire
// protocol than a normal INSERT, so this proves row-level security still
// scopes rows written via BulkCreate to their owning tenant, not just
// rows written via a plain Exec -- per CLAUDE.md's multi-tenancy
// invariant ("every query filters on the current tenant. No exceptions"),
// this needed direct evidence, not an assumption that RLS "just applies."
func TestBulkCreate_TenantIsolationSurvivesCopyFrom(t *testing.T) {
	pool := testPool(t)
	txRunner := rls.New(pool)
	sessions := sessionpg.New(pool)
	kbs := kbpg.New(txRunner)
	docs := docpg.New(txRunner)
	chunks := chunkpg.New(txRunner)
	entities := pgstore.New(txRunner)
	ctx := context.Background()

	newTenantWithEntity := func(text string) (userID, docID uuid.UUID) {
		sess, err := sessions.Create(ctx)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		userID = sess.ID
		userCtx := auth.WithUserID(ctx, userID)

		k, err := kbs.Create(userCtx, userID, "tenant-isolation-test-kb")
		if err != nil {
			t.Fatalf("create kb: %v", err)
		}
		doc, err := docs.Create(userCtx, &document.Document{
			KBID: k.ID, UserID: userID, Filename: "doc.txt",
			S3Key: "test/" + uuid.New().String(), ContentType: "text/plain",
			Status: document.StatusIndexed,
		})
		if err != nil {
			t.Fatalf("create doc: %v", err)
		}
		docID = doc.ID
		if err := chunks.BulkCreate(userCtx, []*chunk.Chunk{
			{DocumentID: docID, KBID: k.ID, UserID: userID, Ordinal: 0, Text: "x", CharStart: 0, CharEnd: 1},
		}); err != nil {
			t.Fatalf("create chunk: %v", err)
		}
		stored, err := chunks.ListByDocument(userCtx, userID, docID)
		if err != nil || len(stored) != 1 {
			t.Fatalf("list chunks: %v (%d)", err, len(stored))
		}
		if err := entities.BulkCreate(userCtx, []*entity.Entity{
			{DocumentID: docID, KBID: k.ID, UserID: userID, ChunkID: stored[0].ID, Type: "person", Text: text, Start: 0, End: 1},
		}); err != nil {
			t.Fatalf("BulkCreate: %v", err)
		}
		return
	}

	userA, docA := newTenantWithEntity("belongs to A")
	_, docB := newTenantWithEntity("belongs to B")

	ctxA := auth.WithUserID(ctx, userA)
	gotA, err := entities.ListByDocument(ctxA, userA, docA)
	if err != nil || len(gotA) != 1 || gotA[0].Text != "belongs to A" {
		t.Fatalf("tenant A's own entities: got %v (err %v)", gotA, err)
	}

	// Tenant A must not be able to read tenant B's document's entities,
	// even by document ID, even though both were written via the same
	// BulkCreate/CopyFrom code path.
	gotCrossTenant, err := entities.ListByDocument(ctxA, userA, docB)
	if err != nil {
		t.Fatalf("cross-tenant ListByDocument errored instead of returning empty: %v", err)
	}
	if len(gotCrossTenant) != 0 {
		t.Fatalf("tenant A read %d of tenant B's entities via CopyFrom-written rows -- RLS violation", len(gotCrossTenant))
	}
}
