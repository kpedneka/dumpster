package document

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Status represents the processing lifecycle of a Document.
type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusIndexed    Status = "indexed"
	StatusFailed     Status = "failed"
)

// Document is a file stored in object storage and queued for indexing.
type Document struct {
	ID          uuid.UUID
	KBID        uuid.UUID
	UserID      uuid.UUID
	Filename    string
	S3Key       string
	ContentType string
	Status      Status
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Repository is the persistence boundary for Document records.
// Every method is tenant-scoped.
type Repository interface {
	Create(ctx context.Context, d *Document) (*Document, error)
	Get(ctx context.Context, userID, id uuid.UUID) (*Document, error)
	ListByKB(ctx context.Context, userID, kbID uuid.UUID) ([]*Document, error)
	UpdateStatus(ctx context.Context, userID, id uuid.UUID, status Status) error
	Delete(ctx context.Context, userID, id uuid.UUID) error
}
