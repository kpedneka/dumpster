//go:build integration

// Integration tests for ratelimit/pgstore -- the shared-store rate limiter
// that replaces internal/ratelimit/memory once the API runs more than one
// replica. rate_limit_counters has no RLS (see migrations/030, and jobs for
// the existing precedent): a rate-limit decision happens before any tenant
// identity exists, so these tests connect with a plain pool, not the
// RLS-aware rls.TxRunner other pgstore integration tests use.
//
// Excluded from the default `go test ./...` run and the coverage gate by
// this build tag. Run explicitly against a migrated database:
//
//	TEST_DATABASE_URL="postgres://..." go test -tags=integration ./internal/ratelimit/pgstore/...
package pgstore_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kunalpednekar/dumpster/internal/ratelimit/pgstore"
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
// Neon's PgBouncer in transaction mode). Allow's own params are plain
// scalars (text, float8), not the array type that broke a different query
// under this mode once already (see queue/pgstore's integration test) --
// this still exercises the real production connection mode directly rather
// than assuming scalar params are safe.
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

func uniqueKey(t *testing.T) string {
	t.Helper()
	return "test-" + uuid.NewString()
}

func TestStore_allowsUpToLimit(t *testing.T) {
	pool := testPool(t)
	s := pgstore.New(pool, 2, time.Minute)
	key := uniqueKey(t)
	ctx := context.Background()

	for i, want := range []bool{true, true, false} {
		got, err := s.Allow(ctx, key)
		if err != nil {
			t.Fatalf("Allow #%d: %v", i, err)
		}
		if got != want {
			t.Errorf("Allow #%d: got %v, want %v", i, got, want)
		}
	}
}

func TestStore_recoversAfterWindowExpires(t *testing.T) {
	pool := testPool(t)
	// A very short window so the test can wait for it to expire for real,
	// rather than needing an injectable clock like memory.Store has.
	s := pgstore.New(pool, 1, 200*time.Millisecond)
	key := uniqueKey(t)
	ctx := context.Background()

	ok, err := s.Allow(ctx, key)
	if err != nil || !ok {
		t.Fatalf("first request: ok=%v err=%v, want true, nil", ok, err)
	}
	ok, err = s.Allow(ctx, key)
	if err != nil || ok {
		t.Fatalf("second request within window: ok=%v err=%v, want false, nil", ok, err)
	}

	time.Sleep(250 * time.Millisecond)

	ok, err = s.Allow(ctx, key)
	if err != nil || !ok {
		t.Fatalf("request after window expiry: ok=%v err=%v, want true, nil", ok, err)
	}
}

func TestStore_differentKeysAreIndependent(t *testing.T) {
	pool := testPool(t)
	s := pgstore.New(pool, 1, time.Minute)
	keyA, keyB := uniqueKey(t), uniqueKey(t)
	ctx := context.Background()

	if ok, err := s.Allow(ctx, keyA); err != nil || !ok {
		t.Fatalf("key A first request: ok=%v err=%v", ok, err)
	}
	if ok, err := s.Allow(ctx, keyA); err != nil || ok {
		t.Fatalf("key A second request should be blocked: ok=%v err=%v", ok, err)
	}
	if ok, err := s.Allow(ctx, keyB); err != nil || !ok {
		t.Fatalf("key B should have its own quota: ok=%v err=%v", ok, err)
	}
}

// TestStore_concurrentRequestsForSameKeyDoNotOvercount is the whole point of
// this package: N concurrent requests for the same key (simulating N API
// replicas hitting the same IP's counter at once) must never let more than
// `limit` of them through, which the atomic INSERT ... ON CONFLICT in
// Allow's query is what guarantees -- a naive read-then-write from Go would
// race here.
func TestStore_concurrentRequestsForSameKeyDoNotOvercount(t *testing.T) {
	pool := testPool(t)
	const limit = 10
	s := pgstore.New(pool, limit, time.Minute)
	key := uniqueKey(t)
	ctx := context.Background()

	const attempts = 50
	results := make(chan bool, attempts)
	for range attempts {
		go func() {
			ok, err := s.Allow(ctx, key)
			if err != nil {
				t.Error(err)
				results <- false
				return
			}
			results <- ok
		}()
	}

	allowed := 0
	for range attempts {
		if <-results {
			allowed++
		}
	}

	if allowed != limit {
		t.Errorf("allowed = %d, want exactly %d", allowed, limit)
	}
}

// TestStore_simpleProtocol confirms Allow's params encode correctly under
// pgx.QueryExecModeSimpleProtocol -- the connection mode cmd/api actually
// runs under in production (see internal/db.Connect), which resolves
// parameter types client-side rather than via a server round trip.
func TestStore_simpleProtocol(t *testing.T) {
	pool := testSimpleProtocolPool(t)
	s := pgstore.New(pool, 1, time.Minute)
	key := uniqueKey(t)
	ctx := context.Background()

	ok, err := s.Allow(ctx, key)
	if err != nil {
		t.Fatalf("Allow under simple protocol: %v", err)
	}
	if !ok {
		t.Error("first request should be allowed")
	}
}

func TestDeleteExpired(t *testing.T) {
	pool := testPool(t)
	s := pgstore.New(pool, 100, time.Millisecond)
	ctx := context.Background()

	key := uniqueKey(t)
	if _, err := s.Allow(ctx, key); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	deleted, err := pgstore.DeleteExpired(ctx, pool, 0)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted < 1 {
		t.Errorf("deleted = %d, want at least 1", deleted)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rate_limit_counters WHERE key = $1`, key).Scan(&count); err != nil {
		t.Fatalf("verify deletion: %v", err)
	}
	if count != 0 {
		t.Errorf("row for %q still present after DeleteExpired", key)
	}
}
