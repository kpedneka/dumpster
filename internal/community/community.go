// Package community computes and stores Louvain community structure over a
// knowledge base's canonical-entity graph (see internal/canonical for what
// a canonical entity is). Detection itself (Graph, WeightedEdge, Louvain)
// is pure graph math with no persistence concerns — see louvain.go; this
// file is the persistence boundary and the per-run summary callers read
// back.
package community

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNoResult is returned by Repository.GetResult when a KB has never had
// community detection run for it.
var ErrNoResult = errors.New("community: no result")

// Result is one KB's most recent community-detection run. Community ids
// are arbitrary per-run integers with no meaning across separate runs, so
// Result carries only the run's summary — the per-entity assignment lives
// on canonical_entities itself.
type Result struct {
	KBID           uuid.UUID
	UserID         uuid.UUID
	ComputedAt     time.Time
	Modularity     float64
	CommunityCount int
	NodeCount      int
	EdgeCount      int
}

// Repository is the persistence boundary for community detection. Every
// method is tenant-scoped, following the same multi-tenancy contract as
// canonical.Repository.
type Repository interface {
	// CountCanonicalEntities returns the number of canonical entities in
	// kbID — the cheap pre-check callers use to decide whether it's safe to
	// build the full graph and run Louvain, without ever materializing a
	// graph for a KB that's too large.
	CountCanonicalEntities(ctx context.Context, userID, kbID uuid.UUID) (int, error)

	// KBGraph builds the canonical-entity graph for kbID: every canonical
	// entity in the KB as a node (including ones with no edges), and every
	// distinct pair of canonical entities that ever co-occur (via any
	// underlying mention pair in entity_edges) as one weighted edge, summed
	// across every contributing mention pair. Mentions not yet linked to a
	// canonical entity are excluded, matching this codebase's existing
	// pure-canonical precedent (see internal/graphrag/pgstore).
	KBGraph(ctx context.Context, userID, kbID uuid.UUID) (*Graph, error)

	// SaveResult persists assignments (canonical entity id -> community id)
	// onto canonical_entities and records run as kbID's new
	// community-detection summary, replacing whatever was there before —
	// community ids have no meaning across separate runs, so a full
	// overwrite is correct, not a merge.
	SaveResult(ctx context.Context, userID, kbID uuid.UUID, assignments map[uuid.UUID]int, run Result) error

	// GetResult returns kbID's most recent community-detection summary.
	// Returns ErrNoResult if detection has never run for this KB.
	GetResult(ctx context.Context, userID, kbID uuid.UUID) (*Result, error)
}
