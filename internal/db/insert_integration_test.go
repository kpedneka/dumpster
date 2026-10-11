//go:build integration

package db_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kunalpednekar/dumpster/internal/db"
)

// InsertRows splits rows across as many statements as Postgres's
// 65535-parameter limit needs: 25,000 rows of 3 columns is 75,000
// parameters, so this only succeeds if it batches.
func TestInsertRows_SplitsAcrossTheParameterLimit(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	const n = 25000
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE insert_rows_test (a int, b text, c double precision) ON COMMIT DROP`); err != nil {
		t.Fatalf("create temp table: %v", err)
	}

	err = db.InsertRows(ctx, tx, "insert_rows_test", []string{"a", "b", "c"}, n, func(i int) ([]any, error) {
		return []any{i, fmt.Sprintf("row-%d", i), float64(i) / 2}, nil
	})
	if err != nil {
		t.Fatalf("InsertRows: %v", err)
	}

	var count, sumA int
	var lastB string
	if err := tx.QueryRow(ctx, `SELECT count(*), sum(a), max(b) FILTER (WHERE a = $1) FROM insert_rows_test`, n-1).Scan(&count, &sumA, &lastB); err != nil {
		t.Fatal(err)
	}
	if count != n || sumA != n*(n-1)/2 || lastB != fmt.Sprintf("row-%d", n-1) {
		t.Errorf("got count=%d sum=%d last=%q, want %d rows of 0..%d", count, sumA, lastB, n, n-1)
	}
}

func TestInsertRows_PropagatesRowErrors(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = db.InsertRows(ctx, tx, "never_reached", []string{"a"}, 1, func(int) ([]any, error) {
		return nil, fmt.Errorf("bad row")
	})
	if err == nil {
		t.Error("InsertRows returned nil for a failing row")
	}
}
