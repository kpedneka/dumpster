// Package memory provides an in-memory intrusion.Repository for use in
// tests. It only implements the persistence half (SaveResult/GetResult) --
// the data-fetch half (MemberSource) is already covered by
// internal/theme/memory.Repository, which every real MemberSource caller
// also uses in production (see intrusion.MemberSource's own doc comment),
// so tests needing seeded community members should use that instead of
// duplicating it here.
package memory

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/intrusion"
)

type kbKey struct {
	userID uuid.UUID
	kbID   uuid.UUID
}

// Repository is an in-memory, tenant-scoped intrusion.Repository.
type Repository struct {
	mu      sync.Mutex
	results map[kbKey]intrusion.Result
}

// New returns an empty in-memory Repository.
func New() *Repository {
	return &Repository{results: make(map[kbKey]intrusion.Result)}
}

// SaveResult records result, overwriting kbID's previous one.
func (r *Repository) SaveResult(_ context.Context, userID, kbID uuid.UUID, result intrusion.Result) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := result
	cp.Communities = append([]intrusion.CommunityResult(nil), result.Communities...)
	r.results[kbKey{userID, kbID}] = cp
	return nil
}

// GetResult returns kbID's most recently saved result.
func (r *Repository) GetResult(_ context.Context, userID, kbID uuid.UUID) (*intrusion.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.results[kbKey{userID, kbID}]
	if !ok || len(res.Communities) == 0 {
		return nil, intrusion.ErrNoResult
	}
	cp := res
	cp.Communities = append([]intrusion.CommunityResult(nil), res.Communities...)
	return &cp, nil
}

var _ intrusion.Repository = (*Repository)(nil)
