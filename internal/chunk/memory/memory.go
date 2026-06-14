// Package memory provides an in-memory chunk.Repository for use in tests.
package memory

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/chunk"
)

type Repository struct {
	mu   sync.RWMutex
	rows []*chunk.Chunk
}

func New() *Repository { return &Repository{} }

func (r *Repository) BulkCreate(_ context.Context, chunks []*chunk.Chunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range chunks {
		cp := *c
		cp.ID = uuid.New()
		r.rows = append(r.rows, &cp)
	}
	return nil
}

func (r *Repository) ListByDocument(_ context.Context, userID, documentID uuid.UUID) ([]*chunk.Chunk, error) {
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

func (r *Repository) ListByKB(_ context.Context, userID, kbID uuid.UUID) ([]*chunk.Chunk, error) {
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

func (r *Repository) DeleteByDocument(_ context.Context, userID, documentID uuid.UUID) error {
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

var _ chunk.Repository = (*Repository)(nil)
