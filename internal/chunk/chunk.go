package chunk

import "context"

// Chunk is a text segment extracted from a Document.
// Embedding is nil until the chunking & embedding pipeline populates it.
type Chunk struct {
	ID         int64
	DocumentID int64
	KBID       int64
	UserID     int64
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
	ListByDocument(ctx context.Context, userID, documentID int64) ([]*Chunk, error)
	ListByKB(ctx context.Context, userID, kbID int64) ([]*Chunk, error)
	DeleteByDocument(ctx context.Context, userID, documentID int64) error
}
