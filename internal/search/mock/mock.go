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

// Searcher is a configurable test double for search.Searcher.
type Searcher struct {
	SearchFn func(ctx context.Context, kbID uuid.UUID, query string) (search.Result, error)
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
