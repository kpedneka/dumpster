// Package sqs provides an SQS-backed implementation of queue.Publisher —
// the publish half of the event-driven job orchestration replacing the ECS
// worker's Postgres-poll loop (see the "Event-Driven Job Orchestration"
// System Architecture page and its dev board for the full design and why).
//
// Consumer is deliberately not implemented here: Lambda's own SQS event
// source mapping replaces explicit Dequeue polling entirely, so there is no
// SQS-backed Consumer to write. Ack/Nack/Heartbeat/SetPhase's equivalents
// (message deletion, visibility-timeout extension) belong inside the
// Lambda handlers that consume these queues, not in this package.
package sqs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Client is the narrow SQS capability Store needs, satisfied by the real
// AWS SDK client (see NewClient) or a test double (internal/queue/sqs/mock).
type Client interface {
	// SendMessage publishes body to the queue at queueURL.
	SendMessage(ctx context.Context, queueURL, body string) error
}

// Config maps each queue.JobType this Store publishes to its own SQS queue
// URL — one queue per job type, matching the Postgres jobs table's
// per-job-type rows today. Explicit named fields rather than a
// map[queue.JobType]string, so a queue left unconfigured is a compile-time
// question at the call site building Config, not a runtime map-lookup miss.
type Config struct {
	DocumentUploadedQueueURL     string
	EntityExtractionQueueURL     string
	EdgeExtractionQueueURL       string
	RegionClassificationQueueURL string
	CanonicalizationQueueURL     string
}

// Store publishes queue events to SQS. It implements queue.Publisher only
// — see the package doc for why there is no Consumer half.
type Store struct {
	client Client
	cfg    Config
}

// New returns a Store that publishes through client, routing each event to
// the queue URL cfg names for its job type.
func New(client Client, cfg Config) *Store {
	return &Store{client: client, cfg: cfg}
}

// message is the JSON envelope published to SQS. The eventual Lambda
// consumer (workstream B/C of the Event-Driven Job Orchestration dev
// board) unmarshals this to reconstruct the equivalent of a queue.Job.
// Deliberately not queue.Job itself: Job carries Attempts/MaxAttempts,
// which are jobs-table/Postgres-retry concepts with no meaning here — SQS
// redrive policies (maxReceiveCount, configured per queue in Terraform)
// are what replace that, not a field on the message.
type message struct {
	Type       queue.JobType `json:"type"`
	DocumentID uuid.UUID     `json:"document_id"`
	UserID     uuid.UUID     `json:"user_id"`
}

func (s *Store) PublishDocumentUploaded(ctx context.Context, evt queue.DocumentUploaded) error {
	return s.publish(ctx, s.cfg.DocumentUploadedQueueURL, message{
		Type: queue.JobTypeDocumentIndexing, DocumentID: evt.DocumentID, UserID: evt.UserID,
	})
}

func (s *Store) PublishEntityExtraction(ctx context.Context, evt queue.EntityExtractionRequested) error {
	return s.publish(ctx, s.cfg.EntityExtractionQueueURL, message{
		Type: queue.JobTypeEntityExtraction, DocumentID: evt.DocumentID, UserID: evt.UserID,
	})
}

func (s *Store) PublishEdgeExtraction(ctx context.Context, evt queue.EdgeExtractionRequested) error {
	return s.publish(ctx, s.cfg.EdgeExtractionQueueURL, message{
		Type: queue.JobTypeEdgeExtraction, DocumentID: evt.DocumentID, UserID: evt.UserID,
	})
}

func (s *Store) PublishRegionClassification(ctx context.Context, evt queue.RegionClassificationRequested) error {
	return s.publish(ctx, s.cfg.RegionClassificationQueueURL, message{
		Type: queue.JobTypeRegionClassification, DocumentID: evt.DocumentID, UserID: evt.UserID,
	})
}

func (s *Store) PublishCanonicalization(ctx context.Context, evt queue.CanonicalizationRequested) error {
	return s.publish(ctx, s.cfg.CanonicalizationQueueURL, message{
		Type: queue.JobTypeCanonicalization, DocumentID: evt.DocumentID, UserID: evt.UserID,
	})
}

func (s *Store) publish(ctx context.Context, queueURL string, msg message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("queue/sqs: marshal %s message for document %s: %w", msg.Type, msg.DocumentID, err)
	}
	if err := s.client.SendMessage(ctx, queueURL, string(body)); err != nil {
		return fmt.Errorf("queue/sqs: publish %s for document %s: %w", msg.Type, msg.DocumentID, err)
	}
	return nil
}

var _ queue.Publisher = (*Store)(nil)
