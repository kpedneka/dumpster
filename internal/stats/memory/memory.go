// Package memory provides an in-memory stats.Repository for use in tests.
package memory

import (
	"context"
	"sync"

	"github.com/kunalpednekar/dumpster/internal/stats"
)

// Repository is an in-memory stats.Repository backed by a single set of
// counters, mirroring the singleton row the Postgres implementation keeps.
type Repository struct {
	mu   sync.Mutex
	snap stats.Snapshot
	// totals in the raw units RecordDocumentIndexed/RecordQueryExecuted are
	// called with, so Get can recompute the averages the same way the
	// Postgres implementation does (sum / count, not a running average).
	docBytesTotal int64
	queryMsTotal  int64
}

// New returns an empty in-memory Repository.
func New() *Repository {
	return &Repository{}
}

// RecordDocumentIndexed increments the documents-indexed counter and adds
// sizeBytes to the running size total.
func (r *Repository) RecordDocumentIndexed(_ context.Context, sizeBytes int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.DocumentsIndexed++
	r.docBytesTotal += sizeBytes
	r.snap.AvgDocumentSizeBytes = float64(r.docBytesTotal) / float64(r.snap.DocumentsIndexed)
	return nil
}

// RecordQueryExecuted increments the queries-executed counter and adds
// durationMs to the running duration total.
func (r *Repository) RecordQueryExecuted(_ context.Context, durationMs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.QueriesExecuted++
	r.queryMsTotal += durationMs
	r.snap.AvgQueryDurationMs = float64(r.queryMsTotal) / float64(r.snap.QueriesExecuted)
	return nil
}

// Get returns the current site-wide totals.
func (r *Repository) Get(_ context.Context) (stats.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snap, nil
}

var _ stats.Repository = (*Repository)(nil)
