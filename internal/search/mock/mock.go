// Package mock provides test doubles for search.Answerer and search.Searcher.
package mock

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
	"github.com/kunalpednekar/dumpster/internal/search"
)

// Answerer is a configurable test double for search.Answerer.
type Answerer struct {
	AnswerFn func(ctx context.Context, kbID uuid.UUID, query string, chunks []retrieval.ScoredChunk) (search.Result, error)
	// AnswerStreamFn overrides AnswerStream's default behavior (calling
	// AnswerFn and delivering its Summary as a single delta). Set this in
	// tests that need to exercise multi-delta streaming behavior.
	AnswerStreamFn func(ctx context.Context, kbID uuid.UUID, query string, chunks []retrieval.ScoredChunk, onDelta func(string)) (search.Result, error)
}

// NewAnswerer returns an Answerer that always returns result.
func NewAnswerer(result search.Result) *Answerer {
	return &Answerer{
		AnswerFn: func(_ context.Context, _ uuid.UUID, _ string, _ []retrieval.ScoredChunk) (search.Result, error) {
			return result, nil
		},
	}
}

// NewErrorAnswerer returns an Answerer that always returns an error.
func NewErrorAnswerer(msg string) *Answerer {
	return &Answerer{
		AnswerFn: func(_ context.Context, _ uuid.UUID, _ string, _ []retrieval.ScoredChunk) (search.Result, error) {
			return search.Result{}, errors.New(msg)
		},
	}
}

// Answer delegates to AnswerFn.
func (m *Answerer) Answer(ctx context.Context, kbID uuid.UUID, query string, chunks []retrieval.ScoredChunk) (search.Result, error) {
	return m.AnswerFn(ctx, kbID, query, chunks)
}

// AnswerStream delegates to AnswerStreamFn if set, otherwise falls back to
// calling AnswerFn and delivering its Summary as a single delta.
func (m *Answerer) AnswerStream(ctx context.Context, kbID uuid.UUID, query string, chunks []retrieval.ScoredChunk, onDelta func(string)) (search.Result, error) {
	if m.AnswerStreamFn != nil {
		return m.AnswerStreamFn(ctx, kbID, query, chunks, onDelta)
	}
	result, err := m.AnswerFn(ctx, kbID, query, chunks)
	if err != nil {
		return search.Result{}, err
	}
	onDelta(result.Summary)
	return result, nil
}

// Searcher is a configurable test double for search.Searcher.
type Searcher struct {
	SearchFn func(ctx context.Context, kbID uuid.UUID, query string) (search.Result, error)
	// SearchStreamFn overrides SearchStream's default behavior (calling
	// SearchFn and delivering its Summary as a single delta, with no
	// retrieved_files event). Set this in tests that need to exercise the
	// full event sequence.
	SearchStreamFn func(ctx context.Context, kbID uuid.UUID, query string, onEvent func(search.StreamEvent)) (search.Result, error)
}

// NewSearcher returns a Searcher that always returns result.
func NewSearcher(result search.Result) *Searcher {
	return &Searcher{
		SearchFn: func(_ context.Context, _ uuid.UUID, _ string) (search.Result, error) {
			return result, nil
		},
	}
}

// NewErrorSearcher returns a Searcher that always returns an error.
func NewErrorSearcher(msg string) *Searcher {
	return &Searcher{
		SearchFn: func(_ context.Context, _ uuid.UUID, _ string) (search.Result, error) {
			return search.Result{}, errors.New(msg)
		},
	}
}

// Search delegates to SearchFn.
func (m *Searcher) Search(ctx context.Context, kbID uuid.UUID, query string) (search.Result, error) {
	return m.SearchFn(ctx, kbID, query)
}

// SearchStream delegates to SearchStreamFn if set, otherwise falls back to
// calling SearchFn and delivering its Summary as a single delta (preceded
// by a retrieved_files event built from the result's RetrievedDocuments).
func (m *Searcher) SearchStream(ctx context.Context, kbID uuid.UUID, query string, onEvent func(search.StreamEvent)) (search.Result, error) {
	if m.SearchStreamFn != nil {
		return m.SearchStreamFn(ctx, kbID, query, onEvent)
	}
	result, err := m.SearchFn(ctx, kbID, query)
	if err != nil {
		return search.Result{}, err
	}
	onEvent(search.StreamEvent{Type: search.EventRetrievedFiles, RetrievedDocuments: result.RetrievedDocuments})
	onEvent(search.StreamEvent{Type: search.EventDelta, Delta: result.Summary})
	return result, nil
}
