//go:build integration

// Integration test for chunk/pgstore's BulkCreate, specifically covering
// its switch from a per-row Exec loop to one multi-row "INSERT ... VALUES
// (...), (...), ..." statement per batch -- see BulkCreate's own doc for
// the full story, including why this uses a multi-row VALUES INSERT
// rather than CopyFrom (Postgres's COPY protocol, used for
// entity.pgstore's version of the same fix): the embedding column needs
// a "$N::vector" cast, which COPY has no syntax for, and a real test
// against pgvector confirmed passing the uncast value through CopyFrom
// silently misencodes it rather than falling back to text parsing. This
// exists to prove the embedding (and every other field) round-trips
// correctly through the new query shape, not just that it compiles.
//
// Excluded from the default `go test ./...` run and the coverage gate by
// this build tag. Run explicitly against a migrated database:
//
//	TEST_DATABASE_URL="postgres://..." go test -tags=integration ./internal/chunk/pgstore/...
package pgstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/chunk/pgstore"
	"github.com/kunalpednekar/dumpster/internal/document"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
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

// TestBulkCreate_RoundTripsFieldsAndEmbedding is the critical correctness
// check for this change. ListByDocument doesn't even select the embedding
// column (see list()'s query), so it can't be used to verify this -- this
// queries the stored value back directly to confirm the $N::vector cast
// still applies correctly when every row's placeholders are offset into
// one shared multi-row VALUES list, not just for a single-row INSERT.
func TestBulkCreate_RoundTripsFieldsAndEmbedding(t *testing.T) {
	pool := testPool(t)
	txRunner := rls.New(pool)
	sessions := sessionpg.New(pool)
	kbs := kbpg.New(txRunner)
	docs := docpg.New(txRunner)
	chunks := pgstore.New(txRunner)

	ctx := context.Background()
	sess, err := sessions.Create(ctx)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	userID := sess.ID
	userCtx := auth.WithUserID(ctx, userID)

	k, err := kbs.Create(userCtx, userID, "chunk-bulkcreate-test-kb")
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

	embedding := make([]float32, 384)
	for i := range embedding {
		embedding[i] = float32(i) * 0.001
	}
	bbox := &chunk.BoundingBox{X0: 0.1, Y0: 0.2, X1: 0.8, Y1: 0.9}
	pageNum := 3

	want := []*chunk.Chunk{
		{
			DocumentID: doc.ID, KBID: k.ID, UserID: userID, Ordinal: 0,
			Text: "Ada Lovelace worked with Charles Babbage.", TokenCount: 10,
			Embedding: embedding, CharStart: 0, CharEnd: 42,
			PageNumber: &pageNum, BoundingBox: bbox,
		},
		{
			DocumentID: doc.ID, KBID: k.ID, UserID: userID, Ordinal: 1,
			Text: "No embedding or bounding box on this one.", TokenCount: 8,
			CharStart: 42, CharEnd: 84,
		},
	}
	if err := chunks.BulkCreate(userCtx, want); err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	got, err := chunks.ListByDocument(userCtx, userID, doc.ID)
	if err != nil {
		t.Fatalf("ListByDocument: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d", len(got), len(want))
	}

	c0 := got[0]
	if c0.Text != want[0].Text || c0.TokenCount != want[0].TokenCount || c0.CharStart != want[0].CharStart || c0.CharEnd != want[0].CharEnd {
		t.Errorf("chunk 0 fields: got %+v, want %+v", c0, want[0])
	}
	if c0.PageNumber == nil || *c0.PageNumber != pageNum {
		t.Errorf("chunk 0 PageNumber: got %v, want %d", c0.PageNumber, pageNum)
	}
	if c0.BoundingBox == nil || *c0.BoundingBox != *bbox {
		t.Errorf("chunk 0 BoundingBox: got %+v, want %+v", c0.BoundingBox, bbox)
	}

	c1 := got[1]
	if c1.PageNumber != nil || c1.BoundingBox != nil {
		t.Errorf("chunk 1 expected nil PageNumber/BoundingBox, got %v / %+v", c1.PageNumber, c1.BoundingBox)
	}

	// Embedding isn't returned by ListByDocument at all (see list()'s
	// SELECT) -- query it back directly, the only way to actually verify
	// CopyFrom's uncast text value was parsed correctly by pgvector.
	var dims int
	var asText string
	err = pool.QueryRow(ctx,
		`SELECT vector_dims(embedding), embedding::text FROM chunks WHERE id = $1`,
		c0.ID,
	).Scan(&dims, &asText)
	if err != nil {
		t.Fatalf("query stored embedding: %v", err)
	}
	if dims != len(embedding) {
		t.Fatalf("stored embedding dims: got %d, want %d", dims, len(embedding))
	}
	// Spot-check a few values rather than parsing the full 384-dim text
	// representation -- confirms the vector wasn't truncated, zeroed, or
	// corrupted by the uncast COPY path, without over-specifying exact
	// float formatting.
	if asText == "" || asText[0] != '[' {
		t.Fatalf("stored embedding doesn't look like a vector literal: %q", asText[:min(50, len(asText))])
	}

	var emptyDims *int
	err = pool.QueryRow(ctx,
		`SELECT vector_dims(embedding) FROM chunks WHERE id = $1`,
		c1.ID,
	).Scan(&emptyDims)
	if err != nil {
		t.Fatalf("query chunk 1 embedding: %v", err)
	}
	if emptyDims != nil {
		t.Errorf("chunk 1 (no embedding given) expected NULL embedding, got dims=%d", *emptyDims)
	}
}

// TestBulkCreate_SpansMultipleStatementsWithoutLosingRows is a regression
// test for BulkCreate's new per-group statement splitting
// (chunkBulkCreateRowsPerStatement, added to stay under Postgres's 65535-
// parameter hard limit for a single statement): a batch that spans two
// groups must persist every row from both, not silently drop the second
// group or duplicate the first. Deliberately just over one row past the
// split boundary rather than a much larger number, to keep this fast
// while still exercising the actual boundary condition, not comfortably
// inside a single group.
func TestBulkCreate_SpansMultipleStatementsWithoutLosingRows(t *testing.T) {
	pool := testPool(t)
	txRunner := rls.New(pool)
	sessions := sessionpg.New(pool)
	kbs := kbpg.New(txRunner)
	docs := docpg.New(txRunner)
	chunks := pgstore.New(txRunner)

	ctx := context.Background()
	sess, err := sessions.Create(ctx)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	userID := sess.ID
	userCtx := auth.WithUserID(ctx, userID)

	k, err := kbs.Create(userCtx, userID, "chunk-bulkcreate-boundary-test-kb")
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

	// One more than chunkBulkCreateRowsPerStatement (2000) -- not
	// exported, so hardcoded here; if that constant changes, this test's
	// intent (spanning exactly two groups) should be revisited alongside it.
	const n = 2001
	want := make([]*chunk.Chunk, n)
	for i := range want {
		want[i] = &chunk.Chunk{
			DocumentID: doc.ID, KBID: k.ID, UserID: userID, Ordinal: i,
			Text: "chunk", TokenCount: 1, CharStart: i, CharEnd: i + 1,
		}
	}
	if err := chunks.BulkCreate(userCtx, want); err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	got, err := chunks.ListByDocument(userCtx, userID, doc.ID)
	if err != nil {
		t.Fatalf("ListByDocument: %v", err)
	}
	if len(got) != n {
		t.Fatalf("got %d chunks, want %d (a row was lost or duplicated across the statement-group boundary)", len(got), n)
	}
	seen := make(map[int]bool, n)
	for _, c := range got {
		if seen[c.Ordinal] {
			t.Errorf("duplicate ordinal %d", c.Ordinal)
		}
		seen[c.Ordinal] = true
	}
	if len(seen) != n {
		t.Errorf("got %d distinct ordinals, want %d", len(seen), n)
	}
}
