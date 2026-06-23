// Package noop provides a queue.Publisher that logs events instead of
// enqueuing them. Use this until a concrete queue backend is wired up.
package noop

import (
	"context"
	"log"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Publisher satisfies queue.Publisher by logging each event.
type Publisher struct{}

// New returns a no-op Publisher.
func New() *Publisher { return &Publisher{} }

// PublishDocumentUploaded logs the event instead of enqueuing it.
func (p *Publisher) PublishDocumentUploaded(_ context.Context, evt queue.DocumentUploaded) error {
	log.Printf("queue: DocumentUploaded{document_id=%s user_id=%s}", evt.DocumentID, evt.UserID)
	return nil
}

// PublishEntityExtraction logs the event instead of enqueuing it.
func (p *Publisher) PublishEntityExtraction(_ context.Context, evt queue.EntityExtractionRequested) error {
	log.Printf("queue: EntityExtractionRequested{document_id=%s user_id=%s}", evt.DocumentID, evt.UserID)
	return nil
}

// PublishEdgeExtraction logs the event instead of enqueuing it.
func (p *Publisher) PublishEdgeExtraction(_ context.Context, evt queue.EdgeExtractionRequested) error {
	log.Printf("queue: EdgeExtractionRequested{document_id=%s user_id=%s}", evt.DocumentID, evt.UserID)
	return nil
}

var _ queue.Publisher = (*Publisher)(nil)
