// Package memory provides an in-memory chunk.Repository for use in tests.
package memory

import (
	"context"
	"sync"

	"github.com/kunalpednekar/dumpster/internal/chunk"
)

type Repository struct {
	mu   sync.RWMutex
	rows []*chunk.Chunk
	next int64
}

func New() *Repository {
	return &Repository{next: 1}
}

func (r *Repository) BulkCreate(_ context.Context, chunks []*chunk.Chunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range chunks {
		cp := *c
		cp.ID = r.next
		r.next++
		r.rows = append(r.rows, &cp)
	}
	return nil
}

func (r *Repository) ListByDocument(_ context.Context, userID, documentID int64) ([]*chunk.Chunk, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*chunk.Chunk
	for _, c := range r.rows {
		if c.UserID == userID && c.DocumentID == documentID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (r *Repository) ListByKB(_ context.Context, userID, kbID int64) ([]*chunk.Chunk, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*chunk.Chunk
	for _, c := range r.rows {
		if c.UserID == userID && c.KBID == kbID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (r *Repository) DeleteByDocument(_ context.Context, userID, documentID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	filtered := r.rows[:0]
	for _, c := range r.rows {
		if c.UserID != userID || c.DocumentID != documentID {
			filtered = append(filtered, c)
		}
	}
	r.rows = filtered
	return nil
}

// Compile-time check.
var _ chunk.Repository = (*Repository)(nil)
