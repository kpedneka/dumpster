// Package canonical defines the domain types and persistence boundary for
// canonical entities: a stable identity for "this entity" that survives
// across chunks and documents within a knowledge base. entity.Entity rows
// are per-mention with no dedup, so query-time graph traversal (a later
// card) has nothing stable to hop on today; this package is that missing
// identity layer, resolved from mentions by exact normalized-text+type
// matching (fuzzy/embedding-based matching is a deliberate later upgrade).
package canonical

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	"github.com/kunalpednekar/dumpster/internal/entity"
)

// ErrNotFound is returned by Repository methods when the requested record
// does not exist or belongs to a different tenant.
var ErrNotFound = errors.New("canonical: not found")

// CanonicalEntity is the deduplicated identity that one or more
// entity.Entity mentions, across any number of chunks and documents in a
// KB, resolve to.
type CanonicalEntity struct {
	ID     uuid.UUID
	KBID   uuid.UUID
	UserID uuid.UUID
	// CanonicalText is a display form — the text of the first mention seen
	// for this identity. It is not re-derived on later mentions, so it stays
	// stable even as MentionCount grows.
	CanonicalText string
	// NormalizedText is the matching key mentions are deduped against (see
	// Normalize). It, together with EntityType/KBID/UserID, is what the
	// database's unique identity constraint is keyed on.
	NormalizedText string
	Type           entity.Type
	// MentionCount is the total number of mentions resolved to this
	// identity, across every document that currently contributes to it.
	MentionCount int
	// DocumentCount is the number of distinct documents currently
	// contributing at least one mention to this identity.
	DocumentCount int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Normalize maps mention text to the matching key two mentions are
// considered "the same entity" by: Unicode NFKC normalization (so
// visually/semantically equivalent but differently-encoded text, e.g.
// combining-character forms, collapses together), lowercased, with
// leading/trailing whitespace trimmed and interior whitespace runs
// collapsed to a single space. It deliberately does not strip punctuation —
// "Apple, Inc." and "Apple Inc" are left as distinct identities, matching
// this package's exact-match (not fuzzy) scope.
func Normalize(text string) string {
	folded := norm.NFKC.String(strings.ToLower(text))
	return strings.Join(strings.FieldsFunc(folded, unicode.IsSpace), " ")
}

// Repository is the persistence boundary for CanonicalEntity records. Every
// method is tenant-scoped, following the same multi-tenancy contract as
// entity.Repository.
type Repository interface {
	// Canonicalize resolves each of mentions to a canonical entity —
	// creating one on first sight per unique (kb_id, user_id,
	// normalized_text, entity_type) — incrementing MentionCount once per
	// mention and DocumentCount once per distinct canonical entity touched
	// by this call (not once per mention). Returns the resolved canonical
	// entity ID keyed by mention ID.
	//
	// Callers must not pass a mention more than once across calls without
	// an intervening DecrementForDocument, or counts double-count; use
	// ResolveNew, which filters out already-resolved mentions, instead of
	// calling this directly from a retryable context.
	Canonicalize(ctx context.Context, mentions []*entity.Entity) (map[uuid.UUID]uuid.UUID, error)

	// DecrementForDocument reverses documentID's current contribution to
	// canonical entity stats: for every canonical entity linked from one of
	// the document's existing mentions, decrements MentionCount by that
	// document's mention count for it and DocumentCount by 1, deleting the
	// canonical row if MentionCount reaches zero. It is a no-op for a
	// document with no canonicalized mentions (e.g. a first-ever run).
	//
	// Must be called before the document's entities are deleted (explicit
	// document delete, or entity-extraction re-run) — the mention →
	// canonical linkage this needs to reverse is gone once those rows are.
	DecrementForDocument(ctx context.Context, userID, documentID uuid.UUID) error

	// Get returns the canonical entity with the given id, scoped to userID.
	Get(ctx context.Context, userID, id uuid.UUID) (*CanonicalEntity, error)

	// ListByKB returns every canonical entity in kbID belonging to userID.
	ListByKB(ctx context.Context, userID, kbID uuid.UUID) ([]*CanonicalEntity, error)

	// FuzzyCandidates returns existing canonical entities in kbID (of the
	// same entityType, excluding excludeID) whose normalized text is a
	// whitespace-token subset of normalizedText, or vice versa -- e.g.
	// "kade" is a token subset of "rosalind kade". Exact matches are
	// excluded by construction (Canonicalize already merges those); this
	// is specifically for the case Canonicalize structurally cannot catch:
	// a real alias with different text (a surname alone, an abbreviation
	// already spelled out elsewhere). Candidates, not confirmed merges --
	// see ResolveAliases for the LLM-confirm step before anything merges.
	FuzzyCandidates(ctx context.Context, userID, kbID uuid.UUID, normalizedText string, entityType entity.Type, excludeID uuid.UUID) ([]AliasCandidate, error)

	// MergeInto repoints every entity currently linked to fromID onto toID,
	// adds fromID's MentionCount/DocumentCount onto toID, and deletes
	// fromID. Both must belong to userID; the caller (ResolveAliases) is
	// responsible for having already confirmed this merge is correct --
	// this method does no judgment of its own.
	MergeInto(ctx context.Context, userID, fromID, toID uuid.UUID) error
}

// AliasCandidate is an existing canonical entity FuzzyCandidates proposes
// as a possible alias of another -- not yet confirmed as the same
// real-world entity.
type AliasCandidate struct {
	ID             uuid.UUID
	CanonicalText  string
	NormalizedText string
	MentionCount   int
	CreatedAt      time.Time
}

// ResolveNew canonicalizes whichever of mentions are not yet linked to a
// canonical entity (CanonicalEntityID == nil) via repo.Canonicalize, then
// persists the resulting links back onto the corresponding entities rows.
// Already-linked mentions are skipped, which is what makes this safe to
// call again for the same document — e.g. from a redelivered queue job —
// without double-counting a mention that was already resolved.
//
// Returns the mention-ID -> canonical-ID map Canonicalize produced (nil if
// there was nothing pending), so a caller can feed it straight into
// ResolveAliases without a second lookup.
func ResolveNew(ctx context.Context, repo Repository, entities entity.Repository, userID uuid.UUID, mentions []*entity.Entity) (map[uuid.UUID]uuid.UUID, error) {
	var pending []*entity.Entity
	for _, m := range mentions {
		if m.CanonicalEntityID == nil {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}

	resolved, err := repo.Canonicalize(ctx, pending)
	if err != nil {
		return nil, err
	}

	if err := entities.BulkSetCanonicalEntityID(ctx, userID, resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}
