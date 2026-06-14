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
	log.Printf("queue: DocumentUploaded{document_id=%s}", evt.DocumentID)
	return nil
}

var _ queue.Publisher = (*Publisher)(nil)
