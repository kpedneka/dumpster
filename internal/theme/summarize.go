package theme

import (
	"context"
	"fmt"
	"strconv"
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

// maxSignificantThemes caps how many themes a single Summarize call ever
// returns. The prompt already instructs the model to return at most this
// many; this is a defense-in-depth backstop against a model that doesn't
// follow that instruction, not the primary mechanism — the primary
// mechanism is the model's own judgment of what's genuinely significant
// (see buildPrompt), which is free to return fewer, including none, when
// nothing stands out.
const maxSignificantThemes = 3

// noneMarker is what the model is instructed to respond with when none of
// the candidate communities are distinctive enough to surface. Parsed back
// into an empty result rather than treated as a malformed response —
// "nothing stood out" is a legitimate, expected outcome here, not a
// failure to work around.
const noneMarker = "NONE"

// Summarizer generates themes for a KB's candidate communities via an
// llm.Generator, reusing the same interface internal/search.LLMAnswerer
// and internal/router.LLMRouter already build on.
type Summarizer struct {
	gen llm.Generator
}

// NewSummarizer returns a Summarizer backed by gen.
func NewSummarizer(gen llm.Generator) *Summarizer {
	return &Summarizer{gen: gen}
}

// Summarize reviews every community in candidates together, in a single LLM
// call, and returns a label/summary only for the ones judged truly
// significant — up to maxSignificantThemes, possibly zero.
//
// This is deliberately one combined call rather than one call per
// candidate (the earlier design). Whether a cluster is "significant" is an
// inherently comparative judgment — significant relative to what else is
// here — which an isolated per-community call can never make; it can only
// ever say "yes, this is a theme" about the one cluster in front of it.
// Putting every candidate in front of the model at once is what makes "up
// to 3, only what stands out" possible at all.
//
// The second return value is the model's own brief account of what set the
// returned themes apart from the other candidates it reviewed (empty if it
// didn't supply one, or no themes were returned) — the raw material for a
// caller-built "there might be more" note. It's captured here, in the same
// call, because that comparative judgment only exists at the moment the
// model is actually weighing candidates against each other; reconstructing
// a reason later from stored labels/summaries alone would just be a
// generic guess, not the model's actual reasoning.
func (s *Summarizer) Summarize(ctx context.Context, candidates []CommunityMembers) ([]Theme, string, error) {
	if len(candidates) == 0 {
		return nil, "", nil
	}
	resp, err := s.gen.Generate(ctx, buildPrompt(candidates))
	if err != nil {
		return nil, "", fmt.Errorf("theme: generate: %w", err)
	}
	themes, reason := parseResponse(resp, candidates)
	return themes, reason, nil
}

func buildPrompt(candidates []CommunityMembers) string {
	var sb strings.Builder
	sb.WriteString("Below are several tightly connected clusters of entities found in a knowledge base's entity graph, each tagged with a community ID.\n\n")
	for _, cm := range candidates {
		entities := cm.Entities
		if len(entities) > maxEntitiesPerPrompt {
			entities = entities[:maxEntitiesPerPrompt]
		}
		fmt.Fprintf(&sb, "Community %d:\n", cm.CommunityID)
		for _, e := range entities {
			fmt.Fprintf(&sb, "- %s (%s)\n", e.Text, e.Type)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Not every cluster here necessarily represents a genuinely distinctive theme — some may be too generic, scattered, or unremarkable to be worth surfacing to a reader. Review all of them together and pick out only the ones that truly stand out as a coherent, meaningful theme.\n\n")
	fmt.Fprintf(&sb, "Return at most %d, ordered most significant first. If none of them stand out, that's a valid answer — say so rather than forcing a label onto something unremarkable.\n\n", maxSignificantThemes)
	sb.WriteString("For each theme you choose, respond with exactly this block (repeated once per theme, nothing else in between):\nCOMMUNITY: <community id>\nLABEL: <short label, 3-6 words>\nSUMMARY: <one plain-language sentence describing what connects them>\n\n")
	sb.WriteString("Do not use the words \"cluster\", \"community\", or \"graph\" in the label or summary — write for someone who has never heard those terms.\n\n")
	sb.WriteString("If you return at least one theme, add one more line after all of them: NOTE: <one complete, standalone sentence explaining what specifically set the theme(s) you picked apart from the other candidates above>. Write it so it can be shown to a reader exactly as-is, with nothing else added before or after it — it should stand on its own, not read like a clause meant to be spliced into another sentence.\n\n")
	fmt.Fprintf(&sb, "If nothing stands out, respond with exactly the single word %s and nothing else.\n", noneMarker)
	return sb.String()
}

const (
	communityPrefix = "COMMUNITY:"
	labelPrefix     = "LABEL:"
	summaryPrefix   = "SUMMARY:"
	notePrefix      = "NOTE:"
	// fallbackLabel is used when a parsed theme block is missing a
	// parseable LABEL: line — a malformed label shouldn't drop the whole
	// theme, since its Summary (if parsed) is still informative on its
	// own.
	fallbackLabel = "Untitled theme"
)

// parseResponse turns the model's raw response into Themes plus its
// trailing NOTE: line, if any. Each COMMUNITY: line starts a new theme
// block; its id is matched back against candidates for EntityCount (and to
// validate it — a hallucinated id that doesn't match a real candidate is
// dropped rather than trusted). Response text with no valid COMMUNITY:
// blocks at all — including the explicit NONE marker, but also any other
// malformed response — yields an empty, non-error result, since "nothing
// stood out" and "the model didn't follow the format" are both better
// handled by surfacing no themes than by fabricating one.
func parseResponse(resp string, candidates []CommunityMembers) (themes []Theme, note string) {
	entityCounts := make(map[int]int, len(candidates))
	for _, cm := range candidates {
		entityCounts[cm.CommunityID] = len(cm.Entities)
	}

	var cur *Theme
	flush := func() {
		if cur == nil {
			return
		}
		if count, ok := entityCounts[cur.CommunityID]; ok {
			if cur.Label == "" {
				cur.Label = fallbackLabel
			}
			cur.EntityCount = count
			themes = append(themes, *cur)
		}
		cur = nil
	}

	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, communityPrefix); ok {
			flush()
			if id, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil {
				cur = &Theme{CommunityID: id}
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, notePrefix); ok {
			flush()
			note = strings.TrimSpace(rest)
			continue
		}
		if cur == nil {
			continue
		}
		if rest, ok := strings.CutPrefix(line, labelPrefix); ok {
			cur.Label = strings.TrimSpace(rest)
		} else if rest, ok := strings.CutPrefix(line, summaryPrefix); ok {
			cur.Summary = strings.TrimSpace(rest)
		}
	}
	flush()

	if len(themes) > maxSignificantThemes {
		themes = themes[:maxSignificantThemes]
	}
	if len(themes) == 0 {
		note = ""
	}
	return themes, note
}
