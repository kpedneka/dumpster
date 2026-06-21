package noop_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/queue/noop"
)

func TestPublishDocumentUploaded(t *testing.T) {
	p := noop.New()
	err := p.PublishDocumentUploaded(context.Background(), queue.DocumentUploaded{DocumentID: uuid.New()})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPublishEntityExtraction(t *testing.T) {
	p := noop.New()
	err := p.PublishEntityExtraction(context.Background(), queue.EntityExtractionRequested{DocumentID: uuid.New()})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImplementsPublisher(t *testing.T) {
	var _ queue.Publisher = noop.New()
}
