// Package memory provides an in-memory entity.Repository for use in tests.
package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/entity"
)

// Repository is an in-memory, tenant-scoped entity.Repository.
type Repository struct {
	mu   sync.RWMutex
	rows map[uuid.UUID]*entity.Entity
}

// New returns an empty in-memory Repository.
func New() *Repository {
	return &Repository{rows: make(map[uuid.UUID]*entity.Entity)}
}

// BulkCreate persists a copy of each entity, assigning it a new ID.
func (r *Repository) BulkCreate(_ context.Context, entities []*entity.Entity) error {
	if len(entities) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range entities {
		cp := *e
		cp.ID = uuid.New()
		r.rows[cp.ID] = &cp
	}
	return nil
}

// ListByDocument returns all entities for documentID belonging to userID,
// ordered for determinism (by chunk ID, then start offset).
func (r *Repository) ListByDocument(_ context.Context, userID, documentID uuid.UUID) ([]*entity.Entity, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*entity.Entity
	for _, e := range r.rows {
		if e.UserID == userID && e.DocumentID == documentID {
			cp := *e
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChunkID != out[j].ChunkID {
			return out[i].ChunkID.String() < out[j].ChunkID.String()
		}
		return out[i].Start < out[j].Start
	})
	return out, nil
}

// DeleteByDocument removes all entities for documentID belonging to userID.
func (r *Repository) DeleteByDocument(_ context.Context, userID, documentID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, e := range r.rows {
		if e.UserID == userID && e.DocumentID == documentID {
			delete(r.rows, id)
		}
	}
	return nil
}

var _ entity.Repository = (*Repository)(nil)
