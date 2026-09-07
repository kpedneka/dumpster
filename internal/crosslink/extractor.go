package crosslink

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/llm"
)

// Extractor infers a relation type for candidate cross-chunk entity pairs
// via an llm.Generator, the same interface internal/relation's Extractor
// builds on.
type Extractor struct {
	gen llm.Generator
}

// NewExtractor returns an Extractor backed by gen.
func NewExtractor(gen llm.Generator) *Extractor {
	return &Extractor{gen: gen}
}

// Extract reviews every candidate in a single LLM call and returns one
// Update per candidate the model gave an answer for -- a real relation
// type, or NoneRelation when the model found no clear relationship. A
// candidate the model's response never addresses (a malformed or
// truncated response) is simply absent from updates, left for a later run
// to pick up again rather than guessed at.
func (e *Extractor) Extract(ctx context.Context, candidates []Candidate) ([]Update, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	resp, err := e.gen.Generate(ctx, buildPrompt(candidates))
	if err != nil {
		return nil, fmt.Errorf("crosslink: generate: %w", err)
	}
	answers := parseResponse(resp)

	var updates []Update
	for i, c := range candidates {
		rel, ok := answers[i+1]
		if !ok {
			continue
		}
		updates = append(updates, Update{
			EntityAID: c.EntityAID, EntityBID: c.EntityBID,
			RelationType: rel,
			ChunkAID:     c.ChunkAID, ChunkBID: c.ChunkBID,
		})
	}
	return updates, nil
}

func buildPrompt(candidates []Candidate) string {
	var sb strings.Builder
	sb.WriteString("Below are candidate pairs of named things from a knowledge base, each drawn from a different passage (plus, where given, a connecting passage that links them through something in between). For each pair, based only on what the passages state together, decide whether there is a specific, direct relationship between the two named things (for example: one influenced, is a later version of, is derived from, or is part of the history of the other). If the passages support a clear, specific relationship, respond with a short relation label (2-5 words). If they don't -- the passages just happen to exist in the same knowledge base without a specific connection between the two named things -- respond with NONE. Do not guess at a connection the text doesn't actually support.\n\n")
	for i, c := range candidates {
		fmt.Fprintf(&sb, "Candidate %d:\nPassage A (mentions %q):\n\"\"\"\n%s\n\"\"\"\n", i+1, c.EntityAText, c.ChunkAText)
		if c.BridgeChunkText != "" {
			fmt.Fprintf(&sb, "Connecting passage:\n\"\"\"\n%s\n\"\"\"\n", c.BridgeChunkText)
		}
		fmt.Fprintf(&sb, "Passage B (mentions %q):\n\"\"\"\n%s\n\"\"\"\n\n", c.EntityBText, c.ChunkBText)
	}
	sb.WriteString("For each candidate, respond with exactly this block (repeated once per candidate, in the same order given, nothing else in between):\nCANDIDATE: <candidate number>\nRELATION: <short relation label, or NONE>\n")
	return sb.String()
}

const (
	candidatePrefix = "CANDIDATE:"
	relationPrefix  = "RELATION:"
)

// parseResponse turns the model's raw response into a candidate-index ->
// relation label map, normalized to NoneRelation wherever the model said
// NONE (case-insensitively) or gave an empty label. A block with an
// unparseable CANDIDATE number is skipped entirely.
func parseResponse(resp string) map[int]string {
	answers := make(map[int]string)
	var cur int
	var haveCandidate bool
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, candidatePrefix):
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, candidatePrefix)))
			haveCandidate = err == nil
			if haveCandidate {
				cur = n
			}
		case strings.HasPrefix(line, relationPrefix):
			if !haveCandidate {
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
