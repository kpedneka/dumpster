package chunk

import (
	"context"

	"github.com/google/uuid"
)

// Chunk is a text segment extracted from a Document.
// Embedding is nil until the chunking & embedding pipeline populates it.
type Chunk struct {
	ID         uuid.UUID
	DocumentID uuid.UUID
	KBID       uuid.UUID
	UserID     uuid.UUID
	Ordinal    int
	Text       string
	TokenCount int
	Embedding  []float32
	CharStart  int
	CharEnd    int
	// Region provenance — only set for chunks derived from PDF/image region
	// classification. Nil for text/markdown chunks which have no manifest region.
	RegionID    *uuid.UUID
	PageNumber  *int
	BoundingBox *BoundingBox
}

// BoundingBox holds fractional page coordinates [0,1]: top-left (X0,Y0)
// to bottom-right (X1,Y1). Stored as JSONB in Postgres; nil for chunks
// that have no page-region provenance.
type BoundingBox struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// Repository is the persistence boundary for Chunk records.
// Every method is tenant-scoped.
type Repository interface {
	BulkCreate(ctx context.Context, chunks []*Chunk) error
	ListByDocument(ctx context.Context, userID, documentID uuid.UUID) ([]*Chunk, error)
	ListByKB(ctx context.Context, userID, kbID uuid.UUID) ([]*Chunk, error)
	DeleteByDocument(ctx context.Context, userID, documentID uuid.UUID) error
	// UpdateEmbedding sets a chunk's embedding vector. Used by the
	// local-embeddings backfill (internal/reembed) to populate vectors for
	// chunks whose embedding was cleared by an embedding-model migration.
	UpdateEmbedding(ctx context.Context, userID, id uuid.UUID, embedding []float32) error
}
