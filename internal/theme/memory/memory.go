// Package memory provides an in-memory theme.Repository for use in tests.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/theme"
)

type kbKey struct {
	userID uuid.UUID
	kbID   uuid.UUID
}

// Repository is an in-memory, tenant-scoped theme.Repository. Unlike the
// Postgres implementation, it has no canonical_entities table to derive
// community membership from — tests seed it directly via
// SeedCommunityMembers, matching community/memory's SeedGraph precedent.
type Repository struct {
	mu      sync.Mutex
	members map[kbKey][]theme.CommunityMembers
	results map[kbKey]theme.Result
}

// New returns an empty in-memory Repository.
func New() *Repository {
	return &Repository{
		members: make(map[kbKey][]theme.CommunityMembers),
		results: make(map[kbKey]theme.Result),
	}
}

// SeedCommunityMembers sets kbID's community membership directly, for
// tests — the in-memory fake has no canonical_entities table to derive it
// from the way the Postgres implementation does.
func (r *Repository) SeedCommunityMembers(userID, kbID uuid.UUID, members []theme.CommunityMembers) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members[kbKey{userID, kbID}] = members
}

// CommunityMembers returns the members seeded for kbID via SeedCommunityMembers.
func (r *Repository) CommunityMembers(_ context.Context, userID, kbID uuid.UUID) ([]theme.CommunityMembers, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]theme.CommunityMembers(nil), r.members[kbKey{userID, kbID}]...), nil
}

// SaveResult records themes and computedAt, overwriting kbID's previous
// result.
func (r *Repository) SaveResult(_ context.Context, userID, kbID uuid.UUID, themes []theme.Theme, computedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results[kbKey{userID, kbID}] = theme.Result{
		ComputedAt: computedAt,
		Themes:     append([]theme.Theme(nil), themes...),
	}
	return nil
}

// GetResult returns kbID's most recently saved result.
func (r *Repository) GetResult(_ context.Context, userID, kbID uuid.UUID) (*theme.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.results[kbKey{userID, kbID}]
	if !ok || len(res.Themes) == 0 {
		return nil, theme.ErrNoResult
	}
	cp := res
	return &cp, nil
}

var _ theme.Repository = (*Repository)(nil)
