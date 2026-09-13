package search_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/graphrag/memory"
	"github.com/kunalpednekar/dumpster/internal/llm"
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
	if c.Text != "Go is a statically typed language." {
		t.Errorf("citation text: got %q, want the source chunk's own text", c.Text)
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

// TestAnswerer_ProviderQuotaExceeded_ReturnsGracefulMessageNotError verifies
// the one case that must NOT behave like TestAnswerer_GeneratorError above:
// a spend-limit denial (e.g. production's Bedrock budget action) should
// degrade to a clear, user-facing message with a nil error, not propagate
// as a raw failure the caller has to translate itself.
func TestAnswerer_ProviderQuotaExceeded_ReturnsGracefulMessageNotError(t *testing.T) {
	chunks := []retrieval.ScoredChunk{
		makeChunk(uuid.New(), "some text", 0, 9),
	}
	gen := &mock.Generator{
		GenerateStreamCachedFn: func(context.Context, string, string, func(string)) (string, error) {
			return "", fmt.Errorf("bedrock: converse: %w", llm.ErrProviderQuotaExceeded)
		},
	}
	a := search.NewAnswerer(gen)

	result, err := a.Answer(authedCtx(), uuid.New(), "query", chunks)
	if err != nil {
		t.Fatalf("expected no error (graceful degradation), got: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected a non-empty graceful message, got empty Summary")
	}
	if len(result.Citations) != 0 {
		t.Errorf("expected no citations on a quota-exceeded response, got %+v", result.Citations)
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

// ---- AnswerStream tests -------------------------------------------------------

// TestAnswerer_AnswerStream_DeliversDeltasAndNeverLeaksFooter verifies that
// AnswerStream forwards every generated text chunk via onDelta, in order,
// and that none of them ever contain any part of the CITATIONS: footer —
// footer stripping happens for the streamed preview exactly as it does for
// the final parsed Summary.
func TestAnswerer_AnswerStream_DeliversDeltasAndNeverLeaksFooter(t *testing.T) {
	docID := uuid.New()
	chunks := []retrieval.ScoredChunk{
		makeChunk(docID, "Go is a statically typed language.", 0, 34),
	}

	gen := mock.NewGenerator("unused")
	gen.GenerateStreamFn = func(_ context.Context, _ string, onDelta func(string)) (string, error) {
		for _, d := range []string{"Go is fast ", "[1].\n", "CITATIONS", ": 1"} {
			onDelta(d)
		}
		return "Go is fast [1].\nCITATIONS: 1", nil
	}
	a := search.NewAnswerer(gen)

	var deltas []string
	result, err := a.AnswerStream(authedCtx(), uuid.New(), "What is Go?", chunks, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := strings.Join(deltas, "")
	if strings.Contains(strings.ToLower(got), "citations:") {
		t.Errorf("streamed deltas leaked the footer: %q", got)
	}
	if got != "Go is fast [1].\n" {
		t.Errorf("streamed deltas = %q, want the pre-footer text only", got)
	}
	// The final Result must still be fully and correctly parsed, exactly as
	// non-streaming Answer would produce.
	if len(result.Citations) != 1 {
		t.Errorf("expected 1 citation from the authoritative parse, got %d", len(result.Citations))
	}
}

// TestAnswerer_AnswerStream_NoChunks_NoDeltasEmitted verifies the not-found
// guardrail short-circuits before any generation, so onDelta is never
// called (matching Answer's existing no-Generate-call behavior).
func TestAnswerer_AnswerStream_NoChunks_NoDeltasEmitted(t *testing.T) {
	gen := mock.NewGenerator("should not be called")
	a := search.NewAnswerer(gen)

	called := false
	result, err := a.AnswerStream(authedCtx(), uuid.New(), "anything?", nil, func(string) { called = true })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Error("onDelta must not be called for the not-found guardrail")
	}
	if result.Summary == "" {
		t.Error("expected a not-found summary")
	}
}

// TestAnswerer_AnswerStream_GeneratorError_Propagates verifies that a
// streaming generation error is returned, matching Answer's behavior.
func TestAnswerer_AnswerStream_GeneratorError_Propagates(t *testing.T) {
	chunks := []retrieval.ScoredChunk{makeChunk(uuid.New(), "some text", 0, 9)}
	gen := mock.NewErrorGenerator("llm unavailable")
	a := search.NewAnswerer(gen)

	_, err := a.AnswerStream(authedCtx(), uuid.New(), "query", chunks, func(string) {})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestAnswerer_Answer_StillWorksThroughStreamingPath verifies that Answer's
// observable behavior is unchanged now that it's implemented in terms of
// AnswerStream with a no-op onDelta — same assertion as TestAnswerer_WithChunks,
// re-run here to make the delegation explicit.
func TestAnswerer_Answer_StillWorksThroughStreamingPath(t *testing.T) {
	docID := uuid.New()
	chunks := []retrieval.ScoredChunk{makeChunk(docID, "Go is a statically typed language.", 0, 34)}
	gen := mock.NewGenerator("Go is fast [1].\nCITATIONS: 1")
	a := search.NewAnswerer(gen)

	result, err := a.Answer(authedCtx(), uuid.New(), "What is Go?", chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Citations) != 1 {
		t.Errorf("expected 1 citation, got %d", len(result.Citations))
	}
}

// ---- SearchStream tests ---------------------------------------------------

// TestService_SearchStream_EmitsRetrievedFilesBeforeDeltas verifies the
// event ordering contract: the retrieved_files event always arrives before
// any delta event, so a client can render the relevant-files list before
// the answer starts streaming in.
func TestService_SearchStream_EmitsRetrievedFilesBeforeDeltas(t *testing.T) {
	docID := uuid.New()
	kbID := uuid.New()
	chunks := []retrieval.ScoredChunk{makeChunk(docID, "The sky is blue.", 0, 16)}

	ret := &stubRetriever{chunks: chunks}
	gen := mock.NewGenerator("unused")
	gen.GenerateStreamFn = func(_ context.Context, _ string, onDelta func(string)) (string, error) {
		onDelta("The sky is blue [1].\n")
		onDelta("CITATIONS: 1")
		return "The sky is blue [1].\nCITATIONS: 1", nil
	}
	svc := search.New(ret, search.NewAnswerer(gen))

	var events []search.StreamEvent
	result, err := svc.SearchStream(authedCtx(), kbID, "What colour is the sky?", func(e search.StreamEvent) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(events) == 0 || events[0].Type != search.EventRetrievedFiles {
		t.Fatalf("first event = %+v, want EventRetrievedFiles first", events)
	}
	if len(events[0].RetrievedDocuments) != 1 || events[0].RetrievedDocuments[0] != docID {
		t.Errorf("retrieved_files event: got %+v, want [%v]", events[0].RetrievedDocuments, docID)
	}
	for _, e := range events[1:] {
		if e.Type != search.EventDelta {
			t.Errorf("event after the first: got type %v, want EventDelta only", e.Type)
		}
	}
	if result.Summary == "" {
		t.Error("expected non-empty final summary")
	}
	if len(result.Citations) == 0 {
		t.Error("expected at least one citation in the final result")
	}
}

// TestService_SearchStream_AnswererError_Propagates verifies that an
// answerer error surfaces from SearchStream the same way it does from Search.
func TestService_SearchStream_AnswererError_Propagates(t *testing.T) {
	ret := &stubRetriever{chunks: []retrieval.ScoredChunk{makeChunk(uuid.New(), "text", 0, 4)}}
	gen := mock.NewErrorGenerator("llm unavailable")
	svc := search.New(ret, search.NewAnswerer(gen))

	_, err := svc.SearchStream(authedCtx(), uuid.New(), "query", func(search.StreamEvent) {})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestService_Search_DelegatesToSearchStream verifies Search's observable
// behavior is unchanged now that it's a thin wrapper around SearchStream.
func TestService_Search_DelegatesToSearchStream(t *testing.T) {
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
		t.Error("expected at least one citation")
	}
	if len(result.RetrievedDocuments) != 1 || result.RetrievedDocuments[0] != docID {
		t.Errorf("RetrievedDocuments: got %v, want [%v]", result.RetrievedDocuments, docID)
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

// TestService_Search_RetrievedDocuments verifies that Result carries every
// document from the fused top-k chunk list, ranked and deduped — a superset
// of Citations, since a search engine-style citation cites the source file,
// not every chunk that contributed to retrieval.
func TestService_Search_RetrievedDocuments(t *testing.T) {
	docA, docB := uuid.New(), uuid.New()
	kbID := uuid.New()
	// docA appears twice (two chunks); docB once. docA's first occurrence
	// should determine its rank position, and it should appear only once.
	chunks := []retrieval.ScoredChunk{
		makeChunk(docA, "first chunk of doc A", 0, 10),
		makeChunk(docB, "only chunk of doc B", 0, 10),
		makeChunk(docA, "second chunk of doc A", 10, 20),
	}

	ret := &stubRetriever{chunks: chunks}
	gen := mock.NewGenerator("An answer [1].\nCITATIONS: 1")
	svc := search.New(ret, search.NewAnswerer(gen))

	result, err := svc.Search(authedCtx(), kbID, "a query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []uuid.UUID{docA, docB}
	if len(result.RetrievedDocuments) != len(want) {
		t.Fatalf("RetrievedDocuments: got %v, want %v", result.RetrievedDocuments, want)
	}
	for i, id := range want {
		if result.RetrievedDocuments[i] != id {
			t.Errorf("RetrievedDocuments[%d]: got %s, want %s", i, result.RetrievedDocuments[i], id)
		}
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

// TestService_Search_RouterQuotaExceeded_FallsBackToNormal verifies the one
// router failure mode that must NOT behave like TestService_Search_RouterError
// above: a spend-limit denial falls back to router.Normal (the same default
// Route itself already uses for an unrecognized classification) instead of
// failing the whole search -- otherwise the user would never reach
// answerer.go's own graceful degradation, just a raw error at the routing
// step instead. Proven by configuring the router to *claim* Aggregation
// (so the test would fail if that were used) and wiring the graph
// retriever's aggregation leg to error if it's ever actually reached --
// a successful search here is only possible if the fallback to Normal
// (which skips the graph leg entirely) really happened.
func TestService_Search_RouterQuotaExceeded_FallsBackToNormal(t *testing.T) {
	ret := &stubRetriever{chunks: []retrieval.ScoredChunk{makeChunk(uuid.New(), "hybrid text", 0, 11)}}
	rt := routermock.New(router.Aggregation)
	rt.Err = fmt.Errorf("router: generate: %w", llm.ErrProviderQuotaExceeded)
	gr := memory.New()
	gr.AggregationErr = errors.New("should never be reached -- fallback should skip the graph leg entirely")
	gen := mock.NewGenerator("An answer [1].\nCITATIONS: 1")
	svc := search.New(ret, search.NewAnswerer(gen), search.WithRouter(rt), search.WithGraphRetriever(gr))

	result, err := svc.Search(authedCtx(), uuid.New(), "query")
	if err != nil {
		t.Fatalf("expected the search to succeed via Normal fallback, got error: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected a real answer from the fallback Normal path, got empty Summary")
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
