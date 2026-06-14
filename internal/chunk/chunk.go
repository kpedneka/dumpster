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
}

// Repository is the persistence boundary for Chunk records.
// Every method is tenant-scoped.
type Repository interface {
	BulkCreate(ctx context.Context, chunks []*Chunk) error
	ListByDocument(ctx context.Context, userID, documentID uuid.UUID) ([]*Chunk, error)
	ListByKB(ctx context.Context, userID, kbID uuid.UUID) ([]*Chunk, error)
	DeleteByDocument(ctx context.Context, userID, documentID uuid.UUID) error
}
