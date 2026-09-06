// Package memory provides an in-memory relation.Repository for use in
// tests.
package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/relation"
)

type kbKey struct {
	userID uuid.UUID
	kbID   uuid.UUID
}

type pairKey struct {
	chunkID              uuid.UUID
	entityAID, entityBID uuid.UUID
}

// Repository is an in-memory, tenant-scoped relation.Repository. Tests
// seed candidate chunks directly via SeedCandidates, matching
// community/memory's SeedGraph and theme/memory's SeedCommunityMembers
// precedent.
type Repository struct {
	mu         sync.Mutex
	candidates map[kbKey][]relation.ChunkCandidates
	resolved   map[uuid.UUID]map[pairKey]string // userID -> pair -> relation type
}

// New returns an empty in-memory Repository.
func New() *Repository {
	return &Repository{
		candidates: make(map[kbKey][]relation.ChunkCandidates),
		resolved:   make(map[uuid.UUID]map[pairKey]string),
	}
}

// SeedCandidates sets kbID's candidate chunks directly, for tests.
func (r *Repository) SeedCandidates(userID, kbID uuid.UUID, chunks []relation.ChunkCandidates) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.candidates[kbKey{userID, kbID}] = chunks
}

// CandidateChunks returns the chunks seeded for kbID, excluding any pair
// already resolved via a prior ApplyUpdates call -- mirroring the
// Postgres implementation's relation_type IS NULL filter -- and capped at
// limit chunks.
func (r *Repository) CandidateChunks(_ context.Context, userID, kbID uuid.UUID, limit int) ([]relation.ChunkCandidates, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	resolved := r.resolved[userID]
	var out []relation.ChunkCandidates
	for _, cc := range r.candidates[kbKey{userID, kbID}] {
		var pairs []relation.EdgePair
		for _, p := range cc.Pairs {
			if _, done := resolved[pairKey{cc.ChunkID, p.EntityAID, p.EntityBID}]; !done {
				pairs = append(pairs, p)
			}
		}
		if len(pairs) == 0 {
			continue
		}
		out = append(out, relation.ChunkCandidates{ChunkID: cc.ChunkID, Text: cc.Text, Pairs: pairs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChunkID.String() < out[j].ChunkID.String() })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ApplyUpdates records each update's relation type, for tests to assert
// against and for subsequent CandidateChunks calls to exclude.
func (r *Repository) ApplyUpdates(_ context.Context, userID uuid.UUID, updates []relation.Update) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolved[userID] == nil {
		r.resolved[userID] = make(map[pairKey]string)
	}
	for _, u := range updates {
		r.resolved[userID][pairKey{u.ChunkID, u.EntityAID, u.EntityBID}] = u.RelationType
	}
	return nil
}

// Resolved returns the relation type recorded for one pair, for test
// assertions -- ok is false if it was never resolved.
func (r *Repository) Resolved(userID, chunkID, entityAID, entityBID uuid.UUID) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.resolved[userID][pairKey{chunkID, entityAID, entityBID}]
	return v, ok
}

var _ relation.Repository = (*Repository)(nil)
