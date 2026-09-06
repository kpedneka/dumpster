package relation

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/llm"
)

const (
	// maxPairsPerChunk caps how many of a chunk's candidate pairs go into
	// the prompt, keeping prompt size bounded regardless of how many
	// entities a single chunk mentions -- same rationale as
	// theme.maxEntitiesPerPrompt. A chunk with more candidates than this
	// simply has its excess pairs left for a later run (CandidateChunks
	// only selects not-yet-checked pairs, so nothing is lost, only
	// deferred).
	maxPairsPerChunk = 15
)

// Extractor infers a relation type for candidate entity pairs via an
// llm.Generator, reusing the same interface theme.Summarizer and
// intrusion.Tester already build on.
type Extractor struct {
	gen llm.Generator
}

// NewExtractor returns an Extractor backed by gen.
func NewExtractor(gen llm.Generator) *Extractor {
	return &Extractor{gen: gen}
}

// Extract reviews every chunk's candidate pairs in a single LLM call and
// returns one Update per pair the model gave an answer for -- a real
// relation type, or NoneRelation when the model found no clear
// relationship. A pair the model's response never addresses (a malformed
// or truncated response) is simply absent from the result, left for a
// later run to pick up again rather than guessed at.
func (e *Extractor) Extract(ctx context.Context, chunks []ChunkCandidates) ([]Update, error) {
	trimmed := make([]ChunkCandidates, len(chunks))
	for i, c := range chunks {
		pairs := c.Pairs
		if len(pairs) > maxPairsPerChunk {
			pairs = pairs[:maxPairsPerChunk]
		}
		trimmed[i] = ChunkCandidates{ChunkID: c.ChunkID, Text: c.Text, Pairs: pairs}
	}

	var anyPairs bool
	for _, c := range trimmed {
		if len(c.Pairs) > 0 {
			anyPairs = true
			break
		}
	}
	if !anyPairs {
		return nil, nil
	}

	resp, err := e.gen.Generate(ctx, buildPrompt(trimmed))
	if err != nil {
		return nil, fmt.Errorf("relation: generate: %w", err)
	}
	answers := parseResponse(resp)

	var updates []Update
	for ci, c := range trimmed {
		for pi, p := range c.Pairs {
			relation, ok := answers[answerKey{chunk: ci + 1, pair: pi + 1}]
			if !ok {
				continue
			}
			updates = append(updates, Update{
				ChunkID: c.ChunkID, EntityAID: p.EntityAID, EntityBID: p.EntityBID,
				RelationType: relation,
			})
		}
	}
	return updates, nil
}

func buildPrompt(chunks []ChunkCandidates) string {
	var sb strings.Builder
	sb.WriteString("Below are text passages from a knowledge base, each followed by candidate pairs of entities found mentioned together in that passage.\n\n")
	sb.WriteString("For each pair, if the passage explicitly states or clearly implies a specific relationship between the two entities, respond with a short relation label (2-4 words, e.g. \"founded\", \"works at\", \"depends on\", \"part of\", \"located in\"). If the passage doesn't support a clear, specific relationship -- the two are just mentioned in the same passage without one connecting to the other -- respond with NONE. Do not guess at a relationship the text doesn't actually support.\n\n")
	for ci, c := range chunks {
		if len(c.Pairs) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "Chunk %d:\n\"\"\"\n%s\n\"\"\"\nPairs:\n", ci+1, c.Text)
		for pi, p := range c.Pairs {
			fmt.Fprintf(&sb, "%d. %s -- %s\n", pi+1, p.TextA, p.TextB)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("For each pair in each chunk, respond with exactly this block (repeated once per pair, in the same order given, nothing else in between):\nCHUNK: <chunk number>\nPAIR: <pair number>\nRELATION: <short relation label, or NONE>\n")
	return sb.String()
}

const (
	chunkPrefix    = "CHUNK:"
	pairPrefix     = "PAIR:"
	relationPrefix = "RELATION:"
)

// answerKey identifies one pair's answer by its 1-indexed position in the
// prompt -- chunk number and pair-within-chunk number, exactly as sent, so
// parsing never depends on matching relation label text back to a
// particular entity pair.
type answerKey struct{ chunk, pair int }

// parseResponse turns the model's raw response into a keyed set of
// relation labels, normalized to NoneRelation wherever the model said
// NONE (case-insensitively) or gave an empty label. A block with an
// unparseable CHUNK/PAIR number is skipped entirely -- CandidateChunks
// will simply offer that pair again on a later run, which is safer than
// guessing which pair a malformed block was meant to answer.
func parseResponse(resp string) map[answerKey]string {
	answers := make(map[answerKey]string)
	var cur answerKey
	var haveChunk, havePair bool
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, chunkPrefix):
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, chunkPrefix)))
			haveChunk = err == nil
			if haveChunk {
				cur.chunk = n
			}
			havePair = false
		case strings.HasPrefix(line, pairPrefix):
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, pairPrefix)))
			havePair = err == nil
			if havePair {
				cur.pair = n
			}
		case strings.HasPrefix(line, relationPrefix):
			if !haveChunk || !havePair {
				continue
			}
			relation := strings.TrimSpace(strings.TrimPrefix(line, relationPrefix))
			if relation == "" || strings.EqualFold(relation, "NONE") {
				relation = NoneRelation
			} else {
				relation = strings.ToLower(relation)
			}
			answers[cur] = relation
		}
	}
	return answers
}
