// Package entity defines the domain types and interfaces for local entity
// extraction: pulling named entities out of a document's existing chunks so
// they can be stored as raw material for a future graph-based retrieval
// feature. Extraction runs locally (no LLM call) to turn a variable
// per-document API cost into a fixed, sunk hardware cost.
package entity

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/chunk"
)

// ErrNotFound is returned by Repository methods when the requested record
// does not exist or belongs to a different tenant.
var ErrNotFound = errors.New("entity: not found")

// Type identifies the kind of real-world thing an entity mention refers to.
// The set of valid types is supplied by configuration (see
// internal/config), not hardcoded here, so adding a type requires no code
// change or migration.
type Type string

// Entity is a single mention of a named entity found within one chunk's
// text, located by the same byte offsets the chunk itself was stored with.
// Offsets are relative to the start of the chunk's Text, so combined with
// the chunk's own CharStart they can be resolved back to the source
// document for citation, mirroring how chunk offsets work.
type Entity struct {
	ID         uuid.UUID
	DocumentID uuid.UUID
	KBID       uuid.UUID
	UserID     uuid.UUID
	ChunkID    uuid.UUID
	// Type is one of the configured entity types (e.g. "person", "org").
	Type Type
	// Text is the literal mention text as it appears in the chunk.
	Text string
	// Start and End are byte offsets of the mention within the chunk's
	// text, satisfying 0 <= Start < End <= len(chunk.Text). Combined with
	// the chunk's CharStart they can be resolved to document-absolute
	// offsets the same way chunk citations are.
	Start int
	End   int
	// Score is the extractor's confidence for this mention, in [0, 1].
	// Extractors that do not produce a confidence score may leave this 0.
	Score float32
	// CanonicalEntityID links this mention to its resolved canonical
	// identity (internal/canonical.CanonicalEntity). Nil until the async
	// canonicalization job stage has run for the document this mention
	// belongs to.
	CanonicalEntityID *uuid.UUID
}

// Repository is the persistence boundary for Entity records. Every method
// is tenant-scoped, following the same multi-tenancy contract as
// chunk.Repository and document.Repository.
type Repository interface {
	// BulkCreate persists entities in a single batch. Each entity's UserID
	// must be set; ID is assigned by the repository.
	BulkCreate(ctx context.Context, entities []*Entity) error
	// ListByDocument returns all entities extracted for documentID, ordered
	// by chunk ordinal then offset.
	ListByDocument(ctx context.Context, userID, documentID uuid.UUID) ([]*Entity, error)
	// DeleteByDocument removes all entities for documentID. Used to make
	// re-running extraction on an already-processed document idempotent,
	// without touching that document's chunks or embeddings.
	DeleteByDocument(ctx context.Context, userID, documentID uuid.UUID) error
	// BulkSetCanonicalEntityID links each mention (keyed by mention ID) to
	// the canonical entity ID it was resolved to. Used by
	// internal/canonical.ResolveNew once canonicalization has run.
	BulkSetCanonicalEntityID(ctx context.Context, userID uuid.UUID, mentionToCanonical map[uuid.UUID]uuid.UUID) error
}

// Extractor runs entity extraction over a batch of chunks and returns the
// entities found, scoped to the provided allowed types. Implementations
// must not mutate or re-derive chunk text/offsets — extraction reads
// existing chunks, it never re-chunks.
//
// The only implementation that talks to a vendor/ML dependency is
// internal/entity/inference, which calls the consolidated ML inference
// service's /entities endpoint over HTTP; every other caller depends on
// this interface so units stay testable in isolation (see
// internal/entity/mock for the test double).
type Extractor interface {
	// Extract returns the entities found across chunks, restricted to the
	// given allowed entity types. The returned entities' ChunkID/DocumentID/
	// KBID/UserID fields are populated from the corresponding input chunk;
	// callers do not need to backfill them.
	Extract(ctx context.Context, chunks []*chunk.Chunk, allowedTypes []Type) ([]*Entity, error)
}
