package search

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

// notFoundSummary is returned verbatim when retrieval yields no relevant chunks.
const notFoundSummary = "I could not find relevant information to answer this question."

// LLMAnswerer implements Answerer using a language model Generator.
// It builds a prompt that numbers each source chunk, instructs the model to
// cite claims using [N] markers, and parses a "CITATIONS: 1,2,3" footer to
// resolve which chunks were actually used.
type LLMAnswerer struct {
	gen llm.Generator
}

// NewAnswerer returns an LLMAnswerer backed by gen.
func NewAnswerer(gen llm.Generator) *LLMAnswerer {
	return &LLMAnswerer{gen: gen}
}

// Answer builds a grounding prompt from chunks, calls the Generator, and
// parses the structured response into a Result. If chunks is empty, it
// returns the not-found guardrail immediately without calling the Generator.
func (a *LLMAnswerer) Answer(ctx context.Context, _ uuid.UUID, query string, chunks []retrieval.ScoredChunk) (Result, error) {
	if len(chunks) == 0 {
		return Result{Summary: notFoundSummary}, nil
	}

	prompt := buildPrompt(query, chunks)
	resp, err := a.gen.Generate(ctx, prompt)
	if err != nil {
		return Result{}, fmt.Errorf("search: generate: %w", err)
	}

	return parseResponse(resp, chunks), nil
}

// buildPrompt constructs the grounding prompt, numbering each chunk [1..N].
func buildPrompt(query string, chunks []retrieval.ScoredChunk) string {
	var sb strings.Builder
	sb.WriteString("You are a knowledge-base assistant. Answer the question using ONLY the source chunks below.\n")
	sb.WriteString("Cite every factual claim with [N] where N is the chunk number.\n")
	sb.WriteString("If the chunks do not contain enough information, reply with exactly: \"" + notFoundSummary + "\"\n\n")
	sb.WriteString("Source chunks:\n")
	for i, sc := range chunks {
		fmt.Fprintf(&sb, "[%d] (document:%s chars:%d-%d)\n%s\n\n",
			i+1, sc.DocumentID, sc.CharStart, sc.CharEnd, sc.Text)
	}
	fmt.Fprintf(&sb, "Question: %s\n\n", query)
	sb.WriteString("Write your answer using [N] citations, then on a new line write \"CITATIONS: \" followed by a comma-separated list of the chunk numbers you relied on.\n")
	return sb.String()
}

// parseResponse splits the generator output into summary text and cited chunk
// indices. Indices outside [1, len(chunks)] are silently ignored.
//
// Splitting uses LastIndex on the uppercased response so that:
//   - chunk text containing "CITATIONS:" (e.g. academic papers) does not cause
//     a false split — we want the model's footer at the end, not an echo mid-body.
//   - LLM output using "Citations:" (mixed case) is still recognised.
func parseResponse(resp string, chunks []retrieval.ScoredChunk) Result {
	if strings.TrimSpace(resp) == notFoundSummary {
		return Result{Summary: notFoundSummary}
	}

	// Search case-insensitively by lowercasing both sides. ToLower is safe to
	// use for byte-offset computation because lowercasing CITATIONS: (ASCII)
	// is a no-op, and ToLower never increases byte length, preserving offsets
	// into the original string. ToUpper can shrink multi-byte Unicode
	// characters (e.g. ı → I), making idx point to the wrong position.
	const citationsMarker = "citations:"
	idx := strings.LastIndex(strings.ToLower(resp), citationsMarker)
	if idx == -1 {
		return Result{Summary: strings.TrimSpace(resp)}
	}

	summary := strings.TrimSpace(resp[:idx])
	citationsSection := resp[idx+len(citationsMarker):]

	seen := make(map[int]bool)
	var citations []Citation
	for _, raw := range strings.Split(citationsSection, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 1 || n > len(chunks) || seen[n] {
			continue
		}
		seen[n] = true
		sc := chunks[n-1]
		citations = append(citations, Citation{
			Number:     n,
			DocumentID: sc.DocumentID,
			ChunkID:    sc.ID,
			CharStart:  sc.CharStart,
			CharEnd:    sc.CharEnd,
		})
	}

	return Result{Summary: summary, Citations: citations}
}
