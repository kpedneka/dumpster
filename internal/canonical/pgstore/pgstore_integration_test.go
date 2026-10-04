//go:build integration

// Integration tests for canonical/pgstore against a real, migrated
// Postgres, run as a non-superuser so row-level security applies:
//
//	TEST_DATABASE_URL="postgres://..." go test -tags=integration ./internal/canonical/pgstore/...
package pgstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kunalpednekar/dumpster/internal/auth"
	canonicalpg "github.com/kunalpednekar/dumpster/internal/canonical/pgstore"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkpg "github.com/kunalpednekar/dumpster/internal/chunk/pgstore"
	"github.com/kunalpednekar/dumpster/internal/document"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	entitypg "github.com/kunalpednekar/dumpster/internal/entity/pgstore"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
)

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

// TestDecrementForDocument_IsIdempotent covers a retried entity-extraction
// reset: if DecrementForDocument runs twice before the document's entities
// are deleted, canonical counts must only drop once.
func TestDecrementForDocument_IsIdempotent(t *testing.T) {
	pool := testPool(t)
	runner := rls.New(pool)
	ctx := context.Background()

	sess, err := sessionpg.New(pool).Create(ctx)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	userID := sess.ID
	ctx = auth.WithUserID(ctx, userID)
	k, err := kbpg.New(runner).Create(ctx, userID, "canonical-pgstore-test-kb")
	if err != nil {
		t.Fatalf("create kb: %v", err)
	}

	docs, chunks, entities, canon := docpg.New(runner), chunkpg.New(runner), entitypg.New(runner), canonicalpg.New(runner)

	// Two documents mention the same canonical entity, so it survives one
	// document's decrement and its counts can be checked.
	var docIDs []uuid.UUID
	for i := 0; i < 2; i++ {
		doc, err := docs.Create(ctx, &document.Document{
			KBID: k.ID, UserID: userID, Filename: "doc.txt", S3Key: "test/" + uuid.New().String(),
			ContentType: "text/plain", Status: document.StatusIndexed,
		})
		if err != nil {
			t.Fatalf("create doc: %v", err)
		}
		if err := chunks.BulkCreate(ctx, []*chunk.Chunk{{DocumentID: doc.ID, KBID: k.ID, UserID: userID, Text: "Ada Lovelace wrote notes.", CharEnd: 25}}); err != nil {
			t.Fatalf("create chunk: %v", err)
		}
		cs, err := chunks.ListByDocument(ctx, userID, doc.ID)
		if err != nil || len(cs) != 1 {
			t.Fatalf("list chunks: %v (%d)", err, len(cs))
		}
		// A plain INSERT, not entities.BulkCreate: BulkCreate uses COPY,
		// which Postgres rejects on a FORCE ROW LEVEL SECURITY table for a
		// role the policy applies to, as this test's role is.
		if err := runner.RunInTx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				`INSERT INTO entities (document_id, kb_id, user_id, chunk_id, entity_type, text, char_start, char_end, score)
				 VALUES ($1, $2, $3, $4, 'person', 'Ada Lovelace', 0, 12, 0.9)`,
				doc.ID, k.ID, userID, cs[0].ID)
			return err
		}); err != nil {
			t.Fatalf("create entity: %v", err)
		}
		mentions, err := entities.ListByDocument(ctx, userID, doc.ID)
		if err != nil {
			t.Fatalf("list entities: %v", err)
		}
		links, err := canon.Canonicalize(ctx, mentions)
		if err != nil {
			t.Fatalf("canonicalize: %v", err)
		}
		if err := entities.BulkSetCanonicalEntityID(ctx, userID, links); err != nil {
			t.Fatalf("link mentions: %v", err)
		}
		docIDs = append(docIDs, doc.ID)
	}

	all, err := canon.ListByKB(ctx, userID, k.ID)
	if err != nil || len(all) != 1 || all[0].MentionCount != 2 || all[0].DocumentCount != 2 {
		t.Fatalf("setup: canonical entities = %+v, %v; want one with 2 mentions in 2 documents", all, err)
	}

	for i := 0; i < 2; i++ {
		if err := canon.DecrementForDocument(ctx, userID, docIDs[0]); err != nil {
			t.Fatalf("DecrementForDocument call %d: %v", i+1, err)
		}
	}

	got, err := canon.Get(ctx, userID, all[0].ID)
	if err != nil {
		t.Fatalf("Get after decrement: %v (a second decrement deleted the shared canonical entity)", err)
	}
	if got.MentionCount != 1 || got.DocumentCount != 1 {
		t.Errorf("after two decrements for one document: mentions=%d documents=%d, want 1 and 1", got.MentionCount, got.DocumentCount)
	}
}
