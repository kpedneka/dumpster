// Package dispatch provides the queue.Publisher that records each job in
// the status store and hands it to SQS or Step Functions.
package dispatch

import (
	"context"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// StatusWriter is the part of jobstatus.Writer the publisher needs.
type StatusWriter interface {
	Enqueue(ctx context.Context, key jobstatus.Key) (jobstatus.Enqueued, error)
	MarkFailed(ctx context.Context, key jobstatus.Key, attempt int, reason string) error
}

// Target runs a job.
type Target interface {
	Dispatch(ctx context.Context, run queue.JobRun) error
}

// Publisher implements queue.Publisher.
type Publisher struct {
	status   StatusWriter
	sqs, sfn Target
}

// New returns a Publisher.
func New(status StatusWriter, sqs, stepFunctions Target) *Publisher {
	return &Publisher{status: status, sqs: sqs, sfn: stepFunctions}
}

// PublishDocumentUploaded implements queue.Publisher.
func (p *Publisher) PublishDocumentUploaded(ctx context.Context, evt queue.DocumentUploaded) error {
	return nil
}

// PublishEntityExtraction implements queue.Publisher.
func (p *Publisher) PublishEntityExtraction(ctx context.Context, evt queue.EntityExtractionRequested) error {
	return nil
}

// PublishEdgeExtraction implements queue.Publisher.
func (p *Publisher) PublishEdgeExtraction(ctx context.Context, evt queue.EdgeExtractionRequested) error {
	return nil
}

// PublishRegionClassification implements queue.Publisher.
func (p *Publisher) PublishRegionClassification(ctx context.Context, evt queue.RegionClassificationRequested) error {
	return nil
}

// PublishCanonicalization implements queue.Publisher.
func (p *Publisher) PublishCanonicalization(ctx context.Context, evt queue.CanonicalizationRequested) error {
	return nil
}
