package search

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/graphrag"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
	"github.com/kunalpednekar/dumpster/internal/router"
)

// defaultK is the number of chunks retrieved per leg per query. Raised from 5
// to reduce how often a section spanning multiple chunks (e.g. a multi-page
// PDF conclusion split one region/chunk per page) gets partially cut off by
// the retrieval cutoff — similarity ranking has no notion of section
// contiguity, so a higher K meaningfully improves coverage even though it
// doesn't fully solve it. See the aggregation-leg comment below for the
// broader, still-open version of this problem.
const defaultK = 12

// Service orchestrates the full query path: route → retrieve → answer.
// It is the single entry point that the API layer calls.
type Service struct {
	retriever      retrieval.Retriever
	answerer       Answerer
	router         router.Router
	graphRetriever graphrag.GraphRetriever
}

// Option is a functional option for configuring a Service.
type Option func(*Service)

// WithRouter attaches a query router to the Service. Without this option
// the Service always takes the normal (hybrid-only) path.
func WithRouter(rt router.Router) Option {
	return func(s *Service) { s.router = rt }
}

// WithGraphRetriever attaches graph-based retrieval legs to the Service.
// Without this option graph legs are never activated even if a router is set.
func WithGraphRetriever(gr graphrag.GraphRetriever) Option {
	return func(s *Service) { s.graphRetriever = gr }
}

// New returns a Service wired to the given Retriever and Answerer.
// Pass WithRouter and WithGraphRetriever to enable GraphRAG paths.
func New(r retrieval.Retriever, a Answerer, opts ...Option) *Service {
	s := &Service{retriever: r, answerer: a}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Search executes the full query pipeline and returns once the complete
// Result is ready. It is a convenience wrapper around SearchStream for
// callers that don't need incremental events.
func (s *Service) Search(ctx context.Context, kbID uuid.UUID, query string) (Result, error) {
	return s.SearchStream(ctx, kbID, query, func(StreamEvent) {})
}

// SearchStream executes the full query pipeline:
//  1. Route the query to decide which legs to activate (normal / aggregation / multi-hop).
//  2. Always run the hybrid (vector + keyword) leg.
//  3. If the router selected a graph path and a GraphRetriever is wired in,
//     run the appropriate graph leg alongside the hybrid leg.
//  4. Merge all legs via RRF, emit the ranked retrieval set via onEvent,
//     then generate a cited answer, forwarding each generated text chunk
//     via onEvent as it arrives.
//
// When no router is configured the Service behaves exactly as before
// v2.7 — the graph legs are never activated and existing callers need
// no changes.
//
// Known limitation (parked deliberately): aggregation queries promise
// completeness ("all orgs mentioned with FEMA") but feeding their chunks into
// a top-k RRF merge can silently drop correctly-found entities. This will be
// revisited once real usage shows whether the truncation costs anything
// observable rather than forcing a premature answer now.
func (s *Service) SearchStream(ctx context.Context, kbID uuid.UUID, query string, onEvent func(StreamEvent)) (Result, error) {
	qt := router.Normal
	if s.router != nil {
		var err error
		qt, err = s.router.Route(ctx, query)
		if err != nil {
			return Result{}, fmt.Errorf("search: route: %w", err)
		}
	}

	hybrid, err := s.retriever.Retrieve(ctx, kbID, query, defaultK)
	if err != nil {
		return Result{}, fmt.Errorf("search: retrieve: %w", err)
	}

	legs := [][]retrieval.ScoredChunk{hybrid}

	if s.graphRetriever != nil {
		switch qt {
		case router.Aggregation:
			graphChunks, err := s.graphRetriever.AggregationLeg(ctx, kbID, query, defaultK)
			if err != nil {
				return Result{}, fmt.Errorf("search: aggregation leg: %w", err)
			}
			legs = append(legs, graphChunks)
		case router.MultiHop:
			graphChunks, err := s.graphRetriever.TraversalLeg(ctx, kbID, query, defaultK)
			if err != nil {
				return Result{}, fmt.Errorf("search: traversal leg: %w", err)
			}
			legs = append(legs, graphChunks)
		}
	}

	chunks := retrieval.RRFMerge(defaultK, legs...)
	docs := retrievedDocuments(chunks)
	onEvent(StreamEvent{Type: EventRetrievedFiles, RetrievedDocuments: docs})

	result, err := s.answerer.AnswerStream(ctx, kbID, query, chunks, func(delta string) {
		onEvent(StreamEvent{Type: EventDelta, Delta: delta})
	})
	if err != nil {
		return Result{}, fmt.Errorf("search: answer: %w", err)
	}
	result.RetrievedDocuments = docs
	return result, nil
}

// retrievedDocuments returns every document represented in chunks, ranked
// (best first, i.e. in chunks' existing fused order) and deduped by first
// occurrence — a document with multiple chunks in the fused list is ranked
// at its best (first) occurrence, not repeated.
func retrievedDocuments(chunks []retrieval.ScoredChunk) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(chunks))
	docs := make([]uuid.UUID, 0, len(chunks))
	for _, c := range chunks {
		if seen[c.DocumentID] {
			continue
		}
		seen[c.DocumentID] = true
		docs = append(docs, c.DocumentID)
	}
	return docs
}
