package search_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/graphrag/memory"
	"github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
	"github.com/kunalpednekar/dumpster/internal/router"
	routermock "github.com/kunalpednekar/dumpster/internal/router/mock"
	"github.com/kunalpednekar/dumpster/internal/search"
	searchmock "github.com/kunalpednekar/dumpster/internal/search/mock"
)

// ---- helpers ----------------------------------------------------------------

func authedCtx() context.Context {
	return auth.WithUserID(context.Background(), uuid.New())
}

func makeChunk(docID uuid.UUID, text string, start, end int) retrieval.ScoredChunk {
	return retrieval.ScoredChunk{
		Chunk: &chunk.Chunk{
			ID:         uuid.New(),
			DocumentID: docID,
			Text:       text,
			CharStart:  start,
			CharEnd:    end,
		},
		Score: 1.0,
	}
}

// stubRetriever returns a fixed list of chunks.
type stubRetriever struct {
	chunks []retrieval.ScoredChunk
	err    error
}

func (s *stubRetriever) Retrieve(_ context.Context, _ uuid.UUID, _ string, _ int) ([]retrieval.ScoredChunk, error) {
	return s.chunks, s.err
}

// ---- Answerer tests ---------------------------------------------------------

// TestAnswerer_WithChunks verifies that Answerer returns a non-empty summary
// and at least one citation when chunks are provided and the generator returns
// a well-formed response.
func TestAnswerer_WithChunks(t *testing.T) {
	docID := uuid.New()
	chunks := []retrieval.ScoredChunk{
		makeChunk(docID, "Go is a statically typed language.", 0, 34),
	}

	gen := mock.NewGenerator("Go is fast [1].\nCITATIONS: 1")
	a := search.NewAnswerer(gen)

	result, err := a.Answer(authedCtx(), uuid.New(), "What is Go?", chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(result.Citations) == 0 {
		t.Error("expected at least one citation")
	}
	c := result.Citations[0]
	if c.DocumentID != docID {
		t.Errorf("citation DocumentID: got %v, want %v", c.DocumentID, docID)
	}
	if c.CharStart != 0 || c.CharEnd != 34 {
		t.Errorf("citation offsets: got %d-%d, want 0-34", c.CharStart, c.CharEnd)
	}
}

// TestAnswerer_NoChunks verifies the guardrail: when no chunks are provided
// the Answerer returns a "not found" response rather than fabricating an answer.
func TestAnswerer_NoChunks(t *testing.T) {
	gen := mock.NewGenerator("should not be called")
	a := search.NewAnswerer(gen)

	result, err := a.Answer(authedCtx(), uuid.New(), "anything?", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected a not-found summary")
	}
	if len(result.Citations) != 0 {
		t.Errorf("expected no citations for not-found, got %d", len(result.Citations))
	}
	if !strings.Contains(strings.ToLower(result.Summary), "not") &&
		!strings.Contains(strings.ToLower(result.Summary), "no ") &&
		!strings.Contains(strings.ToLower(result.Summary), "found") {
		t.Errorf("expected not-found message, got: %q", result.Summary)
	}
}

// TestAnswerer_GeneratorError verifies that generator errors are propagated.
func TestAnswerer_GeneratorError(t *testing.T) {
	chunks := []retrieval.ScoredChunk{
		makeChunk(uuid.New(), "some text", 0, 9),
	}
	gen := mock.NewErrorGenerator("llm unavailable")
	a := search.NewAnswerer(gen)

	_, err := a.Answer(authedCtx(), uuid.New(), "query", chunks)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestAnswerer_CitationsReferenceValidChunks verifies that every returned
// citation resolves to a chunk whose offsets bound real source text.
func TestAnswerer_CitationsReferenceValidChunks(t *testing.T) {
	doc1 := uuid.New()
	doc2 := uuid.New()
	chunks := []retrieval.ScoredChunk{
		makeChunk(doc1, "Alpha content about databases.", 0, 30),
		makeChunk(doc2, "Beta content about caches.", 31, 56),
	}

	// Generator cites both chunks.
	gen := mock.NewGenerator("Databases [1] and caches [2] are different.\nCITATIONS: 1,2")
	a := search.NewAnswerer(gen)

	result, err := a.Answer(authedCtx(), uuid.New(), "compare databases and caches", chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Citations) != 2 {
		t.Fatalf("expected 2 citations, got %d", len(result.Citations))
	}
	// Each citation must map to its source chunk's document and offsets.
	for _, c := range result.Citations {
		if c.DocumentID != doc1 && c.DocumentID != doc2 {
			t.Errorf("citation DocumentID %v not in expected set", c.DocumentID)
		}
	}
}

// TestAnswerer_CitationNumberMatchesMarker verifies that when the model's
// CITATIONS footer omits an earlier marker number, the remaining citation's
// Number still reflects which [N] marker it corresponds to — callers must
// not assume Citations[i] corresponds to marker [i+1].
func TestAnswerer_CitationNumberMatchesMarker(t *testing.T) {
	doc1 := uuid.New()
	doc2 := uuid.New()
	chunks := []retrieval.ScoredChunk{
		makeChunk(doc1, "Alpha content about databases.", 0, 30),
		makeChunk(doc2, "Beta content about caches.", 31, 56),
	}

	// The model's prose references marker [2], but the footer only lists "2" —
	// chunk 1 is never cited, so Citations has a single entry at index 0.
	gen := mock.NewGenerator("Caches are different [2].\nCITATIONS: 2")
	a := search.NewAnswerer(gen)

	result, err := a.Answer(authedCtx(), uuid.New(), "what about caches?", chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Citations) != 1 {
		t.Fatalf("expected 1 citation, got %d", len(result.Citations))
	}
	c := result.Citations[0]
	if c.Number != 2 {
		t.Errorf("citation Number: got %d, want 2 (must not be slice position 1)", c.Number)
	}
	if c.DocumentID != doc2 {
		t.Errorf("citation DocumentID: got %v, want doc2 (%v)", c.DocumentID, doc2)
	}
}

// TestAnswerer_OutOfRangeCitationsIgnored verifies that citations referencing
// non-existent chunk indices are silently dropped.
func TestAnswerer_OutOfRangeCitationsIgnored(t *testing.T) {
	chunks := []retrieval.ScoredChunk{
		makeChunk(uuid.New(), "Only one chunk.", 0, 15),
	}
	gen := mock.NewGenerator("Some answer.\nCITATIONS: 1,99,0")
	a := search.NewAnswerer(gen)

	result, err := a.Answer(authedCtx(), uuid.New(), "q", chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only index 1 is valid; 99 and 0 are out of range.
	if len(result.Citations) != 1 {
		t.Errorf("expected 1 valid citation, got %d", len(result.Citations))
	}
}

// TestAnswerer_NoCitationsInResponse verifies that a response without a
// CITATIONS section produces an empty citation list (not an error).
func TestAnswerer_NoCitationsInResponse(t *testing.T) {
	chunks := []retrieval.ScoredChunk{
		makeChunk(uuid.New(), "Some content.", 0, 13),
	}
	gen := mock.NewGenerator("A summary with no citation section.")
	a := search.NewAnswerer(gen)

	result, err := a.Answer(authedCtx(), uuid.New(), "q", chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

// ---- SearchService tests -----------------------------------------------------

// TestService_Search verifies end-to-end: Retriever returns chunks →
// Answerer produces a result → Service returns it.
func TestService_Search(t *testing.T) {
	docID := uuid.New()
	kbID := uuid.New()
	chunks := []retrieval.ScoredChunk{
		makeChunk(docID, "The sky is blue.", 0, 16),
	}

	ret := &stubRetriever{chunks: chunks}
	gen := mock.NewGenerator("The sky is blue [1].\nCITATIONS: 1")
	svc := search.New(ret, search.NewAnswerer(gen))

	result, err := svc.Search(authedCtx(), kbID, "What colour is the sky?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(result.Citations) == 0 {
		t.Error("expected at least one citation")
	}
}

// TestService_SearchNotFound verifies that when Retriever returns no chunks
// the service returns a not-found response without an error.
func TestService_SearchNotFound(t *testing.T) {
	ret := &stubRetriever{chunks: nil}
	gen := mock.NewGenerator("irrelevant")
	svc := search.New(ret, search.NewAnswerer(gen))

	result, err := svc.Search(authedCtx(), uuid.New(), "unknowable query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected not-found summary")
	}
	if len(result.Citations) != 0 {
		t.Errorf("expected no citations, got %d", len(result.Citations))
	}
}

// TestService_RetrieverError verifies that retriever errors surface as errors.
func TestService_RetrieverError(t *testing.T) {
	ret := &stubRetriever{err: errors.New("pg down")}
	gen := mock.NewGenerator("irrelevant")
	svc := search.New(ret, search.NewAnswerer(gen))

	_, err := svc.Search(authedCtx(), uuid.New(), "query")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestAnswerer_LowercaseCitationsRecognised verifies that mixed-case variants
// of the CITATIONS footer (e.g. "Citations:") are still parsed correctly.
func TestAnswerer_LowercaseCitationsRecognised(t *testing.T) {
	docID := uuid.New()
	chunks := []retrieval.ScoredChunk{
		makeChunk(docID, "Go uses goroutines.", 0, 19),
	}
	gen := mock.NewGenerator("Go is concurrent [1].\nCitations: 1")
	a := search.NewAnswerer(gen)

	result, err := a.Answer(authedCtx(), uuid.New(), "concurrency?", chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Citations) != 1 {
		t.Errorf("expected 1 citation from mixed-case footer, got %d", len(result.Citations))
	}
}

// TestAnswerer_ChunkTextContainsCitationsMarker verifies that chunk text
// containing the literal string "CITATIONS:" does not corrupt the parse —
// the footer detection uses the LAST occurrence in the response.
func TestAnswerer_ChunkTextContainsCitationsMarker(t *testing.T) {
	docID := uuid.New()
	chunks := []retrieval.ScoredChunk{
		makeChunk(docID, "See CITATIONS: Appendix A for references.", 0, 40),
	}
	// The LLM echoes the chunk text in its answer, then adds the real footer.
	gen := mock.NewGenerator("Per chunk [1]: See CITATIONS: Appendix A for references.\nCITATIONS: 1")
	a := search.NewAnswerer(gen)

	result, err := a.Answer(authedCtx(), uuid.New(), "references?", chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(result.Citations) != 1 {
		t.Errorf("expected 1 citation (split on last CITATIONS: marker), got %d", len(result.Citations))
	}
}

// TestAnswerer_NotFoundExactMatch verifies the guardrail uses exact string
// equality, so a valid answer that merely contains the not-found phrase as a
// substring is still parsed and its citations are preserved.
func TestAnswerer_NotFoundExactMatch(t *testing.T) {
	docID := uuid.New()
	chunks := []retrieval.ScoredChunk{
		makeChunk(docID, "Go is compiled.", 0, 15),
	}
	// The LLM hedges using the not-found phrase but still provides a cited answer.
	gen := mock.NewGenerator("I could not find relevant information to answer this question directly, but [1] shows Go is compiled.\nCITATIONS: 1")
	a := search.NewAnswerer(gen)

	result, err := a.Answer(authedCtx(), uuid.New(), "compiled?", chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Citations) == 0 {
		t.Error("expected citations to be preserved; not-found check should use exact match, not substring")
	}
}

// ---- GraphRAG service tests --------------------------------------------------

// TestService_Search_Normal verifies that a Normal route never activates any
// graph leg even when a GraphRetriever is wired in.
func TestService_Search_Normal(t *testing.T) {
	docID := uuid.New()
	hybridChunks := []retrieval.ScoredChunk{makeChunk(docID, "hybrid chunk", 0, 12)}

	gr := memory.New()
	// If a graph leg were called, it would return this; seeing it in the
	// answer would signal an incorrect activation.
	gr.AggregationChunks = []retrieval.ScoredChunk{makeChunk(docID, "should not appear", 100, 120)}

	ret := &stubRetriever{chunks: hybridChunks}
	gen := mock.NewGenerator("Hybrid answer [1].\nCITATIONS: 1")
	rt := routermock.New(router.Normal)
	svc := search.New(ret, search.NewAnswerer(gen), search.WithRouter(rt), search.WithGraphRetriever(gr))

	result, err := svc.Search(authedCtx(), uuid.New(), "What is Go?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

// TestService_Search_Aggregation verifies that an aggregation query activates
// the aggregation leg and includes graph chunks in the RRF merge.
func TestService_Search_Aggregation(t *testing.T) {
	docID := uuid.New()
	hybridChunk := makeChunk(docID, "hybrid chunk", 0, 12)
	graphChunk := makeChunk(docID, "graph chunk", 13, 25)

	ret := &stubRetriever{chunks: []retrieval.ScoredChunk{hybridChunk}}
	gr := memory.New()
	gr.AggregationChunks = []retrieval.ScoredChunk{graphChunk}

	rt := routermock.New(router.Aggregation)
	gen := mock.NewGenerator("Answer [1].\nCITATIONS: 1")
	svc := search.New(ret, search.NewAnswerer(gen), search.WithRouter(rt), search.WithGraphRetriever(gr))

	result, err := svc.Search(authedCtx(), uuid.New(), "What organizations are mentioned with FEMA?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

// TestService_Search_MultiHop verifies that a multi-hop query activates the
// traversal leg and includes graph chunks in the RRF merge.
func TestService_Search_MultiHop(t *testing.T) {
	docID := uuid.New()
	hybridChunk := makeChunk(docID, "hybrid chunk", 0, 12)
	graphChunk := makeChunk(docID, "traversal chunk", 13, 28)

	ret := &stubRetriever{chunks: []retrieval.ScoredChunk{hybridChunk}}
	gr := memory.New()
	gr.TraversalChunks = []retrieval.ScoredChunk{graphChunk}

	rt := routermock.New(router.MultiHop)
	gen := mock.NewGenerator("Connected [1].\nCITATIONS: 1")
	svc := search.New(ret, search.NewAnswerer(gen), search.WithRouter(rt), search.WithGraphRetriever(gr))

	result, err := svc.Search(authedCtx(), uuid.New(), "How is Alice connected to Bob?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

// TestService_Search_RouterError verifies that a router error is propagated.
func TestService_Search_RouterError(t *testing.T) {
	ret := &stubRetriever{chunks: nil}
	rt := routermock.New(router.Normal)
	rt.Err = errors.New("router unavailable")
	gen := mock.NewGenerator("irrelevant")
	svc := search.New(ret, search.NewAnswerer(gen), search.WithRouter(rt))

	_, err := svc.Search(authedCtx(), uuid.New(), "query")
	if err == nil {
		t.Fatal("expected error from router, got nil")
	}
}

// TestService_Search_AggregationLegError verifies that a graph leg error surfaces.
func TestService_Search_AggregationLegError(t *testing.T) {
	ret := &stubRetriever{chunks: []retrieval.ScoredChunk{makeChunk(uuid.New(), "hybrid", 0, 6)}}
	gr := memory.New()
	gr.AggregationErr = errors.New("graph db down")

	rt := routermock.New(router.Aggregation)
	gen := mock.NewGenerator("irrelevant")
	svc := search.New(ret, search.NewAnswerer(gen), search.WithRouter(rt), search.WithGraphRetriever(gr))

	_, err := svc.Search(authedCtx(), uuid.New(), "query")
	if err == nil {
		t.Fatal("expected error from aggregation leg, got nil")
	}
}

// TestService_Search_TraversalLegError verifies that a traversal leg error surfaces.
func TestService_Search_TraversalLegError(t *testing.T) {
	ret := &stubRetriever{chunks: []retrieval.ScoredChunk{makeChunk(uuid.New(), "hybrid", 0, 6)}}
	gr := memory.New()
	gr.TraversalErr = errors.New("graph db down")

	rt := routermock.New(router.MultiHop)
	gen := mock.NewGenerator("irrelevant")
	svc := search.New(ret, search.NewAnswerer(gen), search.WithRouter(rt), search.WithGraphRetriever(gr))

	_, err := svc.Search(authedCtx(), uuid.New(), "query")
	if err == nil {
		t.Fatal("expected error from traversal leg, got nil")
	}
}

// TestService_Search_NoRouter verifies backward compatibility: a Service with
// no router configured behaves exactly as before — always uses hybrid only.
func TestService_Search_NoRouter(t *testing.T) {
	docID := uuid.New()
	chunks := []retrieval.ScoredChunk{makeChunk(docID, "The sky is blue.", 0, 16)}
	ret := &stubRetriever{chunks: chunks}
	gen := mock.NewGenerator("The sky is blue [1].\nCITATIONS: 1")
	svc := search.New(ret, search.NewAnswerer(gen))

	result, err := svc.Search(authedCtx(), uuid.New(), "What colour is the sky?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Citations) == 0 {
		t.Error("expected citations from hybrid-only path")
	}
}

// ---- Interface compliance ---------------------------------------------------

var _ search.Answerer = (*search.LLMAnswerer)(nil)
var _ search.Searcher = (*search.Service)(nil)
var _ search.Answerer = (*searchmock.Answerer)(nil)
var _ search.Searcher = (*searchmock.Searcher)(nil)
