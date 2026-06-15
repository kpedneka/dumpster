// Package memory provides an in-memory kb.Repository for use in tests.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/kb"
)

type Repository struct {
	mu   sync.RWMutex
	rows map[uuid.UUID]*kb.KnowledgeBase
}

func New() *Repository {
	return &Repository{rows: make(map[uuid.UUID]*kb.KnowledgeBase)}
}

func (r *Repository) Create(_ context.Context, userID uuid.UUID, name string) (*kb.KnowledgeBase, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	k := &kb.KnowledgeBase{ID: uuid.New(), UserID: userID, Name: name, CreatedAt: now, UpdatedAt: now}
	r.rows[k.ID] = k
	return k, nil
}

func (r *Repository) Get(_ context.Context, userID, id uuid.UUID) (*kb.KnowledgeBase, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.rows[id]
	if !ok || k.UserID != userID {
		return nil, kb.ErrNotFound
	}
	return k, nil
}

func (r *Repository) List(_ context.Context, userID uuid.UUID) ([]*kb.KnowledgeBase, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*kb.KnowledgeBase
	for _, k := range r.rows {
		if k.UserID == userID {
			out = append(out, k)
		}
	}
	return out, nil
}

func (r *Repository) Rename(_ context.Context, userID, id uuid.UUID, name string) (*kb.KnowledgeBase, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.rows[id]
	if !ok || k.UserID != userID {
		return nil, kb.ErrNotFound
	}
	k.Name = name
	k.UpdatedAt = time.Now()
	return k, nil
}

func (r *Repository) Delete(_ context.Context, userID, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.rows[id]
	if !ok || k.UserID != userID {
		return kb.ErrNotFound
	}
	delete(r.rows, id)
	return nil
}

var _ kb.Repository = (*Repository)(nil)
