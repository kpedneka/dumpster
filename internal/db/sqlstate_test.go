package db_test

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kunalpednekar/dumpster/internal/pipeline"
)

// internal/pipeline classifies database errors through a SQLState method
// without importing pgx. These tests pin that pgx's error type still
// provides it, so the classification can't silently stop working on a pgx
// upgrade.
func TestPgError_DataExceptionIsNotTransient(t *testing.T) {
	err := pipeline.Transient(fmt.Errorf("chunk: bulk create: %w", &pgconn.PgError{Code: "22P02"}))
	if pipeline.IsTransient(err) {
		t.Error("a pgx data exception (22P02) was marked transient")
	}
}

func TestPgError_ConnectionFailureIsTransient(t *testing.T) {
	err := pipeline.Transient(fmt.Errorf("query: %w", &pgconn.PgError{Code: "08006"}))
	if !pipeline.IsTransient(err) {
		t.Error("a pgx connection failure (08006) should stay transient")
	}
}
