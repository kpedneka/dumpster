// Package memory provides an in-memory kb.Repository for use in tests.
package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kunalpednekar/dumpster/internal/kb"
)

type Repository struct {
	mu   sync.RWMutex
	rows map[int64]*kb.KnowledgeBase
	next int64
}

func New() *Repository {
	return &Repository{rows: make(map[int64]*kb.KnowledgeBase), next: 1}
}

func (r *Repository) Create(_ context.Context, userID int64, name string) (*kb.KnowledgeBase, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	k := &kb.KnowledgeBase{ID: r.next, UserID: userID, Name: name, CreatedAt: now, UpdatedAt: now}
	r.rows[r.next] = k
	r.next++
	return k, nil
}

func (r *Repository) Get(_ context.Context, userID, id int64) (*kb.KnowledgeBase, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.rows[id]
	if !ok || k.UserID != userID {
		return nil, fmt.Errorf("kb: not found")
	}
	return k, nil
}

func (r *Repository) List(_ context.Context, userID int64) ([]*kb.KnowledgeBase, error) {
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

func (r *Repository) Delete(_ context.Context, userID, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.rows[id]
	if !ok || k.UserID != userID {
		return fmt.Errorf("kb: not found")
	}
	delete(r.rows, id)
	return nil
}

// Compile-time check.
var _ kb.Repository = (*Repository)(nil)
