package memory_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/queue/memory"
)

func TestPublishAndEvents(t *testing.T) {
	p := memory.New()

	if events := p.Events(); len(events) != 0 {
		t.Fatalf("initial events: got %d, want 0", len(events))
	}

	id1, id2 := uuid.New(), uuid.New()
	if err := p.PublishDocumentUploaded(context.Background(), queue.DocumentUploaded{DocumentID: id1}); err != nil {
		t.Fatal(err)
	}
	if err := p.PublishDocumentUploaded(context.Background(), queue.DocumentUploaded{DocumentID: id2}); err != nil {
		t.Fatal(err)
	}

	events := p.Events()
	if len(events) != 2 {
		t.Fatalf("events: got %d, want 2", len(events))
	}
	if events[0].DocumentID != id1 {
		t.Errorf("events[0] id mismatch")
	}
	if events[1].DocumentID != id2 {
		t.Errorf("events[1] id mismatch")
	}
}

func TestEvents_ReturnsCopy(t *testing.T) {
	p := memory.New()
	_ = p.PublishDocumentUploaded(context.Background(), queue.DocumentUploaded{DocumentID: uuid.New()})

	snap := p.Events()
	snap[0].DocumentID = uuid.Nil // mutate the copy

	if p.Events()[0].DocumentID == uuid.Nil {
		t.Error("Events() should return a copy, not a reference to internal slice")
	}
}

func TestPublishAndEntityExtractionEvents(t *testing.T) {
	p := memory.New()

	if events := p.EntityExtractionEvents(); len(events) != 0 {
		t.Fatalf("initial entity extraction events: got %d, want 0", len(events))
	}

	id1, id2 := uuid.New(), uuid.New()
	if err := p.PublishEntityExtraction(context.Background(), queue.EntityExtractionRequested{DocumentID: id1}); err != nil {
		t.Fatal(err)
	}
	if err := p.PublishEntityExtraction(context.Background(), queue.EntityExtractionRequested{DocumentID: id2}); err != nil {
		t.Fatal(err)
	}

	events := p.EntityExtractionEvents()
	if len(events) != 2 {
		t.Fatalf("entity extraction events: got %d, want 2", len(events))
	}
	if events[0].DocumentID != id1 || events[1].DocumentID != id2 {
		t.Errorf("entity extraction events mismatch: %v", events)
	}

	// Publishing entity extraction events must not affect document-uploaded events.
	if docEvents := p.Events(); len(docEvents) != 0 {
		t.Errorf("Events() should be unaffected by PublishEntityExtraction, got %d", len(docEvents))
	}
}
