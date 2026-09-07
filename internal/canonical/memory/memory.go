// Package memory provides an in-memory canonical.Repository for use in tests.
package memory

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/canonical"
	"github.com/kunalpednekar/dumpster/internal/entity"
)

// Repository is an in-memory, tenant-scoped canonical.Repository.
type Repository struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*canonical.CanonicalEntity
	// docContribution tracks, per (userID, documentID), how many mentions
	// each canonical entity currently owes to that document — the in-memory
	// analogue of the pgstore implementation's entities.canonical_entity_id
	// join, needed by DecrementForDocument.
	docContribution map[docKey]map[uuid.UUID]int
}

type docKey struct {
	userID     uuid.UUID
	documentID uuid.UUID
}

// New returns an empty in-memory Repository.
func New() *Repository {
	return &Repository{
		rows:            make(map[uuid.UUID]*canonical.CanonicalEntity),
		docContribution: make(map[docKey]map[uuid.UUID]int),
	}
}

// Canonicalize resolves each mention to a canonical entity, creating one on
// first sight per (kb_id, user_id, normalized_text, entity_type), and
// increments MentionCount per mention / DocumentCount once per distinct
// canonical entity touched in this call.
func (r *Repository) Canonicalize(_ context.Context, mentions []*entity.Entity) (map[uuid.UUID]uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	resolved := make(map[uuid.UUID]uuid.UUID, len(mentions))
	touchedThisCall := make(map[uuid.UUID]bool)

	for _, m := range mentions {
		normalized := canonical.Normalize(m.Text)
		ce := r.findLocked(m.KBID, m.UserID, normalized, m.Type)
		if ce == nil {
			ce = &canonical.CanonicalEntity{
				ID:             uuid.New(),
				KBID:           m.KBID,
				UserID:         m.UserID,
				CanonicalText:  m.Text,
				NormalizedText: normalized,
				Type:           m.Type,
				CreatedAt:      time.Now(),
			}
			r.rows[ce.ID] = ce
		}
		ce.MentionCount++
		resolved[m.ID] = ce.ID

		key := docKey{userID: m.UserID, documentID: m.DocumentID}
		if r.docContribution[key] == nil {
			r.docContribution[key] = make(map[uuid.UUID]int)
		}
		r.docContribution[key][ce.ID]++

		if !touchedThisCall[ce.ID] {
			touchedThisCall[ce.ID] = true
			ce.DocumentCount++
		}
	}
	return resolved, nil
}

func (r *Repository) findLocked(kbID, userID uuid.UUID, normalized string, t entity.Type) *canonical.CanonicalEntity {
	for _, ce := range r.rows {
		if ce.KBID == kbID && ce.UserID == userID && ce.NormalizedText == normalized && ce.Type == t {
			return ce
		}
	}
	return nil
}

// DecrementForDocument reverses documentID's current contribution to
// canonical entity stats.
func (r *Repository) DecrementForDocument(_ context.Context, userID, documentID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := docKey{userID: userID, documentID: documentID}
	contributions, ok := r.docContribution[key]
	if !ok {
		return nil
	}
	for canonicalID, count := range contributions {
		ce, ok := r.rows[canonicalID]
		if !ok {
			continue
		}
		ce.MentionCount -= count
		ce.DocumentCount--
		if ce.MentionCount <= 0 {
			delete(r.rows, canonicalID)
		}
	}
	delete(r.docContribution, key)
	return nil
}

// Get returns the canonical entity with the given id, scoped to userID.
func (r *Repository) Get(_ context.Context, userID, id uuid.UUID) (*canonical.CanonicalEntity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ce, ok := r.rows[id]
	if !ok || ce.UserID != userID {
		return nil, canonical.ErrNotFound
	}
	cp := *ce
	return &cp, nil
}

// ListByKB returns every canonical entity in kbID belonging to userID.
func (r *Repository) ListByKB(_ context.Context, userID, kbID uuid.UUID) ([]*canonical.CanonicalEntity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*canonical.CanonicalEntity
	for _, ce := range r.rows {
		if ce.UserID == userID && ce.KBID == kbID {
			cp := *ce
			out = append(out, &cp)
		}
	}
	return out, nil
}

// FuzzyCandidates returns existing canonical entities of entityType in
// kbID (excluding excludeID) whose normalized text is a whitespace-token
// subset of normalizedText, or vice versa.
func (r *Repository) FuzzyCandidates(_ context.Context, userID, kbID uuid.UUID, normalizedText string, entityType entity.Type, excludeID uuid.UUID) ([]canonical.AliasCandidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	target := tokenSet(normalizedText)
	var out []canonical.AliasCandidate
	for _, ce := range r.rows {
		if ce.UserID != userID || ce.KBID != kbID || ce.Type != entityType || ce.ID == excludeID {
			continue
		}
		if ce.NormalizedText == normalizedText {
			continue // exact match, not a fuzzy case
		}
		other := tokenSet(ce.NormalizedText)
		if isSubset(target, other) || isSubset(other, target) {
			out = append(out, canonical.AliasCandidate{
				ID: ce.ID, CanonicalText: ce.CanonicalText, NormalizedText: ce.NormalizedText,
				MentionCount: ce.MentionCount, CreatedAt: ce.CreatedAt,
			})
		}
	}
	return out, nil
}

func tokenSet(s string) map[string]bool {
	set := make(map[string]bool)
	for _, tok := range strings.Fields(s) {
		set[tok] = true
	}
	return set
}

func isSubset(a, b map[string]bool) bool {
	for tok := range a {
		if !b[tok] {
			return false
		}
	}
	return true
}

// MergeInto repoints fromID's mentions onto toID, adds its stats onto
// toID, and deletes fromID.
func (r *Repository) MergeInto(_ context.Context, userID, fromID, toID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	from, ok := r.rows[fromID]
	if !ok || from.UserID != userID {
		return canonical.ErrNotFound
	}
	to, ok := r.rows[toID]
	if !ok || to.UserID != userID {
		return canonical.ErrNotFound
	}

	to.MentionCount += from.MentionCount
	to.DocumentCount += from.DocumentCount

	for key, contributions := range r.docContribution {
		if key.userID != userID {
			continue
		}
		if cnt, ok := contributions[fromID]; ok {
			contributions[toID] += cnt
			delete(contributions, fromID)
		}
	}

	delete(r.rows, fromID)
	return nil
}

var _ canonical.Repository = (*Repository)(nil)
