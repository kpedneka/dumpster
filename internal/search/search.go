// Package search implements the query answering layer: an Answerer that turns
// retrieved chunks into grounded, cited prose, and a Service that orchestrates
// the full query path from a natural-language question to a structured answer.
package search

import (
	"context"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

// Citation identifies the exact source span that supports a claim in the answer.
// Every citation resolves to a real chunk: slicing the original document at
// CharStart:CharEnd reproduces the text the model relied on.
type Citation struct {
	DocumentID uuid.UUID
	ChunkID    uuid.UUID
	CharStart  int
	CharEnd    int
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
