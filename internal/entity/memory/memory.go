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

// ChunkIDsWithEntities returns the set of chunk IDs that currently have at
// least one persisted entity for documentID belonging to userID.
func (r *Repository) ChunkIDsWithEntities(_ context.Context, userID, documentID uuid.UUID) (map[uuid.UUID]bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[uuid.UUID]bool)
	for _, e := range r.rows {
		if e.UserID == userID && e.DocumentID == documentID {
			out[e.ChunkID] = true
		}
	}
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

// BulkSetCanonicalEntityID links each mention in mentionToCanonical (keyed
// by mention ID) to its resolved canonical entity ID, scoped to userID.
// Mention IDs not belonging to userID (or not found) are silently skipped,
// matching the pgstore implementation's WHERE-scoped UPDATE semantics.
func (r *Repository) BulkSetCanonicalEntityID(_ context.Context, userID uuid.UUID, mentionToCanonical map[uuid.UUID]uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for mentionID, canonicalID := range mentionToCanonical {
		e, ok := r.rows[mentionID]
		if !ok || e.UserID != userID {
			continue
		}
		id := canonicalID
		e.CanonicalEntityID = &id
	}
	return nil
}

var _ entity.Repository = (*Repository)(nil)
