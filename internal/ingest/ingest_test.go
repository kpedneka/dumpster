package ingest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/ingest"
	"github.com/kunalpednekar/dumpster/internal/queue"
	qmem "github.com/kunalpednekar/dumpster/internal/queue/memory"
)

func TestNeedsRegionClassification(t *testing.T) {
	cases := map[string]bool{
		"application/pdf": true,
		"image/png":       true,
		"image/jpeg":      true,
		"text/plain":      false,
		"text/markdown":   false,
		"":                false,
	}
	for contentType, want := range cases {
		if got := ingest.NeedsRegionClassification(contentType); got != want {
			t.Errorf("NeedsRegionClassification(%q) = %v, want %v", contentType, got, want)
		}
	}
}

func TestStart_PDFGoesToRegionClassification(t *testing.T) {
	pub := qmem.New()
	doc := &document.Document{ID: uuid.New(), UserID: uuid.New(), ContentType: "application/pdf"}

	if err := ingest.Start(context.Background(), pub, doc); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := []queue.RegionClassificationRequested{{DocumentID: doc.ID, UserID: doc.UserID}}
	if got := pub.RegionClassificationEvents(); len(got) != 1 || got[0] != want[0] {
		t.Errorf("region classification events = %+v, want %+v", got, want)
	}
	if got := pub.Events(); len(got) != 0 {
		t.Errorf("document indexing events = %+v, want none", got)
	}
}

func TestStart_TextGoesToDocumentIndexing(t *testing.T) {
	pub := qmem.New()
	doc := &document.Document{ID: uuid.New(), UserID: uuid.New(), ContentType: "text/plain"}

	if err := ingest.Start(context.Background(), pub, doc); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := queue.DocumentUploaded{DocumentID: doc.ID, UserID: doc.UserID}
	if got := pub.Events(); len(got) != 1 || got[0] != want {
		t.Errorf("document indexing events = %+v, want [%+v]", got, want)
	}
	if got := pub.RegionClassificationEvents(); len(got) != 0 {
		t.Errorf("region classification events = %+v, want none", got)
	}
}

type failingPublisher struct{ queue.Publisher }

func (failingPublisher) PublishDocumentUploaded(context.Context, queue.DocumentUploaded) error {
	return errors.New("queue down")
}

func TestStart_ReturnsThePublishError(t *testing.T) {
	doc := &document.Document{ID: uuid.New(), UserID: uuid.New(), ContentType: "text/plain"}
	if err := ingest.Start(context.Background(), failingPublisher{}, doc); err == nil {
		t.Error("Start returned nil for a failed publish")
	}
}
