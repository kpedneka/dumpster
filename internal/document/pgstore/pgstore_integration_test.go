//go:build integration

// Integration tests for document/pgstore. Run against a migrated database:
//
//	TEST_DATABASE_URL="postgres://..." go test -tags=integration ./internal/document/pgstore/...
package pgstore_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/document/pgstore"
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

func TestGetByS3Key(t *testing.T) {
	pool := testPool(t)
	txRunner := rls.New(pool)
	docs := pgstore.New(txRunner)
	ctx := context.Background()

	sess, err := sessionpg.New(pool).Create(ctx)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	userID := sess.ID
	userCtx := auth.WithUserID(ctx, userID)
	k, err := kbpg.New(txRunner).Create(userCtx, userID, "getbys3key-test-kb")
	if err != nil {
		t.Fatalf("create kb: %v", err)
	}
	key := "documents/" + userID.String() + "/" + k.ID.String() + "/" + uuid.NewString() + "/scan.pdf"
	created, err := docs.Create(userCtx, &document.Document{
		KBID: k.ID, UserID: userID, Filename: "scan.pdf", S3Key: key,
		ContentType: "application/pdf", Status: document.StatusPending,
	})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}

	got, err := docs.GetByS3Key(userCtx, userID, key)
	if err != nil {
		t.Fatalf("GetByS3Key: %v", err)
	}
	if got.ID != created.ID || got.UserID != userID || got.ContentType != "application/pdf" {
		t.Errorf("got %+v, want document %v", got, created.ID)
	}

	if _, err := docs.GetByS3Key(userCtx, userID, key+"-missing"); !errors.Is(err, document.ErrNotFound) {
		t.Errorf("missing key: got %v, want document.ErrNotFound", err)
	}
	other := uuid.New()
	if _, err := docs.GetByS3Key(auth.WithUserID(ctx, other), other, key); !errors.Is(err, document.ErrNotFound) {
		t.Errorf("another tenant's key: got %v, want document.ErrNotFound", err)
	}
}
