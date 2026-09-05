//go:build integration

// Integration tests for queue/pgstore's SetPhase and
// CurrentJobsForDocuments -- the two new read/write paths added for the
// upload-progress feature. jobs has no RLS policy (unlike most other
// tables in this app), so these tests filter by user_id manually, same
// as the production code they're testing.
//
// Excluded from the default `go test ./...` run and the coverage gate by
// this build tag. Run explicitly against a migrated database:
//
//	TEST_DATABASE_URL="postgres://..." go test -tags=integration ./internal/queue/pgstore/...
package pgstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/queue/pgstore"
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

// testSimpleProtocolPool mirrors internal/db.Connect's production
// configuration for cmd/api (pgx.QueryExecModeSimpleProtocol, required for
// Neon's PgBouncer in transaction mode) -- the default testPool above uses
// pgx's normal extended/prepared-statement protocol instead, which
// resolves parameter types via a server round trip and so does not
// exercise the client-side-only encoding path simple protocol requires.
// A bare []uuid.UUID query parameter silently fails only under this mode
// ("cannot find encode plan" against OID 0); CurrentJobsForDocuments must
// be tested against it directly, not just the default pool.
func testSimpleProtocolPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pcfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	pcfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newDocument creates a real session/kb/document row so jobs' foreign
// keys are satisfied -- jobs.document_id and jobs.user_id both reference
// real tables.
func newDocument(t *testing.T, pool *pgxpool.Pool) (userID, docID uuid.UUID) {
	t.Helper()
	txRunner := rls.New(pool)
	sessions := sessionpg.New(pool)
	kbs := kbpg.New(txRunner)
	docs := docpg.New(txRunner)

	ctx := context.Background()
	sess, err := sessions.Create(ctx)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	userID = sess.ID
	userCtx := auth.WithUserID(ctx, userID)

	k, err := kbs.Create(userCtx, userID, "queue-pgstore-test-kb")
	if err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc, err := docs.Create(userCtx, &document.Document{
		KBID: k.ID, UserID: userID, Filename: "doc.pdf",
		S3Key: "test/" + uuid.New().String(), ContentType: "application/pdf",
		Status: document.StatusProcessing,
	})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	return userID, doc.ID
}

func TestSetPhase_PersistsAndIsReadableViaCurrentJobsForDocuments(t *testing.T) {
	pool := testPool(t)
	store := pgstore.New(pool)
	ctx := context.Background()
	userID, docID := newDocument(t, pool)

	if err := store.PublishRegionClassification(ctx, queue.RegionClassificationRequested{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishRegionClassification: %v", err)
	}

	statuses, err := store.CurrentJobsForDocuments(ctx, userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("CurrentJobsForDocuments: %v", err)
	}
	got, ok := statuses[docID]
	if !ok {
		t.Fatal("expected a job status for the document")
	}
	if got.Type != queue.JobTypeRegionClassification || got.Phase != "" {
		t.Errorf("before SetPhase: got %+v, want Type=region_classification, Phase=\"\"", got)
	}

	job, err := store.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if err := store.SetPhase(ctx, job.ID, queue.PhaseEmbedding); err != nil {
		t.Fatalf("SetPhase: %v", err)
	}

	statuses, err = store.CurrentJobsForDocuments(ctx, userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("CurrentJobsForDocuments (after SetPhase): %v", err)
	}
	got = statuses[docID]
	if got.Phase != queue.PhaseEmbedding {
		t.Errorf("after SetPhase: Phase = %q, want %q", got.Phase, queue.PhaseEmbedding)
	}
}

// TestCurrentJobsForDocuments_WorksUnderSimpleProtocol guards against a
// regression that shipped once: passing []uuid.UUID as a query parameter
// works fine under pgx's default extended protocol (used by testPool
// above) but fails client-side under the QueryExecModeSimpleProtocol
// cmd/api actually runs with in production ("unable to encode
// []uuid.UUID ... cannot find encode plan"), because that mode has no
// server round trip to resolve the parameter's array element type. See
// testSimpleProtocolPool's doc comment and CurrentJobsForDocuments's own
// comment on why document IDs are marshaled to []string before the query.
func TestCurrentJobsForDocuments_WorksUnderSimpleProtocol(t *testing.T) {
	pool := testSimpleProtocolPool(t)
	store := pgstore.New(pool)
	ctx := context.Background()
	userID, docID := newDocument(t, pool)

	if err := store.PublishRegionClassification(ctx, queue.RegionClassificationRequested{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishRegionClassification: %v", err)
	}

	statuses, err := store.CurrentJobsForDocuments(ctx, userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("CurrentJobsForDocuments under simple protocol: %v", err)
	}
	got, ok := statuses[docID]
	if !ok {
		t.Fatal("expected a job status for the document")
	}
	if got.Type != queue.JobTypeRegionClassification {
		t.Errorf("Type = %q, want %q", got.Type, queue.JobTypeRegionClassification)
	}
}

func TestCurrentJobsForDocuments_NoActiveJob_OmittedFromResult(t *testing.T) {
	pool := testPool(t)
	store := pgstore.New(pool)
	ctx := context.Background()
	userID, docID := newDocument(t, pool)

	statuses, err := store.CurrentJobsForDocuments(ctx, userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("CurrentJobsForDocuments: %v", err)
	}
	if _, ok := statuses[docID]; ok {
		t.Error("expected no entry for a document with no jobs row at all")
	}
}

func TestCurrentJobsForDocuments_TenantIsolation(t *testing.T) {
	pool := testPool(t)
	store := pgstore.New(pool)
	ctx := context.Background()

	userA, docA := newDocument(t, pool)
	if err := store.PublishRegionClassification(ctx, queue.RegionClassificationRequested{DocumentID: docA, UserID: userA}); err != nil {
		t.Fatalf("PublishRegionClassification: %v", err)
	}
	userB, docB := newDocument(t, pool)
	if err := store.PublishRegionClassification(ctx, queue.RegionClassificationRequested{DocumentID: docB, UserID: userB}); err != nil {
		t.Fatalf("PublishRegionClassification: %v", err)
	}

	// Tenant A queries for tenant B's document ID under tenant A's own
	// user_id -- must not see it, even though the document ID is known
	// and a job for it genuinely exists.
	statuses, err := store.CurrentJobsForDocuments(ctx, userA, []uuid.UUID{docA, docB})
	if err != nil {
		t.Fatalf("CurrentJobsForDocuments: %v", err)
	}
	if _, ok := statuses[docA]; !ok {
		t.Error("expected tenant A's own document to be present")
	}
	if _, ok := statuses[docB]; ok {
		t.Error("tenant A must not see tenant B's job status")
	}
}
