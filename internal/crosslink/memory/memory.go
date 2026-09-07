// Package memory provides an in-memory crosslink.Repository for use in
// tests.
package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/crosslink"
)

type kbKey struct {
	userID uuid.UUID
	kbID   uuid.UUID
}

type pairKey struct {
	entityAID, entityBID uuid.UUID
}

// Repository is an in-memory, tenant-scoped crosslink.Repository. Tests
// seed candidates directly via SeedCandidates, matching relation/memory's
// SeedCandidates precedent.
type Repository struct {
	mu         sync.Mutex
	candidates map[kbKey][]crosslink.Candidate
	resolved   map[uuid.UUID]map[pairKey]string // userID -> pair -> relation type
}

// New returns an empty in-memory Repository.
func New() *Repository {
	return &Repository{
		candidates: make(map[kbKey][]crosslink.Candidate),
		resolved:   make(map[uuid.UUID]map[pairKey]string),
	}
}

// SeedCandidates sets kbID's candidate chains directly, for tests.
func (r *Repository) SeedCandidates(userID, kbID uuid.UUID, candidates []crosslink.Candidate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.candidates[kbKey{userID, kbID}] = candidates
}

// CandidateChains returns the candidates seeded for kbID, excluding any
// pair already resolved via a prior ApplyUpdates call, capped at limit.
func (r *Repository) CandidateChains(_ context.Context, userID, kbID uuid.UUID, limit int) ([]crosslink.Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	resolved := r.resolved[userID]
	var out []crosslink.Candidate
	for _, c := range r.candidates[kbKey{userID, kbID}] {
		if _, done := resolved[pairKey{c.EntityAID, c.EntityBID}]; !done {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EntityAID.String() < out[j].EntityAID.String() })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ApplyUpdates records each update's relation type, for tests to assert
// against and for subsequent CandidateChains calls to exclude.
func (r *Repository) ApplyUpdates(_ context.Context, userID, _ uuid.UUID, updates []crosslink.Update) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolved[userID] == nil {
		r.resolved[userID] = make(map[pairKey]string)
	}
	for _, u := range updates {
		r.resolved[userID][pairKey{u.EntityAID, u.EntityBID}] = u.RelationType
	}
	return nil
}

// Resolved returns the relation type recorded for one pair, for test
// assertions -- ok is false if it was never resolved.
func (r *Repository) Resolved(userID, entityAID, entityBID uuid.UUID) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.resolved[userID][pairKey{entityAID, entityBID}]
	return v, ok
}

// ListConfirmed returns every seeded candidate resolved to a real (non-
// NoneRelation) relation type, with the seeded candidate's entity text.
func (r *Repository) ListConfirmed(_ context.Context, userID, kbID uuid.UUID) ([]crosslink.ConfirmedRelation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	resolved := r.resolved[userID]
	var out []crosslink.ConfirmedRelation
	for _, c := range r.candidates[kbKey{userID, kbID}] {
		rel, ok := resolved[pairKey{c.EntityAID, c.EntityBID}]
		if !ok || rel == crosslink.NoneRelation {
			continue
		}
		out = append(out, crosslink.ConfirmedRelation{
			EntityAText: c.EntityAText, EntityBText: c.EntityBText, RelationType: rel,
		})
	}
	return out, nil
}

// PurgeLowQuality removes every resolved relation for kbID whose seeded
// candidate text doesn't start with a capital letter on either side,
// mirroring the pgstore implementation's rule.
func (r *Repository) PurgeLowQuality(_ context.Context, userID, kbID uuid.UUID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	resolved := r.resolved[userID]
	if resolved == nil {
		return 0, nil
	}
	var deleted int
	for _, c := range r.candidates[kbKey{userID, kbID}] {
		if isCapitalized(c.EntityAText) && isCapitalized(c.EntityBText) {
			continue
		}
		key := pairKey{c.EntityAID, c.EntityBID}
		if _, ok := resolved[key]; ok {
			delete(resolved, key)
			deleted++
		}
	}
	return deleted, nil
}

// isCapitalized strips one leading "the"/"a"/"an" (case-insensitive)
// before checking the first letter -- extraction sometimes includes a
// leading article in the mention span ("the Harbor Beacon Leveler"), and
// checking the raw string would wrongly reject a legitimate proper noun
// over an included article. Mirrors the pgstore implementation's rule.
func isCapitalized(s string) bool {
	for _, article := range []string{"the ", "a ", "an "} {
		if len(s) > len(article) && strings.EqualFold(s[:len(article)], article) {
			s = s[len(article):]
			break
		}
	}
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

var _ crosslink.Repository = (*Repository)(nil)
