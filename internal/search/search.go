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
}

// Answerer converts a set of pre-retrieved chunks into a grounded answer.
// It performs no retrieval; callers supply the chunks, making it independently
// testable with hand-crafted data.
type Answerer interface {
	Answer(ctx context.Context, kbID uuid.UUID, query string, chunks []retrieval.ScoredChunk) (Result, error)
}

// Searcher is the single entry point for end-to-end query execution.
// It owns the sequence: retrieve → answer → assemble citations.
type Searcher interface {
	Search(ctx context.Context, kbID uuid.UUID, query string) (Result, error)
}
