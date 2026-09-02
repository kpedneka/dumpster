// Package pgstore provides a Postgres-backed stats.Repository, backed by
// the single row in usage_stats.
package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/stats"
)

// Store is a Postgres-backed implementation of stats.Repository. runner is
// deliberately a plain db.TxRunner, not the RLS-aware one everything else in
// this codebase uses: usage_stats carries no user_id and has no RLS policy,
// since it's read by the public, unauthenticated GET /stats endpoint whose
// request context carries no tenant identity at all.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) stats.Repository {
	return &Store{runner: runner}
}

// RecordDocumentIndexed increments the documents-indexed counter and adds
// sizeBytes to the running size total.
func (s *Store) RecordDocumentIndexed(ctx context.Context, sizeBytes int64) error {
	const q = `
UPDATE usage_stats
SET    documents_indexed_count       = documents_indexed_count + 1,
       documents_indexed_bytes_total = documents_indexed_bytes_total + $1,
       updated_at                    = NOW()`
	if err := s.exec(ctx, q, sizeBytes); err != nil {
		return fmt.Errorf("stats: record document indexed: %w", err)
	}
	return nil
}

// RecordQueryExecuted increments the queries-executed counter and adds
// durationMs to the running duration total.
func (s *Store) RecordQueryExecuted(ctx context.Context, durationMs int64) error {
	const q = `
UPDATE usage_stats
SET    queries_executed_count    = queries_executed_count + 1,
       queries_duration_ms_total = queries_duration_ms_total + $1,
       updated_at                = NOW()`
	if err := s.exec(ctx, q, durationMs); err != nil {
		return fmt.Errorf("stats: record query executed: %w", err)
	}
	return nil
}

func (s *Store) exec(ctx context.Context, q string, arg int64) error {
	return s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, arg)
		return err
	})
}

// Get returns the current site-wide totals.
func (s *Store) Get(ctx context.Context) (stats.Snapshot, error) {
	var docsCount, docsBytes, queriesCount, queriesMs int64
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
SELECT documents_indexed_count, documents_indexed_bytes_total,
       queries_executed_count, queries_duration_ms_total
FROM   usage_stats`).Scan(&docsCount, &docsBytes, &queriesCount, &queriesMs)
	})
	if err != nil {
		return stats.Snapshot{}, fmt.Errorf("stats: get: %w", err)
	}

	snap := stats.Snapshot{DocumentsIndexed: docsCount, QueriesExecuted: queriesCount}
	if docsCount > 0 {
		snap.AvgDocumentSizeBytes = float64(docsBytes) / float64(docsCount)
	}
	if queriesCount > 0 {
		snap.AvgQueryDurationMs = float64(queriesMs) / float64(queriesCount)
	}
	return snap, nil
}

var _ stats.Repository = (*Store)(nil)
