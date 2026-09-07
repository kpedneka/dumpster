package evalcorpus_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/evalcorpus"
	objmock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
	"github.com/kunalpednekar/dumpster/internal/queue"
	queuemem "github.com/kunalpednekar/dumpster/internal/queue/memory"
)

func TestIngestFile_CreatesDocumentUploadsObjectAndPublishes(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	pub := queuemem.New()
	userID, kbID := uuid.New(), uuid.New()

	ing := &evalcorpus.Ingester{Objects: objects, Documents: docs, Publisher: pub}

	doc, err := ing.IngestFile(context.Background(), userID, kbID, "maritime/01_biography.txt", []byte("Elena Voss was born in 1861."))
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}

	if doc.Filename != "maritime/01_biography.txt" {
		t.Errorf("Filename = %q, want the relative corpus path preserved for later lookup", doc.Filename)
	}
	if doc.UserID != userID || doc.KBID != kbID {
		t.Errorf("doc tenancy fields = (%s, %s), want (%s, %s)", doc.UserID, doc.KBID, userID, kbID)
	}
	if doc.Status != document.StatusPending {
		t.Errorf("Status = %q, want %q", doc.Status, document.StatusPending)
	}
	if doc.ContentType != "text/plain" {
		t.Errorf("ContentType = %q, want text/plain", doc.ContentType)
	}

	rc, err := objects.Get(context.Background(), doc.S3Key)
	if err != nil {
		t.Fatalf("object not stored at %q: %v", doc.S3Key, err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading stored object: %v", err)
	}
	if string(got) != "Elena Voss was born in 1861." {
		t.Errorf("stored object content = %q, want original file content", string(got))
	}

	events := pub.Events()
	if len(events) != 1 {
		t.Fatalf("published %d DocumentUploaded events, want 1", len(events))
	}
	if events[0].DocumentID != doc.ID || events[0].UserID != userID {
		t.Errorf("published event = %+v, want DocumentID=%s UserID=%s", events[0], doc.ID, userID)
	}
}

// failingPublisher wraps a real memory.Publisher but always fails
// PublishDocumentUploaded, to exercise IngestFile's failure path (marking
// the document Failed rather than leaving it Pending forever).
type failingPublisher struct {
	*queuemem.Publisher
}

func (failingPublisher) PublishDocumentUploaded(context.Context, queue.DocumentUploaded) error {
	return errors.New("publish boom")
}

func TestIngestFile_MarksDocumentFailedWhenPublishErrors(t *testing.T) {
	docs := docmem.New()
	ing := &evalcorpus.Ingester{
		Objects:   objmock.New(),
		Documents: docs,
		Publisher: failingPublisher{queuemem.New()},
	}
	userID, kbID := uuid.New(), uuid.New()

	_, err := ing.IngestFile(context.Background(), userID, kbID, "maritime/01_biography.txt", []byte("x"))
	if err == nil {
		t.Fatal("expected an error when publish fails, got nil")
	}

	docsList, listErr := docs.ListByKB(context.Background(), userID, kbID)
	if listErr != nil || len(docsList) != 1 {
		t.Fatalf("ListByKB = %+v, %v; want exactly 1 document", docsList, listErr)
	}
	if docsList[0].Status != document.StatusFailed {
		t.Errorf("Status = %q, want %q after a publish failure", docsList[0].Status, document.StatusFailed)
	}
}

func TestIngestFile_RejectsUnsupportedExtension(t *testing.T) {
	ing := &evalcorpus.Ingester{Objects: objmock.New(), Documents: docmem.New(), Publisher: queuemem.New()}
	_, err := ing.IngestFile(context.Background(), uuid.New(), uuid.New(), "notes.exe", []byte("x"))
	if err == nil {
		t.Fatal("expected an error for an unsupported extension, got nil")
	}
}

func TestIngestAll_ReturnsOneDocumentPerFile(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	pub := queuemem.New()
	ing := &evalcorpus.Ingester{Objects: objects, Documents: docs, Publisher: pub}
	userID, kbID := uuid.New(), uuid.New()

	files := map[string][]byte{
		"maritime/01_biography.txt": []byte("a"),
		"botany/01.txt":             []byte("b"),
	}

	results, err := ing.IngestAll(context.Background(), userID, kbID, files)
	if err != nil {
		t.Fatalf("IngestAll: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for relPath, doc := range results {
		if _, ok := files[relPath]; !ok {
			t.Errorf("unexpected relPath in results: %q", relPath)
		}
		if doc.Filename != relPath {
			t.Errorf("result[%q].Filename = %q, want %q", relPath, doc.Filename, relPath)
		}
	}
	if len(pub.Events()) != 2 {
		t.Fatalf("published %d events, want 2", len(pub.Events()))
	}
}
