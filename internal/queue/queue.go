// Package queue defines event types and the Publisher interface used to signal
// background processing work. Concrete implementations (e.g. Postgres-queue,
// Kafka) live in sub-packages and are selected at wire-up time.
package queue

import (
	"context"

	"github.com/google/uuid"
)

// DocumentUploaded is published once a document's bytes land in object storage
// and a documents row has been created at status "pending".
type DocumentUploaded struct {
	DocumentID uuid.UUID
}

// Publisher dispatches document lifecycle events for background processing.
type Publisher interface {
	PublishDocumentUploaded(ctx context.Context, evt DocumentUploaded) error
}
