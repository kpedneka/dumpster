// Package memory provides an in-memory document.Repository for use in tests.
package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kunalpednekar/dumpster/internal/document"
)

type Repository struct {
	mu   sync.RWMutex
	rows map[int64]*document.Document
	next int64
}

func New() *Repository {
	return &Repository{rows: make(map[int64]*document.Document), next: 1}
}

func (r *Repository) Create(_ context.Context, d *document.Document) (*document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	cp := *d
	cp.ID = r.next
	cp.CreatedAt = now
	cp.UpdatedAt = now
	r.rows[r.next] = &cp
	r.next++
	return &cp, nil
}

func (r *Repository) Get(_ context.Context, userID, id int64) (*document.Document, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.rows[id]
	if !ok || d.UserID != userID {
		return nil, fmt.Errorf("document: not found")
	}
	return d, nil
}

func (r *Repository) ListByKB(_ context.Context, userID, kbID int64) ([]*document.Document, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*document.Document
	for _, d := range r.rows {
		if d.UserID == userID && d.KBID == kbID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (r *Repository) UpdateStatus(_ context.Context, userID, id int64, status document.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.rows[id]
	if !ok || d.UserID != userID {
		return fmt.Errorf("document: not found")
	}
	d.Status = status
	d.UpdatedAt = time.Now()
	return nil
}

func (r *Repository) Delete(_ context.Context, userID, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.rows[id]
	if !ok || d.UserID != userID {
		return fmt.Errorf("document: not found")
	}
	delete(r.rows, id)
	return nil
}

// Compile-time check.
var _ document.Repository = (*Repository)(nil)
