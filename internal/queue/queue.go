// Package queue defines event types and interfaces for background job
// processing. Concrete implementations (Postgres, Kafka) live in sub-packages
// and are selected at wire-up time.
package queue

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// JobType discriminates between the distinct stages of the ingestion
// pipeline that share the same queue/job-stage seam. Each stage has its own
// Handler (see internal/worker) so it can be re-run independently of the
// others — e.g. re-running entity extraction after a type-set change must
// not re-chunk or re-embed a document.
type JobType string

const (
	// JobTypeDocumentIndexing splits a document into chunks and embeds them.
	JobTypeDocumentIndexing JobType = "document_indexing"
	// JobTypeEntityExtraction runs local entity extraction over a document's
	// existing chunks. It depends on document indexing having already
	// produced chunk rows, but does not itself touch chunks or embeddings.
	JobTypeEntityExtraction JobType = "entity_extraction"
	// JobTypeEdgeExtraction derives co-occurrence edges between entity
	// mentions found in the same chunk, and persists them to the entity_edges
	// table. Depends on entity extraction having populated entities rows, but
	// does not touch chunks, embeddings, or entity text.
	JobTypeEdgeExtraction JobType = "edge_extraction"
	// JobTypeRegionClassification runs the layered PDF/image region
	// classifier (pdfplumber + unstructured.io) and is the
	// alternative entry point to JobTypeDocumentIndexing for non-text
	// file types. It produces the ingestion manifest and the document's
	// chunks, then enqueues JobTypeEntityExtraction.
	JobTypeRegionClassification JobType = "region_classification"
	// JobTypeCanonicalization resolves a document's entity mentions to
	// stable canonical entities (internal/canonical), so query-time graph
	// traversal has a cross-chunk, cross-document identity to hop on.
	// Depends on entity extraction having populated entities rows, but does
	// not touch chunks, embeddings, entity text, or entity_edges.
	JobTypeCanonicalization JobType = "canonicalization"
)

// DocumentUploaded is published once a document's bytes land in object storage
// and a documents row has been created at status "pending".
type DocumentUploaded struct {
	DocumentID uuid.UUID
	UserID     uuid.UUID
}

// EntityExtractionRequested is published to (re-)run local entity extraction
// over a document's existing chunks, independent of chunking/embedding.
type EntityExtractionRequested struct {
	DocumentID uuid.UUID
	UserID     uuid.UUID
}

// EdgeExtractionRequested is published to derive co-occurrence edges between
// entity mentions found in the same chunk, independent of entity extraction.
type EdgeExtractionRequested struct {
	DocumentID uuid.UUID
	UserID     uuid.UUID
}

// CanonicalizationRequested is published to (re-)resolve a document's entity
// mentions to canonical entities, independent of entity extraction and edge
// extraction.
type CanonicalizationRequested struct {
	DocumentID uuid.UUID
	UserID     uuid.UUID
}

// Job is a unit of work dequeued for processing. Type determines which
// registered Handler the worker dispatches it to.
type Job struct {
	ID          uuid.UUID
	Type        JobType
	DocumentID  uuid.UUID
	UserID      uuid.UUID
	Attempts    int
	MaxAttempts int
}

// ErrNoJobs is returned by Consumer.Dequeue when no work is available.
var ErrNoJobs = errors.New("queue: no jobs available")

// RegionClassificationRequested is published for PDF and image documents
// in place of DocumentUploaded, since those file types require region
// classification before chunking and embedding can proceed.
type RegionClassificationRequested struct {
	DocumentID uuid.UUID
	UserID     uuid.UUID
}

// Publisher dispatches document lifecycle events for background processing.
type Publisher interface {
	PublishDocumentUploaded(ctx context.Context, evt DocumentUploaded) error
	// PublishEntityExtraction enqueues a distinct entity-extraction job for
	// an already-ingested document, independently of (re-)chunking or
	// (re-)embedding.
	PublishEntityExtraction(ctx context.Context, evt EntityExtractionRequested) error
	// PublishEdgeExtraction enqueues a distinct edge-extraction job for an
	// already-entity-extracted document, independently of entity extraction.
	PublishEdgeExtraction(ctx context.Context, evt EdgeExtractionRequested) error
	// PublishRegionClassification enqueues a region-classification job for
	// a PDF or image document in place of document indexing. The handler
	// performs layered region classification, produces chunks + the ingestion
	// manifest, and then enqueues entity extraction.
	PublishRegionClassification(ctx context.Context, evt RegionClassificationRequested) error
	// PublishCanonicalization enqueues a distinct canonicalization job for
	// an already-entity-extracted document, independently of edge
	// extraction.
	PublishCanonicalization(ctx context.Context, evt CanonicalizationRequested) error
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
	// Heartbeat touches jobID's updated_at so ReclaimStale doesn't mistake
	// a job that's actively making progress for one that's stuck. Intended
	// for a long-running Handle call that internally makes incremental
	// progress (e.g. EntityHandler's per-batch persistence) to call between
	// steps — without it, a job whose total processing time exceeds
	// JOB_STALE_TIMEOUT gets reclaimed and its attempt count burned even
	// though nothing was actually wrong with it.
	Heartbeat(ctx context.Context, jobID uuid.UUID) error
}

// Queue combines publishing and consuming into a single interface.
type Queue interface {
	Publisher
	Consumer
}
