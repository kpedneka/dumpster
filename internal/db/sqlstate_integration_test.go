//go:build integration

package db_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kunalpednekar/dumpster/internal/pipeline"
)

// Real Postgres errors, wrapped the way the pgstores wrap them, classified
// by pipeline.Transient: the end-to-end version of sqlstate_test.go.
//
//	TEST_DATABASE_URL="postgres://..." go test -tags=integration ./internal/db/...
func TestTransient_RealPostgresErrors(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	// The simple protocol cmd/api and the Lambdas use with PgBouncer.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	deterministic := map[string]string{
		"invalid json (22P02)":     `SELECT 'not json'::json`,
		"division by zero (22012)": `SELECT 1/0`,
		"undefined table (42P01)":  `SELECT * FROM no_such_table_for_sqlstate_test`,
	}
	for name, query := range deterministic {
		t.Run(name, func(t *testing.T) {
			_, qerr := pool.Exec(ctx, query)
			if qerr == nil {
				t.Fatal("query unexpectedly succeeded")
			}
			if pipeline.IsTransient(pipeline.Transient(fmt.Errorf("repo: %w", qerr))) {
				t.Errorf("%v was marked transient", qerr)
			}
		})
	}

	t.Run("unique violation (23505)", func(t *testing.T) {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer conn.Release()
		if _, err := conn.Exec(ctx, `CREATE TEMP TABLE sqlstate_test (id int PRIMARY KEY); INSERT INTO sqlstate_test VALUES (1)`); err != nil {
			t.Fatalf("setup: %v", err)
		}
		_, qerr := conn.Exec(ctx, `INSERT INTO sqlstate_test VALUES (1)`)
		if qerr == nil {
			t.Fatal("duplicate insert unexpectedly succeeded")
		}
		if pipeline.IsTransient(pipeline.Transient(fmt.Errorf("repo: %w", qerr))) {
			t.Errorf("%v was marked transient", qerr)
		}
	})

	t.Run("connection refused stays transient", func(t *testing.T) {
		bad, err := pgxpool.New(ctx, "postgres://nobody@127.0.0.1:1/none?connect_timeout=2")
		if err != nil {
			t.Fatalf("pool config: %v", err)
		}
		defer bad.Close()
		perr := bad.Ping(ctx)
		if perr == nil {
			t.Fatal("ping to a closed port unexpectedly succeeded")
		}
		if !pipeline.IsTransient(pipeline.Transient(fmt.Errorf("repo: %w", perr))) {
			t.Errorf("%v should stay transient", perr)
		}
	})
}
