package objectstore

import (
	"context"
	"io"
	"time"
)

// ObjectStore is the boundary over blob storage (MinIO or Cloudflare R2
// locally, AWS S3 in staging/production).
type ObjectStore interface {
	// Put uploads r under key. size must be the exact byte count of r.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get returns a ReadCloser for the object at key. Caller must close it.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the object at key.
	Delete(ctx context.Context, key string) error
	// PresignedURL returns a time-limited GET URL for key.
	PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
	// PresignedPutURL returns a time-limited PUT URL for key, for a caller
	// without real credentials to upload directly (e.g. an AWS Batch job
	// writing its result) — the write-side counterpart to PresignedURL.
	PresignedPutURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}
