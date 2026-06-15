package search

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

// defaultK is the number of chunks retrieved per query.
const defaultK = 5

// Service orchestrates the full query path: retrieve chunks then answer.
// It is the single entry point that M8's API layer calls.
type Service struct {
	retriever retrieval.Retriever
	answerer  Answerer
}

// New returns a Service wired to the given Retriever and Answerer.
func New(r retrieval.Retriever, a Answerer) *Service {
	return &Service{retriever: r, answerer: a}
}

// Search executes the full query pipeline for the given knowledge base:
// retrieve the top-k chunks, then generate a cited answer from them.
func (s *Service) Search(ctx context.Context, kbID uuid.UUID, query string) (Result, error) {
	chunks, err := s.retriever.Retrieve(ctx, kbID, query, defaultK)
	if err != nil {
		return Result{}, fmt.Errorf("search: retrieve: %w", err)
	}
	result, err := s.answerer.Answer(ctx, kbID, query, chunks)
	if err != nil {
		return Result{}, fmt.Errorf("search: answer: %w", err)
	}
	return result, nil
}
