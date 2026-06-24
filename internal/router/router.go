// Package router classifies natural-language queries into one of three route
// types so the search service can activate the right retrieval legs.
package router

import "context"

// QueryType names the route the search service should take for a query.
type QueryType string

const (
	// Normal routes to the existing hybrid (vector + keyword) path only.
	Normal QueryType = "normal"
	// Aggregation activates the graph aggregation leg alongside hybrid
	// retrieval, handling questions like "what organizations are mentioned
	// with FEMA" — the primary justification for GraphRAG.
	Aggregation QueryType = "aggregation"
	// MultiHop activates the graph traversal leg alongside hybrid retrieval,
	// handling questions that require following entity connections across
	// documents.
	MultiHop QueryType = "multi_hop"
)

// Router classifies a natural-language query into a QueryType.
// Routing deliberately uses an LLM rather than rule-based matching: a
// misrouted aggregation question returns an ordinary RAG answer with no
// signal anything was missed, an asymmetry sharp enough to skip past free
// rule-based matching.
type Router interface {
	Route(ctx context.Context, query string) (QueryType, error)
}
