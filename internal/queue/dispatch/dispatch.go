// Package dispatch provides the queue.Publisher for the event-driven
// pipeline. Every publish first records the job in the status store
// (internal/jobstatus), which assigns its attempt number and makes it
// visible to the progress UI, then hands the run to whatever executes that
// job type:
//
//   - document indexing, entity extraction and region classification start
//     a Step Functions execution (internal/queue/stepfunctions);
//   - edge extraction and canonicalization go to SQS for the stateless-jobs
//     Lambda (internal/queue/sqs).
//
// Callers keep using queue.Publisher, so swapping this in for the Postgres
// queue is a wiring change only. The context must carry the user's
// identity (auth.WithUserID), since the Postgres status store enforces
// row-level security.
package dispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// StatusWriter is the part of jobstatus.Writer the publisher needs.
type StatusWriter interface {
	Enqueue(ctx context.Context, key jobstatus.Key) (jobstatus.Enqueued, error)
	MarkFailed(ctx context.Context, key jobstatus.Key, attempt int, reason string) error
}

// Target hands a job run to whatever executes it. Satisfied by
// *sqs.Store and *stepfunctions.Starter.
type Target interface {
	Dispatch(ctx context.Context, run queue.JobRun) error
}

// Publisher implements queue.Publisher on top of a status store and the
// two execution targets.
type Publisher struct {
	status   StatusWriter
	sqs, sfn Target
}

// New returns a Publisher that records jobs in status and routes them to
// sqs (Lambda job types) or stepFunctions (Batch-backed job types).
func New(status StatusWriter, sqs, stepFunctions Target) *Publisher {
	return &Publisher{status: status, sqs: sqs, sfn: stepFunctions}
}

// PublishDocumentUploaded starts document indexing on Step Functions.
func (p *Publisher) PublishDocumentUploaded(ctx context.Context, evt queue.DocumentUploaded) error {
	return p.publish(ctx, p.sfn, queue.JobTypeDocumentIndexing, evt.DocumentID, evt.UserID)
}

// PublishEntityExtraction starts entity extraction on Step Functions.
func (p *Publisher) PublishEntityExtraction(ctx context.Context, evt queue.EntityExtractionRequested) error {
	return p.publish(ctx, p.sfn, queue.JobTypeEntityExtraction, evt.DocumentID, evt.UserID)
}

// PublishRegionClassification starts region classification on Step
// Functions.
func (p *Publisher) PublishRegionClassification(ctx context.Context, evt queue.RegionClassificationRequested) error {
	return p.publish(ctx, p.sfn, queue.JobTypeRegionClassification, evt.DocumentID, evt.UserID)
}

// PublishEdgeExtraction sends edge extraction to its SQS queue.
func (p *Publisher) PublishEdgeExtraction(ctx context.Context, evt queue.EdgeExtractionRequested) error {
	return p.publish(ctx, p.sqs, queue.JobTypeEdgeExtraction, evt.DocumentID, evt.UserID)
}

// PublishCanonicalization sends canonicalization to its SQS queue.
func (p *Publisher) PublishCanonicalization(ctx context.Context, evt queue.CanonicalizationRequested) error {
	return p.publish(ctx, p.sqs, queue.JobTypeCanonicalization, evt.DocumentID, evt.UserID)
}

// publish records the job and, unless it's already active, dispatches its
// new attempt. If the dispatch fails, the attempt is marked failed so the
// document doesn't sit "pending" forever with nothing running it, which
// would also block its delete/retry guard. The user can then retry, which
// starts the next attempt.
//
// Known gap: a crash between recording the attempt and dispatching it
// leaves the attempt pending with nothing running it. The Postgres queue
// didn't have this gap, because inserting the job row was the dispatch.
func (p *Publisher) publish(ctx context.Context, target Target, jobType queue.JobType, documentID, userID uuid.UUID) error {
	key := jobstatus.Key{UserID: userID, DocumentID: documentID, JobType: jobType}
	res, err := p.status.Enqueue(ctx, key)
	if err != nil {
		return fmt.Errorf("dispatch: record %s for document %s: %w", jobType, documentID, err)
	}
	if !res.Started {
		return nil
	}

	run := queue.JobRun{Type: jobType, DocumentID: documentID, UserID: userID, Attempt: res.Attempt}
	dispatchErr := target.Dispatch(ctx, run)
	if dispatchErr == nil {
		return nil
	}
	err = fmt.Errorf("dispatch: %s attempt %d for document %s: %w", jobType, res.Attempt, documentID, dispatchErr)
	if markErr := p.status.MarkFailed(ctx, key, res.Attempt, err.Error()); markErr != nil {
		return errors.Join(err, fmt.Errorf("dispatch: mark attempt failed: %w", markErr))
	}
	return err
}

var _ queue.Publisher = (*Publisher)(nil)
