// Package queue defines event types and interfaces for background job
// processing. Concrete implementations (Postgres, Kafka) live in sub-packages
// and are selected at wire-up time.
package queue

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// DocumentUploaded is published once a document's bytes land in object storage
// and a documents row has been created at status "pending".
type DocumentUploaded struct {
	DocumentID uuid.UUID
	UserID     uuid.UUID
}

// Job is a unit of work dequeued for processing.
type Job struct {
	ID          uuid.UUID
	DocumentID  uuid.UUID
	UserID      uuid.UUID
	Attempts    int
	MaxAttempts int
}

// ErrNoJobs is returned by Consumer.Dequeue when no work is available.
var ErrNoJobs = errors.New("queue: no jobs available")

// Publisher dispatches document lifecycle events for background processing.
type Publisher interface {
	PublishDocumentUploaded(ctx context.Context, evt DocumentUploaded) error
}

// Consumer pulls jobs from the queue for processing.
type Consumer interface {
	// Dequeue claims the next available job. Returns ErrNoJobs when the queue
	// is empty; the caller should back off before retrying.
	Dequeue(ctx context.Context) (*Job, error)
	// Ack marks a job as successfully completed and removes it from the queue.
	Ack(ctx context.Context, jobID uuid.UUID) error
	// Nack records a processing failure. If the job has exhausted its retries
	// it is dead-lettered and the function returns (true, nil); otherwise the
	// job is rescheduled with exponential backoff and the function returns
	// (false, nil).
	Nack(ctx context.Context, jobID uuid.UUID, reason error) (deadLettered bool, err error)
}

// Queue combines publishing and consuming into a single interface.
type Queue interface {
	Publisher
	Consumer
}
