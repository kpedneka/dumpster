// Package search implements the query answering layer: an Answerer that turns
// retrieved chunks into grounded, cited prose, and a Service that orchestrates
// the full query path from a natural-language question to a structured answer.
package search

import (
	"context"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

// CitationBoundingBox holds the fractional [0,1] page coordinates of a
// region-derived chunk, mirroring chunk.BoundingBox. Defined here to avoid
// a search→chunk import dependency.
type CitationBoundingBox struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// Citation identifies the exact source span that supports a claim in the answer.
// Every citation resolves to a real chunk: slicing the original document at
// CharStart:CharEnd reproduces the text the model relied on.
// For PDF/image chunks derived from region classification, PageNumber and
// BoundingBox additionally locate the region on the rendered source page.
type Citation struct {
	// Number is the 1-indexed [N] marker this citation corresponds to in the
	// summary text. The Citations slice is built from whichever numbers the
	// model actually listed in its CITATIONS footer — it is not guaranteed to
	// be dense or in marker order, so callers must match a marker to a
	// citation by Number, never by slice position.
	Number     int
	DocumentID uuid.UUID
	ChunkID    uuid.UUID
	CharStart  int
	CharEnd    int
	// Text is the chunk's own extracted content — the exact source span the
	// model relied on. Serving this directly to clients (rather than having
	// them re-fetch and slice the original document) is what makes citation
	// display correct for PDF/image chunks, whose CharStart/CharEnd are only
	// meaningful relative to their own region's text, not the whole document.
	Text string
	// PageNumber and BoundingBox are the second citation variant: set only
	// for chunks derived from PDF/image region classification. Both are nil
	// for text/markdown chunks.
	PageNumber  *int
	BoundingBox *CitationBoundingBox
}

// Result is the structured output of an answered query.
type Result struct {
	Summary   string
	Citations []Citation
	// RetrievedDocuments is every document represented in the fused top-k
	// chunk list that was handed to the answerer, ranked (best first) and
	// deduped by first occurrence. It is a superset of the documents named
	// in Citations: retrieval surfaces every file with a keyword/vector
	// match, while a citation only names the subset the model actually
	// relied on to answer.
	RetrievedDocuments []uuid.UUID
}

// Answerer converts a set of pre-retrieved chunks into a grounded answer.
// It performs no retrieval; callers supply the chunks, making it independently
// testable with hand-crafted data.
type Answerer interface {
	Answer(ctx context.Context, kbID uuid.UUID, query string, chunks []retrieval.ScoredChunk) (Result, error)
	// AnswerStream behaves like Answer, but also invokes onDelta for each
	// visible text chunk as it's generated. onDelta never receives any
	// part of the CITATIONS: footer — see footerFilter — so callers can
	// forward every delta straight to a client without leaking it.
	AnswerStream(ctx context.Context, kbID uuid.UUID, query string, chunks []retrieval.ScoredChunk, onDelta func(delta string)) (Result, error)
}

// StreamEventType distinguishes what a StreamEvent carries. Exactly one of
// StreamEvent's payload fields is populated, according to Type.
type StreamEventType string

const (
	// EventRetrievedFiles carries the ranked retrieval set, emitted once,
	// before generation begins — see StreamEvent.RetrievedDocuments.
	EventRetrievedFiles StreamEventType = "retrieved_files"
	// EventDelta carries one chunk of generated answer text, emitted as
	// generation progresses — see StreamEvent.Delta.
	EventDelta StreamEventType = "delta"
)

// StreamEvent is one incremental update emitted while SearchStream answers
// a query. The final Result (summary + citations) is not a StreamEvent: it
// is SearchStream's own return value, since — unlike retrieved files or a
// generated token — it only exists once the whole pipeline has completed.
type StreamEvent struct {
	Type StreamEventType
	// RetrievedDocuments is set when Type == EventRetrievedFiles; see
	// Result.RetrievedDocuments for its meaning.
	RetrievedDocuments []uuid.UUID
	// Delta is set when Type == EventDelta.
	Delta string
}

// Searcher is the single entry point for end-to-end query execution.
// It owns the sequence: retrieve → answer → assemble citations.
type Searcher interface {
	Search(ctx context.Context, kbID uuid.UUID, query string) (Result, error)
	// SearchStream behaves like Search, but emits StreamEvent values via
	// onEvent as they become available — retrieved files immediately after
	// retrieval, then a delta per generated text chunk — instead of making
	// the caller wait for the full Result. It still returns the same final
	// Result Search would, once everything completes.
	SearchStream(ctx context.Context, kbID uuid.UUID, query string, onEvent func(StreamEvent)) (Result, error)
}
