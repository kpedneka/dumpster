package document

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by Repository methods when the requested record does
// not exist or belongs to a different tenant.
var ErrNotFound = errors.New("document: not found")

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
	ID          uuid.UUID `json:"id"`
	KBID        uuid.UUID `json:"kb_id"`
	UserID      uuid.UUID `json:"user_id"`
	Filename    string    `json:"filename"`
	S3Key       string    `json:"s3_key"`
	ContentType string    `json:"content_type"`
	Status      Status    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
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
