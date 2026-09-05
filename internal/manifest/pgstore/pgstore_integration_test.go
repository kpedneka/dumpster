//go:build integration

// Integration test for manifest/pgstore's BulkCreate, specifically
// covering its switch from a per-row Exec loop to CopyFrom (Postgres's
// COPY wire protocol) -- see BulkCreate's own doc for the measured
// numbers behind the change. CopyFrom is a genuinely different code path
// than a normal INSERT, so this exists to give real evidence -- against a
// real Postgres, not an in-memory fake -- that every field (including the
// JSONB bounding_box column) still round-trips correctly.
//
// Excluded from the default `go test ./...` run and the coverage gate by
// this build tag. Run explicitly against a migrated database:
//
//	TEST_DATABASE_URL="postgres://..." go test -tags=integration ./internal/manifest/pgstore/...
package pgstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/manifest"
	"github.com/kunalpednekar/dumpster/internal/manifest/pgstore"
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
	regions := pgstore.New(txRunner)

	ctx := context.Background()
	sess, err := sessions.Create(ctx)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	userID := sess.ID
	userCtx := auth.WithUserID(ctx, userID)

	k, err := kbs.Create(userCtx, userID, "manifest-bulkcreate-test-kb")
	if err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc, err := docs.Create(userCtx, &document.Document{
		KBID: k.ID, UserID: userID, Filename: "doc.pdf",
		S3Key: "test/" + uuid.New().String(), ContentType: "application/pdf",
		Status: document.StatusIndexed,
	})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}

	bbox := manifest.BoundingBox{X0: 0.1, Y0: 0.2, X1: 0.9, Y1: 0.95}
	want := []*manifest.Region{
		{
			DocumentID: doc.ID, KBID: k.ID, UserID: userID,
			RegionType: manifest.RegionTypeNativeText, PageNumber: 1,
			BoundingBox: bbox, Status: manifest.StatusIndexed, ExtractorVersion: "v1",
		},
		{
			DocumentID: doc.ID, KBID: k.ID, UserID: userID,
			RegionType: manifest.RegionTypeFigure, PageNumber: 2,
			BoundingBox: manifest.FullPage(), Status: manifest.StatusSkipped, ExtractorVersion: "v1",
		},
	}
	if err := regions.BulkCreate(userCtx, want); err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	got, err := regions.ListByDocument(userCtx, userID, doc.ID)
	if err != nil {
		t.Fatalf("ListByDocument: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d regions, want %d", len(got), len(want))
	}

	byPage := make(map[int]*manifest.Region, len(got))
	for _, r := range got {
		byPage[r.PageNumber] = r
	}

	r1, ok := byPage[1]
	if !ok {
		t.Fatal("missing page 1 region")
	}
	if r1.RegionType != manifest.RegionTypeNativeText || r1.Status != manifest.StatusIndexed || r1.ExtractorVersion != "v1" {
		t.Errorf("page 1 region fields: %+v", r1)
	}
	if r1.BoundingBox != bbox {
		t.Errorf("page 1 bounding_box (JSONB via CopyFrom) = %+v, want %+v", r1.BoundingBox, bbox)
	}
	if r1.ID == uuid.Nil {
		t.Error("expected a DB-generated ID")
	}

	r2, ok := byPage[2]
	if !ok {
		t.Fatal("missing page 2 region")
	}
	if r2.RegionType != manifest.RegionTypeFigure || r2.Status != manifest.StatusSkipped {
		t.Errorf("page 2 region fields: %+v", r2)
	}
	if r2.BoundingBox != manifest.FullPage() {
		t.Errorf("page 2 bounding_box = %+v, want FullPage()", r2.BoundingBox)
	}
}

// TestBulkCreate_TenantIsolationSurvivesCopyFrom is the critical
// correctness check for this change: COPY is a genuinely different wire
// protocol than a normal INSERT, so this proves row-level security still
// scopes rows written via BulkCreate to their owning tenant -- per
// CLAUDE.md's multi-tenancy invariant ("every query filters on the
// current tenant. No exceptions"), this needed direct evidence, not an
// assumption that RLS "just applies."
func TestBulkCreate_TenantIsolationSurvivesCopyFrom(t *testing.T) {
	pool := testPool(t)
	txRunner := rls.New(pool)
	sessions := sessionpg.New(pool)
	kbs := kbpg.New(txRunner)
	docs := docpg.New(txRunner)
	regions := pgstore.New(txRunner)
	ctx := context.Background()

	newTenantWithRegion := func() (userID, docID uuid.UUID) {
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
			KBID: k.ID, UserID: userID, Filename: "doc.pdf",
			S3Key: "test/" + uuid.New().String(), ContentType: "application/pdf",
			Status: document.StatusIndexed,
		})
		if err != nil {
			t.Fatalf("create doc: %v", err)
		}
		docID = doc.ID
		if err := regions.BulkCreate(userCtx, []*manifest.Region{
			{DocumentID: docID, KBID: k.ID, UserID: userID, RegionType: manifest.RegionTypeNativeText, PageNumber: 1, BoundingBox: manifest.FullPage(), Status: manifest.StatusIndexed, ExtractorVersion: "v1"},
		}); err != nil {
			t.Fatalf("BulkCreate: %v", err)
		}
		return
	}

	userA, docA := newTenantWithRegion()
	_, docB := newTenantWithRegion()

	ctxA := auth.WithUserID(ctx, userA)
	gotA, err := regions.ListByDocument(ctxA, userA, docA)
	if err != nil || len(gotA) != 1 {
		t.Fatalf("tenant A's own regions: got %v (err %v)", gotA, err)
	}

	gotCrossTenant, err := regions.ListByDocument(ctxA, userA, docB)
	if err != nil {
		t.Fatalf("cross-tenant ListByDocument errored instead of returning empty: %v", err)
	}
	if len(gotCrossTenant) != 0 {
		t.Fatalf("tenant A read %d of tenant B's regions via CopyFrom-written rows -- RLS violation", len(gotCrossTenant))
	}
}
