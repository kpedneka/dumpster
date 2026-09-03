package theme

import (
	"context"
	"fmt"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/llm"
)

// maxEntitiesPerPrompt caps how many of a community's member entities go
// into its summarization prompt. The communities SelectTopCommunities picks
// are, by construction, the largest ones — exactly the ones most likely to
// have far more members than are useful to list; a representative sample
// (the most-mentioned members, per CommunityMembers' ordering — see
// pgstore's CommunityMembers) is enough context for a label, and this
// bounds prompt size/cost regardless of how large a single community gets.
const maxEntitiesPerPrompt = 30

// Summarizer generates one Theme per selected community via an
// llm.Generator, reusing the same interface internal/search.LLMAnswerer
// and internal/router.LLMRouter already build on.
type Summarizer struct {
	gen llm.Generator
}

// NewSummarizer returns a Summarizer backed by gen.
func NewSummarizer(gen llm.Generator) *Summarizer {
	return &Summarizer{gen: gen}
}

// Summarize generates one Theme per community in top, in order. A failure
// summarizing any one community fails the whole batch rather than
// returning a partial result silently missing an entry — this is
// user-triggered and cheap to retry by hand, same "manual, cheap to retry"
// precedent community detection's own recompute already established.
func (s *Summarizer) Summarize(ctx context.Context, top []CommunityMembers) ([]Theme, error) {
	themes := make([]Theme, 0, len(top))
	for _, cm := range top {
		resp, err := s.gen.Generate(ctx, buildPrompt(cm.Entities))
		if err != nil {
			return nil, fmt.Errorf("theme: generate community %d: %w", cm.CommunityID, err)
		}
		label, summary := parseResponse(resp)
		themes = append(themes, Theme{
			CommunityID: cm.CommunityID,
			Label:       label,
			Summary:     summary,
			EntityCount: len(cm.Entities),
		})
	}
	return themes, nil
}

func buildPrompt(entities []EntityRef) string {
	if len(entities) > maxEntitiesPerPrompt {
		entities = entities[:maxEntitiesPerPrompt]
	}
	var sb strings.Builder
	sb.WriteString("The following entities were found to form a tightly connected cluster within a knowledge base's entity graph:\n\n")
	for _, e := range entities {
		fmt.Fprintf(&sb, "- %s (%s)\n", e.Text, e.Type)
	}
	sb.WriteString("\nWrite a short label (3-6 words) for the overall theme connecting these entities, then one plain-language sentence describing what connects them. Do not use the words \"cluster\", \"community\", or \"graph\" — write for someone who has never heard those terms.\n")
	sb.WriteString("Respond in exactly this format, nothing else:\nLABEL: <label>\nSUMMARY: <one sentence>\n")
	return sb.String()
}

const (
	labelPrefix   = "LABEL:"
	summaryPrefix = "SUMMARY:"
	// fallbackLabel is used when the model's response doesn't include a
	// parseable LABEL: line — a malformed response shouldn't drop the
	// theme from the result entirely, since its Summary (if parsed) is
	// still informative on its own.
	fallbackLabel = "Untitled theme"
)

func parseResponse(resp string) (label, summary string) {
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, labelPrefix); ok {
			label = strings.TrimSpace(rest)
		}
		if rest, ok := strings.CutPrefix(line, summaryPrefix); ok {
			summary = strings.TrimSpace(rest)
		}
	}
	if label == "" {
		label = fallbackLabel
	}
	return label, summary
}
