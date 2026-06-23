// Package memory provides an in-memory manifest.Repository for use in tests.
package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/manifest"
)

// Repository is an in-memory, tenant-scoped manifest.Repository.
type Repository struct {
	mu   sync.RWMutex
	rows map[uuid.UUID]*manifest.Region
}

// New returns an empty in-memory Repository.
func New() *Repository {
	return &Repository{rows: make(map[uuid.UUID]*manifest.Region)}
}

// BulkCreate persists a copy of each region, assigning it a new ID.
func (r *Repository) BulkCreate(_ context.Context, regions []*manifest.Region) error {
	if len(regions) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, reg := range regions {
		cp := *reg
		cp.ID = uuid.New()
		r.rows[cp.ID] = &cp
	}
	return nil
}

// ListByDocument returns all regions for documentID belonging to userID,
// ordered by page number (ascending) and Y0 within the same page.
func (r *Repository) ListByDocument(_ context.Context, userID, documentID uuid.UUID) ([]*manifest.Region, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*manifest.Region
	for _, reg := range r.rows {
		if reg.UserID == userID && reg.DocumentID == documentID {
			cp := *reg
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PageNumber != out[j].PageNumber {
			return out[i].PageNumber < out[j].PageNumber
		}
		return out[i].BoundingBox.Y0 < out[j].BoundingBox.Y0
	})
	return out, nil
}

// DeleteByDocument removes all regions for documentID belonging to userID.
func (r *Repository) DeleteByDocument(_ context.Context, userID, documentID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, reg := range r.rows {
		if reg.UserID == userID && reg.DocumentID == documentID {
			delete(r.rows, id)
		}
	}
	return nil
}

var _ manifest.Repository = (*Repository)(nil)
