// Package ingest decides how an uploaded document is processed and starts
// that processing. The upload handler, the retry endpoint and the malware
// scan's result handler all start a document the same way, so the choice
// lives here once.
package ingest

import (
	"context"

	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// regionClassificationTypes are the content types that go through region
// classification (layout analysis, then chunking and embedding) instead of
// plain-text document indexing.
var regionClassificationTypes = map[string]bool{
	"application/pdf": true,
	"image/png":       true,
	"image/jpeg":      true,
}

// NeedsRegionClassification reports whether a document of contentType is
// processed by region classification rather than document indexing.
func NeedsRegionClassification(contentType string) bool {
	return regionClassificationTypes[contentType]
}

// Start publishes the first processing job for doc: region classification
// for PDFs and images, document indexing for everything else.
func Start(ctx context.Context, publisher queue.Publisher, doc *document.Document) error {
	if NeedsRegionClassification(doc.ContentType) {
		return publisher.PublishRegionClassification(ctx, queue.RegionClassificationRequested{
			DocumentID: doc.ID, UserID: doc.UserID,
		})
	}
	return publisher.PublishDocumentUploaded(ctx, queue.DocumentUploaded{
		DocumentID: doc.ID, UserID: doc.UserID,
	})
}
