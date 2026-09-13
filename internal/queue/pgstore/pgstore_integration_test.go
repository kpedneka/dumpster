//go:build integration

// Integration tests for queue/pgstore's SetPhase and
// ActiveJobsForDocuments -- the read/write paths added for the
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
	"errors"
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
// ("cannot find encode plan" against OID 0); ActiveJobsForDocuments must
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

func TestSetPhase_PersistsAndIsReadableViaActiveJobsForDocuments(t *testing.T) {
	pool := testPool(t)
	store := pgstore.New(pool)
	ctx := context.Background()
	userID, docID := newDocument(t, pool)

	if err := store.PublishRegionClassification(ctx, queue.RegionClassificationRequested{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishRegionClassification: %v", err)
	}

	statuses, err := store.ActiveJobsForDocuments(ctx, userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments: %v", err)
	}
	active := statuses[docID]
	if len(active) != 1 {
		t.Fatalf("expected exactly 1 active job for the document, got %d", len(active))
	}
	if active[0].Type != queue.JobTypeRegionClassification || active[0].Phase != "" {
		t.Errorf("before SetPhase: got %+v, want Type=region_classification, Phase=\"\"", active[0])
	}

	// Look up this test's own job by document_id rather than
	// store.Dequeue(ctx): Dequeue claims the globally-oldest pending job
	// across the whole table with no document scoping, so it can pick up
	// an unrelated leftover pending row from another test sharing this
	// same real database instead of the one this test just created.
	var jobID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM jobs WHERE document_id = $1`, docID).Scan(&jobID); err != nil {
		t.Fatalf("look up job for document: %v", err)
	}
	if err := store.SetPhase(ctx, jobID, queue.PhaseEmbedding); err != nil {
		t.Fatalf("SetPhase: %v", err)
	}

	statuses, err = store.ActiveJobsForDocuments(ctx, userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments (after SetPhase): %v", err)
	}
	active = statuses[docID]
	if len(active) != 1 || active[0].Phase != queue.PhaseEmbedding {
		t.Errorf("after SetPhase: got %+v, want a single job with Phase=%q", active, queue.PhaseEmbedding)
	}
}

// TestActiveJobsForDocuments_ConcurrentJobs_ReturnsBoth is the regression
// test for the exact scenario introduced by letting entity extraction
// start before a document's own indexing job finishes: entity
// extraction is now published earlier, so both can be genuinely active
// for the same document at
// once. The query must surface both, not just whichever was created most
// recently (this method's predecessor, CurrentJobsForDocuments, picked
// only the latest row per document -- exactly the bug this replaces).
func TestActiveJobsForDocuments_ConcurrentJobs_ReturnsBoth(t *testing.T) {
	pool := testPool(t)
	store := pgstore.New(pool)
	ctx := context.Background()
	userID, docID := newDocument(t, pool)

	if err := store.PublishRegionClassification(ctx, queue.RegionClassificationRequested{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishRegionClassification: %v", err)
	}
	if err := store.PublishEntityExtraction(ctx, queue.EntityExtractionRequested{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishEntityExtraction: %v", err)
	}

	statuses, err := store.ActiveJobsForDocuments(ctx, userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments: %v", err)
	}
	active := statuses[docID]
	if len(active) != 2 {
		t.Fatalf("expected both concurrently active jobs, got %d: %+v", len(active), active)
	}
	var types []queue.JobType
	for _, s := range active {
		types = append(types, s.Type)
	}
	if types[0] != queue.JobTypeRegionClassification || types[1] != queue.JobTypeEntityExtraction {
		t.Errorf("job types = %v, want [region_classification entity_extraction] (created_at order)", types)
	}

	// Clean up both jobs directly (bypassing the queue) so they don't
	// linger as globally-pending rows a later test's store.Dequeue(ctx)
	// call could pick up instead of its own -- Dequeue has no document
	// scoping.
	if _, err := pool.Exec(ctx, `DELETE FROM jobs WHERE document_id = $1`, docID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

// TestActiveJobsForDocuments_DeadLetteredJob_Excluded guards against the
// other failure mode a "most recent row wins" query had: a dead-lettered
// job's row is never deleted (only marked status = "failed"), so it must
// be excluded here rather than shown as permanently active.
func TestActiveJobsForDocuments_DeadLetteredJob_Excluded(t *testing.T) {
	pool := testPool(t)
	store := pgstore.New(pool)
	ctx := context.Background()
	userID, docID := newDocument(t, pool)

	if err := store.PublishRegionClassification(ctx, queue.RegionClassificationRequested{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishRegionClassification: %v", err)
	}
	// Look up this test's own job by document_id rather than
	// store.Dequeue(ctx): Dequeue claims the globally-oldest pending job
	// across the whole table with no document scoping, so it can pick up
	// an unrelated leftover pending row from another test sharing this
	// same real database instead of the one this test just created.
	var jobID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM jobs WHERE document_id = $1`, docID).Scan(&jobID); err != nil {
		t.Fatalf("look up job for document: %v", err)
	}
	// region_classification uses queue.SingleShotMaxAttempts (1), so a
	// single Nack dead-letters it immediately -- see
	// PublishRegionClassification's doc.
	deadLettered, err := store.Nack(ctx, jobID, errors.New("simulated failure"))
	if err != nil {
		t.Fatalf("Nack: %v", err)
	}
	if !deadLettered {
		t.Fatal("expected the job to be dead-lettered on the first Nack (SingleShotMaxAttempts=1)")
	}

	statuses, err := store.ActiveJobsForDocuments(ctx, userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments: %v", err)
	}
	if len(statuses[docID]) != 0 {
		t.Errorf("expected no active jobs for a dead-lettered job, got %+v", statuses[docID])
	}
}

func TestActiveJobsForDocuments_NoActiveJob_OmittedFromResult(t *testing.T) {
	pool := testPool(t)
	store := pgstore.New(pool)
	ctx := context.Background()
	userID, docID := newDocument(t, pool)

	statuses, err := store.ActiveJobsForDocuments(ctx, userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments: %v", err)
	}
	if len(statuses[docID]) != 0 {
		t.Error("expected no entries for a document with no jobs row at all")
	}
}

func TestActiveJobsForDocuments_TenantIsolation(t *testing.T) {
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
	statuses, err := store.ActiveJobsForDocuments(ctx, userA, []uuid.UUID{docA, docB})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments: %v", err)
	}
	if len(statuses[docA]) == 0 {
		t.Error("expected tenant A's own document to be present")
	}
	if len(statuses[docB]) != 0 {
		t.Error("tenant A must not see tenant B's job status")
	}
}

// TestActiveJobsForDocuments_WorksUnderSimpleProtocol guards against a
// regression that shipped once: passing []uuid.UUID as a query parameter
// works fine under pgx's default extended protocol (used by testPool
// above) but fails client-side under the QueryExecModeSimpleProtocol
// cmd/api actually runs with in production ("unable to encode
// []uuid.UUID ... cannot find encode plan"), because that mode has no
// server round trip to resolve the parameter's array element type. See
// testSimpleProtocolPool's doc comment and ActiveJobsForDocuments's own
// comment on why document IDs are marshaled to []string before the query.
func TestActiveJobsForDocuments_WorksUnderSimpleProtocol(t *testing.T) {
	pool := testSimpleProtocolPool(t)
	store := pgstore.New(pool)
	ctx := context.Background()
	userID, docID := newDocument(t, pool)

	if err := store.PublishRegionClassification(ctx, queue.RegionClassificationRequested{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishRegionClassification: %v", err)
	}

	statuses, err := store.ActiveJobsForDocuments(ctx, userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments under simple protocol: %v", err)
	}
	active := statuses[docID]
	if len(active) != 1 || active[0].Type != queue.JobTypeRegionClassification {
		t.Errorf("got %+v, want a single region_classification job", active)
	}
}

// jobIDFor looks up the single job row for documentID -- used instead of
// Dequeue in tests that need to manipulate one specific, known job, since
// Dequeue claims whichever pending job is globally oldest across the whole
// table, not one scoped to a particular document. CountPending's own tests
// need that scoping: this suite's tests all share one real database and
// none of them clean up their rows afterward (matching this file's existing
// tests), so by the time CountPending's test runs there may be other tests'
// leftover pending jobs sitting in the table -- a real, reproducible bug
// caught only by running this test repeatedly (`-count=5`), not on a single
// run: Dequeue non-deterministically claimed a stale leftover job instead
// of either of the two this test had just published.
func jobIDFor(t *testing.T, pool *pgxpool.Pool, documentID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM jobs WHERE document_id = $1`, documentID,
	).Scan(&id); err != nil {
		t.Fatalf("look up job for document %s: %v", documentID, err)
	}
	return id
}

// TestCountPending_GroupsByJobTypeAndExcludesOtherStatuses verifies that
// CountPending reflects only "pending" jobs, grouped by job_type -- the
// signal internal/queuemetrics publishes for the worker's queue-depth
// auto-scaling policy.
//
// Known hazard specific to this repo's shared dev database, not a
// production concern: whichever environment's real worker is currently
// live (confirmed via `aws ecs describe-services` -- staging's worker runs
// continuously against this same database) polls the jobs table with its
// own Dequeue call and can claim this test's rows the instant they're
// committed, before this test's own Ack/CountPending calls run -- a real
// external race, not a bug in CountPending itself (verified correct via a
// standalone, single-consumer reproduction). Reliable on isolated runs;
// only surfaces under repeated back-to-back stress runs (`-count=N>1`)
// while a live worker is also polling. Scoping the row to a specific,
// looked-up job ID (jobIDFor, below) rather than the table-wide Dequeue
// already removes the *other* half of this test's original flakiness
// (claiming some unrelated leftover row instead of one of its own).
func TestCountPending_GroupsByJobTypeAndExcludesOtherStatuses(t *testing.T) {
	pool := testPool(t)
	store := pgstore.New(pool)
	ctx := context.Background()

	userA, docA := newDocument(t, pool)
	userB, docB := newDocument(t, pool)

	if err := store.PublishDocumentUploaded(ctx, queue.DocumentUploaded{DocumentID: docA, UserID: userA}); err != nil {
		t.Fatalf("publish document_indexing: %v", err)
	}
	if err := store.PublishEntityExtraction(ctx, queue.EntityExtractionRequested{DocumentID: docB, UserID: userB}); err != nil {
		t.Fatalf("publish entity_extraction: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE document_id IN ($1, $2)`, docA, docB)
	})

	// Ack docA's own job directly (looked up by document, not claimed via
	// the table-wide Dequeue -- see jobIDFor's doc) -- CountPending must not
	// count it once it has left status "pending".
	if err := store.Ack(ctx, jobIDFor(t, pool, docA)); err != nil {
		t.Fatalf("ack: %v", err)
	}

	counts, err := store.CountPending(ctx)
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}

	if n := counts[queue.JobTypeEntityExtraction]; n < 1 {
		t.Errorf("entity_extraction count = %d, want at least 1 (docB's still-pending job); counts=%+v", n, counts)
	}
	// docA's job_type is document_indexing (PublishDocumentUploaded's
	// type) -- can't assert its count is exactly 0 across the whole table
	// (other tests' leftover rows of the same type may coexist), only that
	// acking removed the row entirely rather than merely changing its
	// status, which a direct existence check confirms unambiguously.
	var stillExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE document_id = $1)`, docA).Scan(&stillExists); err != nil {
		t.Fatalf("check docA job existence: %v", err)
	}
	if stillExists {
		t.Error("docA's job row should have been deleted by Ack, but still exists")
	}
}
