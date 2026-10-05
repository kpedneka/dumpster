// Package queue defines event types and interfaces for background job
// processing. Concrete implementations (Postgres, Kafka) live in sub-packages
// and are selected at wire-up time.
package queue

import (
	"context"

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

// Job is one job handed to a worker.Handler by the stateless-jobs Lambda.
// Type determines which registered Handler it is dispatched to.
type Job struct {
	Type       JobType
	DocumentID uuid.UUID
	UserID     uuid.UUID
}

// JobRun is one attempt at one job, as handed to whatever runs it: an SQS
// queue feeding a Lambda, or a Step Functions state machine. Attempt is
// the job's attempt number from the status store (internal/jobstatus),
// so a run can report progress against exactly the attempt it belongs to.
type JobRun struct {
	Type       JobType
	DocumentID uuid.UUID
	UserID     uuid.UUID
	Attempt    int
}

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

// JobStatus is one active job's current state, for read-only display
// purposes (the upload progress UI) -- distinct from Job, which is what a
// handler receives to run and carries no status.
type JobStatus struct {
	Type      JobType
	Phase     string // empty when this job type has no explicit phase override in play
	Status    string // "pending" or "processing" -- ActiveJobsForDocuments never returns a dead-lettered ("failed") job, see its doc
	LastError string
}

// JobStatusReader exposes read-only job status to the API layer
// (implemented by internal/jobstatus/pgstore), kept separate from the
// status writes the pipeline makes since nothing about display needs the
// ability to mutate a job.
type JobStatusReader interface {
	// ActiveJobsForDocuments returns every currently pending/processing
	// job for each document ID in documentIDs, keyed by DocumentID. A
	// document ID with no entry (or an empty slice) has no active job --
	// either every pipeline stage already completed successfully, or
	// (rarer) is momentarily between one stage finishing and the next
	// one being published. A failed job is never included: treating it
	// as active would misrepresent a permanently stuck job as
	// still-in-progress forever.
	//
	// More than one job can be active for the same document at once now
	// that entity extraction can start before a document's own indexing
	// job (region_classification/document_indexing) finishes -- see
	// internal/pipeline/regionclassify's BuildChunks step. Callers that need
	// a single "what stage is this document on" answer should pick the
	// job representing the *least* progress (queue.EarliestActiveStage),
	// not just the first or most-recently-created one: which job happens
	// to exist is not the same question as which one is the bottleneck.
	ActiveJobsForDocuments(ctx context.Context, userID uuid.UUID, documentIDs []uuid.UUID) (map[uuid.UUID][]JobStatus, error)
}
