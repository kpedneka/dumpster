//go:build integration

// Integration tests for jobstatus/pgstore against a real, migrated
// Postgres. The state rules themselves are unit-tested in
// internal/jobstatus; these tests cover what only a real database can
// show: the SQL, row-level security, and pgx's simple-protocol encoding
// (which cmd/api's pool uses for Neon's PgBouncer).
//
// Excluded from the default `go test ./...` run and the coverage gate by
// this build tag. Run explicitly against a migrated database:
//
//	TEST_DATABASE_URL="postgres://..." go test -tags=integration ./internal/jobstatus/pgstore/...
package pgstore_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/jobstatus/pgstore"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/rls"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
)

func newPool(t *testing.T, simpleProtocol bool) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pcfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if simpleProtocol {
		pcfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newDocument creates real session/kb/document rows so the status table's
// foreign keys are satisfied, and returns a context carrying the owner's
// identity for row-level security.
func newDocument(t *testing.T, pool *pgxpool.Pool) (ctx context.Context, userID, docID uuid.UUID) {
	t.Helper()
	txRunner := rls.New(pool)
	ctx = context.Background()

	sess, err := sessionpg.New(pool).Create(ctx)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	userID = sess.ID
	ctx = auth.WithUserID(ctx, userID)

	k, err := kbpg.New(txRunner).Create(ctx, userID, "jobstatus-pgstore-test-kb")
	if err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc, err := docpg.New(txRunner).Create(ctx, &document.Document{
		KBID: k.ID, UserID: userID, Filename: "doc.pdf",
		S3Key: "test/" + uuid.New().String(), ContentType: "application/pdf",
		Status: document.StatusProcessing,
	})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	return ctx, userID, doc.ID
}

func active(t *testing.T, ctx context.Context, s *pgstore.Store, userID, docID uuid.UUID) []queue.JobStatus {
	t.Helper()
	got, err := s.ActiveJobsForDocuments(ctx, userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments: %v", err)
	}
	return got[docID]
}

func TestLifecycle(t *testing.T) {
	pool := newPool(t, false)
	s := pgstore.New(rls.New(pool))
	ctx, userID, docID := newDocument(t, pool)
	k := jobstatus.Key{UserID: userID, DocumentID: docID, JobType: queue.JobTypeRegionClassification}

	res, err := s.Enqueue(ctx, k)
	if err != nil || !res.Started || res.Attempt != 1 {
		t.Fatalf("Enqueue = %+v, %v; want {Attempt:1 Started:true}", res, err)
	}
	if res, err := s.Enqueue(ctx, k); err != nil || res.Started || res.Attempt != 1 {
		t.Fatalf("duplicate Enqueue = %+v, %v; want {Attempt:1 Started:false}", res, err)
	}

	if err := s.MarkProcessing(ctx, k, 1); err != nil {
		t.Fatalf("MarkProcessing: %v", err)
	}
	if err := s.SetPhase(ctx, k, 1, queue.PhaseEmbedding); err != nil {
		t.Fatalf("SetPhase: %v", err)
	}
	got := active(t, ctx, s, userID, docID)
	if len(got) != 1 || got[0].Status != "processing" || got[0].Phase != queue.PhaseEmbedding {
		t.Fatalf("after SetPhase: %+v, want one processing job in phase %q", got, queue.PhaseEmbedding)
	}

	if err := s.MarkFailed(ctx, k, 1, "unprocessable document"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if got := active(t, ctx, s, userID, docID); len(got) != 0 {
		t.Fatalf("after MarkFailed: %+v, want none", got)
	}

	res, err = s.Enqueue(ctx, k)
	if err != nil || !res.Started || res.Attempt != 2 {
		t.Fatalf("retry Enqueue = %+v, %v; want {Attempt:2 Started:true}", res, err)
	}
	if err := s.MarkSucceeded(ctx, k, 1); !errors.Is(err, jobstatus.ErrAttemptSuperseded) {
		t.Fatalf("MarkSucceeded(stale attempt) err = %v, want ErrAttemptSuperseded", err)
	}
	if err := s.MarkSucceeded(ctx, k, 2); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}
	if got := active(t, ctx, s, userID, docID); len(got) != 0 {
		t.Fatalf("after MarkSucceeded: %+v, want none", got)
	}
}

func TestWrites_UnknownJob_ReturnNotFound(t *testing.T) {
	pool := newPool(t, false)
	s := pgstore.New(rls.New(pool))
	ctx, userID, docID := newDocument(t, pool)
	k := jobstatus.Key{UserID: userID, DocumentID: docID, JobType: queue.JobTypeCanonicalization}

	if err := s.MarkProcessing(ctx, k, 1); !errors.Is(err, jobstatus.ErrNotFound) {
		t.Errorf("MarkProcessing err = %v, want ErrNotFound", err)
	}
}

// TestActiveJobsForDocuments_SimpleProtocol runs the read path the way
// cmd/api does: simple-protocol pool (no server round trip to learn
// parameter types), two concurrently active jobs on one document.
func TestActiveJobsForDocuments_SimpleProtocol(t *testing.T) {
	pool := newPool(t, true)
	s := pgstore.New(rls.New(pool))
	ctx, userID, docID := newDocument(t, pool)

	for _, jt := range []queue.JobType{queue.JobTypeRegionClassification, queue.JobTypeEntityExtraction} {
		if _, err := s.Enqueue(ctx, jobstatus.Key{UserID: userID, DocumentID: docID, JobType: jt}); err != nil {
			t.Fatalf("Enqueue %s: %v", jt, err)
		}
	}

	got := active(t, ctx, s, userID, docID)
	if len(got) != 2 || got[0].Type != queue.JobTypeRegionClassification || got[1].Type != queue.JobTypeEntityExtraction {
		t.Errorf("active = %+v, want region_classification then entity_extraction", got)
	}
}

func TestRowLevelSecurity_OtherTenantSeesAndChangesNothing(t *testing.T) {
	pool := newPool(t, false)
	s := pgstore.New(rls.New(pool))
	ownerCtx, ownerID, docID := newDocument(t, pool)
	otherCtx, otherID, _ := newDocument(t, pool)

	if _, err := s.Enqueue(ownerCtx, jobstatus.Key{UserID: ownerID, DocumentID: docID, JobType: queue.JobTypeDocumentIndexing}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Even if the other tenant passes the owner's user ID, RLS scopes the
	// query to the identity on the context.
	got, err := s.ActiveJobsForDocuments(otherCtx, ownerID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments: %v", err)
	}
	if len(got[docID]) != 0 {
		t.Errorf("other tenant sees %+v, want nothing", got[docID])
	}

	err = s.MarkFailed(otherCtx, jobstatus.Key{UserID: otherID, DocumentID: docID, JobType: queue.JobTypeDocumentIndexing}, 1, "x")
	if !errors.Is(err, jobstatus.ErrNotFound) {
		t.Errorf("other tenant MarkFailed err = %v, want ErrNotFound", err)
	}
	if got := active(t, ownerCtx, s, ownerID, docID); len(got) != 1 || got[0].Status != "pending" {
		t.Errorf("owner's job = %+v, want still pending", got)
	}
}
