// Package graphedge defines the domain types and persistence boundary for
// untyped co-occurrence edges between entity mentions (internal/entity)
// found in the same chunk — the ingestion-side foundation for GraphRAG.
// Edges are derived for free from chunks + entities already stored, no
// model call: this package's only job is recording which entity mentions
// appeared together, so query-time work (a later card) has a graph to
// traverse.
package graphedge

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by Repository methods when the requested record
// does not exist or belongs to a different tenant.
var ErrNotFound = errors.New("graphedge: not found")

// Edge is a co-occurrence relationship between two entity mentions
// (internal/entity.Entity) found within the same chunk. EntityAID and
// EntityBID reference individual mention rows, not deduplicated/canonical
// entities — v2.1's entity extraction has no canonicalization step, so an
// edge connects the exact mentions GLiNER found. EntityAID is always
// lexically less than EntityBID (by UUID string form, equivalent to
// Postgres's native uuid byte-order comparison) so the same pair found in
// either extraction order collapses to one row; see NewEdge.
type Edge struct {
	ID                uuid.UUID
	DocumentID        uuid.UUID
	KBID              uuid.UUID
	UserID            uuid.UUID
	ChunkID           uuid.UUID
	EntityAID         uuid.UUID
	EntityBID         uuid.UUID
	CoOccurrenceCount int
	// RelationType is nullable dial room for a future dependency-parse or
	// LLM pass to populate typed relations as an addition to existing rows,
	// not a migration. Unused by this package.
	RelationType *string
	CreatedAt    time.Time
}

// NewEdge returns an Edge between entityID1 and entityID2, canonicalizing
// pair order (the lexically smaller UUID becomes EntityAID) so callers
// never need to handle ordering themselves and a pair is represented
// identically regardless of which entity was found first.
func NewEdge(documentID, kbID, userID, chunkID, entityID1, entityID2 uuid.UUID) *Edge {
	a, b := entityID1, entityID2
	if a.String() > b.String() {
		a, b = b, a
	}
	return &Edge{
		DocumentID:        documentID,
		KBID:              kbID,
		UserID:            userID,
		ChunkID:           chunkID,
		EntityAID:         a,
		EntityBID:         b,
		CoOccurrenceCount: 1,
	}
}

// Repository is the persistence boundary for Edge records. Every method is
// tenant-scoped, following the same multi-tenancy contract as
// entity.Repository.
type Repository interface {
	// BulkCreate persists edges in a single batch. Each edge's UserID must
	// be set; ID is assigned by the repository.
	BulkCreate(ctx context.Context, edges []*Edge) error
	// ListByEntity returns every edge touching entityID, on either side of
	// the pair.
	ListByEntity(ctx context.Context, userID, entityID uuid.UUID) ([]*Edge, error)
	// DeleteByDocument removes all edges for documentID. Used to make
	// re-running edge extraction on an already-processed document
	// idempotent, without touching that document's entities or chunks.
	DeleteByDocument(ctx context.Context, userID, documentID uuid.UUID) error
}
