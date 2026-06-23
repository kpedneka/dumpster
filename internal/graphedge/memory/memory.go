// Package memory provides an in-memory graphedge.Repository for use in tests.
package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/graphedge"
)

// Repository is an in-memory, tenant-scoped graphedge.Repository.
type Repository struct {
	mu   sync.RWMutex
	rows map[uuid.UUID]*graphedge.Edge
}

// New returns an empty in-memory Repository.
func New() *Repository {
	return &Repository{rows: make(map[uuid.UUID]*graphedge.Edge)}
}

// BulkCreate persists a copy of each edge, assigning it a new ID.
func (r *Repository) BulkCreate(_ context.Context, edges []*graphedge.Edge) error {
	if len(edges) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range edges {
		cp := *e
		cp.ID = uuid.New()
		r.rows[cp.ID] = &cp
	}
	return nil
}

// ListByEntity returns every edge touching entityID, on either side of the
// pair, ordered for determinism (by chunk ID).
func (r *Repository) ListByEntity(_ context.Context, userID, entityID uuid.UUID) ([]*graphedge.Edge, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*graphedge.Edge
	for _, e := range r.rows {
		if e.UserID == userID && (e.EntityAID == entityID || e.EntityBID == entityID) {
			cp := *e
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ChunkID.String() < out[j].ChunkID.String()
	})
	return out, nil
}

// DeleteByDocument removes all edges for documentID belonging to userID.
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

var _ graphedge.Repository = (*Repository)(nil)
