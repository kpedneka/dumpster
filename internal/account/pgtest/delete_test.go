// Package pgtest contains integration tests for the account deletion lifecycle.
// Tests in this package require a live Postgres database and a running
// S3-compatible object store (MinIO locally, Cloudflare R2 in cloud).
//
// Set DATABASE_URL to opt in:
//
//	DATABASE_URL=postgres://... go test ./internal/account/pgtest/
//
// All other S3 env vars fall back to MinIO defaults so docker compose up is
// sufficient locally.
package pgtest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kunalpednekar/dumpster/internal/account"
	docpgstore "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	"github.com/kunalpednekar/dumpster/internal/rls"
	"github.com/kunalpednekar/dumpster/internal/session"
	sessionpgstore "github.com/kunalpednekar/dumpster/internal/session/pgstore"
)

// skipIfNoInfra skips the calling test when DATABASE_URL is absent.
// S3 configuration falls back to MinIO defaults and is not guarded here —
// a connection failure is a loud enough signal for the developer to check.
func skipIfNoInfra(t *testing.T) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("integration: set DATABASE_URL to run")
	}
}

func mustPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("open pg pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func mustS3(t *testing.T) *s3store.Store {
	t.Helper()
	return s3store.New(s3store.Config{
		Endpoint:     getEnvOr("S3_ENDPOINT", "http://localhost:9000"),
		Region:       getEnvOr("S3_REGION", "us-east-1"),
		Bucket:       getEnvOr("S3_BUCKET", "dumpster"),
		AccessKey:    getEnvOr("S3_ACCESS_KEY", "minioadmin"),
		SecretKey:    getEnvOr("S3_SECRET_KEY", "minioadmin"),
		UsePathStyle: true,
	})
}

func getEnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// runWithRLS executes fn inside a transaction with app.current_user_id set to
// userID, matching the per-transaction RLS injection that rls.TxRunner applies
// in production.
func runWithRLS(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_user_id', $1, true)`, userID.String()); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// countRows returns the number of rows in table whose user_id equals userID.
// The query runs inside an RLS-context transaction so FORCE ROW LEVEL SECURITY
// is satisfied; after a cascade delete the count must be zero.
func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, table string) int {
	t.Helper()
	var n int
	err := runWithRLS(ctx, pool, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&n)
	})
	if err != nil {
		t.Fatalf("countRows(%s, %s): %v", table, userID, err)
	}
	return n
}

// insertExpiredSession plants a session row with created_at far enough in the
// past to exceed both HardCap (24 h) and IdleTimeout (6 h), so that a Sweep
// run will unconditionally schedule it for deletion.
func insertExpiredSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *session.Session {
	t.Helper()
	var sess session.Session
	err := pool.QueryRow(ctx,
		`INSERT INTO sessions (created_at, last_active_at)
		 VALUES (NOW() - INTERVAL '25 hours', NOW() - INTERVAL '7 hours')
		 RETURNING id, created_at, last_active_at, warned_at`,
	).Scan(&sess.ID, &sess.CreatedAt, &sess.LastActiveAt, &sess.WarnedAt)
	if err != nil {
		t.Fatalf("insert expired session: %v", err)
	}
	return &sess
}

// seedData creates a knowledge base, one document (+ corresponding S3 object),
// and one chunk for the given session, returning the S3 key so the caller can
// verify it is absent after deletion.
func seedData(t *testing.T, ctx context.Context, pool *pgxpool.Pool, objects *s3store.Store, sessID uuid.UUID) (s3Key string) {
	t.Helper()

	// knowledge base
	var kbID uuid.UUID
	if err := runWithRLS(ctx, pool, sessID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO knowledge_bases (user_id, name) VALUES ($1, 'integration-test') RETURNING id`,
			sessID,
		).Scan(&kbID)
	}); err != nil {
		t.Fatalf("seed kb: %v", err)
	}

	// document + S3 object
	s3Key = fmt.Sprintf("test/%s/doc.txt", sessID)
	var docID uuid.UUID
	if err := runWithRLS(ctx, pool, sessID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO documents (kb_id, user_id, filename, s3_key, content_type, status)
			 VALUES ($1, $2, 'doc.txt', $3, 'text/plain', 'indexed') RETURNING id`,
			kbID, sessID, s3Key,
		).Scan(&docID)
	}); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := objects.Put(ctx, s3Key, strings.NewReader("hello"), 5, "text/plain"); err != nil {
		t.Fatalf("seed s3 object: %v", err)
	}

	// chunk (embedding omitted — nullable)
	if err := runWithRLS(ctx, pool, sessID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO chunks (document_id, kb_id, user_id, ordinal, text, token_count, char_start, char_end)
			 VALUES ($1, $2, $3, 0, 'hello', 1, 0, 5)`,
			docID, kbID, sessID,
		)
		return err
	}); err != nil {
		t.Fatalf("seed chunk: %v", err)
	}

	return s3Key
}

// assertAllGone verifies that the cascade delete removed every descendant row
// from knowledge_bases, documents, and chunks, and that the S3 object at key
// is no longer retrievable.
func assertAllGone(t *testing.T, ctx context.Context, pool *pgxpool.Pool, objects *s3store.Store, userID uuid.UUID, s3Key string) {
	t.Helper()
	for _, table := range []string{"knowledge_bases", "documents", "chunks"} {
		if n := countRows(t, ctx, pool, userID, table); n != 0 {
			t.Errorf("post-delete: %s has %d row(s) for session %s, want 0", table, n, userID)
		}
	}
	if _, err := objects.Get(ctx, s3Key); err == nil {
		t.Errorf("post-delete: S3 object %q still retrievable after deletion", s3Key)
	}
}

// TestDeleter_cascade_removesAllRowsAndObjects exercises the real Deleter
// against live Postgres and MinIO/R2. It verifies that:
//   - every knowledge_base, document, and chunk row for the session is removed
//     by the ON DELETE CASCADE chain when the session row is deleted, and
//   - every S3 object listed in the session's documents is explicitly deleted
//     by account.Deleter before the session row is removed.
func TestDeleter_cascade_removesAllRowsAndObjects(t *testing.T) {
	skipIfNoInfra(t)
	ctx := context.Background()
	pool := mustPool(t)
	objects := mustS3(t)

	sessionStore := sessionpgstore.New(pool)
	docStore := docpgstore.New(rls.New(pool))

	// Create session and seed data.
	sess, err := sessionStore.Create(ctx)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	s3Key := seedData(t, ctx, pool, objects, sess.ID)

	// Confirm test rows are present before calling Delete.
	for _, table := range []string{"knowledge_bases", "documents", "chunks"} {
		if n := countRows(t, ctx, pool, sess.ID, table); n != 1 {
			t.Fatalf("pre-delete: expected 1 row in %s, got %d", table, n)
		}
	}

	// Delete via the real Deleter.
	d := account.New(sessionStore, objects, docStore)
	if err := d.Delete(ctx, sess); err != nil {
		t.Fatalf("Deleter.Delete: %v", err)
	}

	assertAllGone(t, ctx, pool, objects, sess.ID, s3Key)
}

// TestSweep_cascade_removesExpiredSession exercises the full Sweep → Deleter
// path against live infrastructure. It plants a session whose created_at is 25
// hours in the past (past HardCap), seeds KB/document/chunk/S3 data, then
// runs a single Sweep.Run and asserts that every trace of the session is gone.
//
// NOTE: Sweep.Run processes every session in the database. Run this test
// against a dedicated integration database, not a shared development instance.
func TestSweep_cascade_removesExpiredSession(t *testing.T) {
	skipIfNoInfra(t)
	ctx := context.Background()
	pool := mustPool(t)
	objects := mustS3(t)

	sessionStore := sessionpgstore.New(pool)
	docStore := docpgstore.New(rls.New(pool))

	// Plant an expired session with owned data.
	sess := insertExpiredSession(t, ctx, pool)
	s3Key := seedData(t, ctx, pool, objects, sess.ID)

	// Run the sweep with a Now that sees the session as expired.
	deleter := account.New(sessionStore, objects, docStore)
	sweep := account.NewSweep(sessionStore, deleter)
	sweep.Now = func() time.Time { return time.Now() }

	result, err := sweep.Run(ctx)
	if err != nil {
		t.Fatalf("Sweep.Run: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("Sweep.Run: per-session errors: %v", result.Errors)
	}
	if result.Deleted == 0 {
		t.Error("Sweep.Run: expected Deleted >= 1 (the expired session)")
	}

	// The swept session must have left no rows or objects behind.
	assertAllGone(t, ctx, pool, objects, sess.ID, s3Key)

	// The session row itself must be gone.
	if _, err := sessionStore.GetByID(ctx, sess.ID); err == nil {
		t.Error("session row still present after sweep")
	}
}
