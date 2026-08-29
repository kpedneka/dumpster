// Package manifest defines the domain types and persistence boundary for
// the ingestion manifest — a first-class artifact recording every
// classified region in a PDF or image document (native text/tables, figures
// described by the VLM, and detected-but-skipped scanned content). Having
// an explicit row per region, including skipped ones, is what makes "skip"
// mean something beyond silently dropping a region.
package manifest

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by Repository methods when the requested record
// does not exist or belongs to a different tenant.
var ErrNotFound = errors.New("manifest: not found")

// RegionType labels the content class a region was resolved to. The layered
// classifier's three steps (PDF metadata, layout model, VLM) route each
// region to exactly one of these types.
type RegionType string

const (
	// RegionTypeNativeText is a text block extracted directly from PDF
	// metadata without a model call (pdfplumber layer).
	RegionTypeNativeText RegionType = "native_text"
	// RegionTypeNativeTable is a table extracted directly from PDF
	// metadata without a model call (pdfplumber layer).
	RegionTypeNativeTable RegionType = "native_table"
	// RegionTypeFigure is an image/diagram detected by the layout model.
	// Marked skipped, not dropped: there is no text description to index
	// for it (see RegionClassificationHandler's type doc for why).
	RegionTypeFigure RegionType = "figure"
	// RegionTypeScannedText is text that the layout model suspects is
	// rasterized/scanned and so cannot be reliably extracted this pass.
	// Marked skipped, not dropped.
	RegionTypeScannedText RegionType = "scanned_text"
	// RegionTypeScannedTable is a table the layout model suspects is
	// rasterized/scanned and so cannot be reliably extracted this pass.
	// Marked skipped, not dropped.
	RegionTypeScannedTable RegionType = "scanned_table"
)

// Status records the outcome of classifying and extracting a region.
type Status string

const (
	// StatusIndexed means the region produced one or more searchable chunks.
	StatusIndexed Status = "indexed"
	// StatusSkipped means the region was detected but could not be
	// processed this pass (e.g. scanned content), and was intentionally
	// not dropped so it remains visible in per-document summaries.
	StatusSkipped Status = "skipped"
	// StatusFailed means classification or extraction encountered an error
	// that was not recoverable within this job attempt.
	StatusFailed Status = "failed"
)

// BoundingBox represents a region's position on the page as fractional
// coordinates in [0, 1], anchored at the top-left corner. Storing
// fractional coordinates makes them resolution-independent.
type BoundingBox struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// FullPage returns a BoundingBox covering the entire page — used when
// the source is a single-image upload with no sub-region information.
func FullPage() BoundingBox {
	return BoundingBox{X0: 0, Y0: 0, X1: 1, Y1: 1}
}

// Region is a single classified region in a PDF or image document, forming
// one row of the ingestion manifest. Regions with Status = StatusSkipped
// are persisted explicitly so callers can surface "N indexed, M skipped"
// summaries rather than having the skip be invisible.
type Region struct {
	ID               uuid.UUID
	DocumentID       uuid.UUID
	KBID             uuid.UUID
	UserID           uuid.UUID
	RegionType       RegionType
	PageNumber       int
	BoundingBox      BoundingBox
	Status           Status
	ExtractorVersion string
	CreatedAt        time.Time
}

// Repository is the persistence boundary for Region records. Every method
// is tenant-scoped, following the same multi-tenancy contract as
// entity.Repository.
type Repository interface {
	// BulkCreate persists regions in a single batch. Each region's UserID
	// must be set; ID is assigned by the repository.
	BulkCreate(ctx context.Context, regions []*Region) error
	// ListByDocument returns all regions for documentID, ordered by page
	// number then position (top-to-bottom, left-to-right).
	ListByDocument(ctx context.Context, userID, documentID uuid.UUID) ([]*Region, error)
	// DeleteByDocument removes all manifest rows for documentID. Used to
	// make re-running region classification idempotent.
	DeleteByDocument(ctx context.Context, userID, documentID uuid.UUID) error
}
