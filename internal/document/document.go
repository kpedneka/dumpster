package document

import (
	"context"
	"time"
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
	ID          int64
	KBID        int64
	UserID      int64
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
	Get(ctx context.Context, userID, id int64) (*Document, error)
	ListByKB(ctx context.Context, userID, kbID int64) ([]*Document, error)
	UpdateStatus(ctx context.Context, userID, id int64, status Status) error
	Delete(ctx context.Context, userID, id int64) error
}
