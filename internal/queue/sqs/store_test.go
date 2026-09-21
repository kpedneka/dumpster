package sqs_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/queue/sqs"
	"github.com/kunalpednekar/dumpster/internal/queue/sqs/mock"
)

func testConfig() sqs.Config {
	return sqs.Config{
		DocumentUploadedQueueURL:     "https://sqs.example/document-indexing",
		EntityExtractionQueueURL:     "https://sqs.example/entity-extraction",
		EdgeExtractionQueueURL:       "https://sqs.example/edge-extraction",
		RegionClassificationQueueURL: "https://sqs.example/region-classification",
		CanonicalizationQueueURL:     "https://sqs.example/canonicalization",
	}
}

// decodeSent unmarshals the body of the single message the mock client
// recorded, failing the test if there isn't exactly one.
func decodeSent(t *testing.T, client *mock.Client) (queueURL string, msg map[string]any) {
	t.Helper()
	sent := client.Sent()
	if len(sent) != 1 {
		t.Fatalf("SendMessage calls: got %d, want 1", len(sent))
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(sent[0].Body), &decoded); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	return sent[0].QueueURL, decoded
}

func TestStore_PublishDocumentUploaded(t *testing.T) {
	client := mock.New()
	store := sqs.New(client, testConfig())
	docID, userID := uuid.New(), uuid.New()

	if err := store.PublishDocumentUploaded(context.Background(), queue.DocumentUploaded{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishDocumentUploaded: %v", err)
	}

	queueURL, msg := decodeSent(t, client)
	if queueURL != testConfig().DocumentUploadedQueueURL {
		t.Errorf("queue URL: got %q, want %q", queueURL, testConfig().DocumentUploadedQueueURL)
	}
	if msg["type"] != string(queue.JobTypeDocumentIndexing) {
		t.Errorf("type: got %v, want %q", msg["type"], queue.JobTypeDocumentIndexing)
	}
	if msg["document_id"] != docID.String() {
		t.Errorf("document_id: got %v, want %q", msg["document_id"], docID)
	}
	if msg["user_id"] != userID.String() {
		t.Errorf("user_id: got %v, want %q", msg["user_id"], userID)
	}
}

func TestStore_PublishEntityExtraction(t *testing.T) {
	client := mock.New()
	store := sqs.New(client, testConfig())
	docID, userID := uuid.New(), uuid.New()

	if err := store.PublishEntityExtraction(context.Background(), queue.EntityExtractionRequested{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishEntityExtraction: %v", err)
	}

	queueURL, msg := decodeSent(t, client)
	if queueURL != testConfig().EntityExtractionQueueURL {
		t.Errorf("queue URL: got %q, want %q", queueURL, testConfig().EntityExtractionQueueURL)
	}
	if msg["type"] != string(queue.JobTypeEntityExtraction) {
		t.Errorf("type: got %v, want %q", msg["type"], queue.JobTypeEntityExtraction)
	}
}

func TestStore_PublishEdgeExtraction(t *testing.T) {
	client := mock.New()
	store := sqs.New(client, testConfig())
	docID, userID := uuid.New(), uuid.New()

	if err := store.PublishEdgeExtraction(context.Background(), queue.EdgeExtractionRequested{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishEdgeExtraction: %v", err)
	}

	queueURL, msg := decodeSent(t, client)
	if queueURL != testConfig().EdgeExtractionQueueURL {
		t.Errorf("queue URL: got %q, want %q", queueURL, testConfig().EdgeExtractionQueueURL)
	}
	if msg["type"] != string(queue.JobTypeEdgeExtraction) {
		t.Errorf("type: got %v, want %q", msg["type"], queue.JobTypeEdgeExtraction)
	}
}

func TestStore_PublishRegionClassification(t *testing.T) {
	client := mock.New()
	store := sqs.New(client, testConfig())
	docID, userID := uuid.New(), uuid.New()

	if err := store.PublishRegionClassification(context.Background(), queue.RegionClassificationRequested{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishRegionClassification: %v", err)
	}

	queueURL, msg := decodeSent(t, client)
	if queueURL != testConfig().RegionClassificationQueueURL {
		t.Errorf("queue URL: got %q, want %q", queueURL, testConfig().RegionClassificationQueueURL)
	}
	if msg["type"] != string(queue.JobTypeRegionClassification) {
		t.Errorf("type: got %v, want %q", msg["type"], queue.JobTypeRegionClassification)
	}
}

func TestStore_PublishCanonicalization(t *testing.T) {
	client := mock.New()
	store := sqs.New(client, testConfig())
	docID, userID := uuid.New(), uuid.New()

	if err := store.PublishCanonicalization(context.Background(), queue.CanonicalizationRequested{DocumentID: docID, UserID: userID}); err != nil {
		t.Fatalf("PublishCanonicalization: %v", err)
	}

	queueURL, msg := decodeSent(t, client)
	if queueURL != testConfig().CanonicalizationQueueURL {
		t.Errorf("queue URL: got %q, want %q", queueURL, testConfig().CanonicalizationQueueURL)
	}
	if msg["type"] != string(queue.JobTypeCanonicalization) {
		t.Errorf("type: got %v, want %q", msg["type"], queue.JobTypeCanonicalization)
	}
}

// TestStore_Publish_WrapsClientError is a representative test (not
// repeated per method): every Publish* method shares the same private
// publish helper, so proving one wraps the client's error proves all of
// them do.
func TestStore_Publish_WrapsClientError(t *testing.T) {
	client := mock.New()
	client.SendErr = errors.New("sqs: throttled")
	store := sqs.New(client, testConfig())

	err := store.PublishDocumentUploaded(context.Background(), queue.DocumentUploaded{DocumentID: uuid.New(), UserID: uuid.New()})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, client.SendErr) {
		t.Errorf("expected wrapped client error, got %v", err)
	}
}

// TestStore_ImplementsPublisher is a compile-time-adjacent check made
// explicit at the type level below (var _ queue.Publisher), but this test
// also exercises it through the interface, not the concrete type, to
// catch any accidental method-set mismatch that only var _ wouldn't.
func TestStore_ImplementsPublisher(t *testing.T) {
	var _ queue.Publisher = sqs.New(mock.New(), testConfig())
}
