package canonical

import (
	"context"
	"fmt"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/llm"
)

// LLMAliasJudge answers whether two differently-worded entity mentions
// refer to the same real-world entity, using an LLM as the judge.
type LLMAliasJudge struct {
	Generator llm.Generator
}

const aliasVerdictPrefix = "VERDICT:"

// SameEntity asks the LLM whether textA and textB, both of entityType,
// name the same real-world entity. A response with no parseable VERDICT:
// line -- or anything other than an exact SAME -- is treated as different
// rather than erroring or guessing: an incorrectly-skipped merge just
// leaves two identities that could still be merged on a later run, while
// an incorrectly-applied merge is much harder to notice or undo once
// entities have been repointed and the losing canonical row deleted.
func (j *LLMAliasJudge) SameEntity(ctx context.Context, entityType, textA, textB string) (bool, error) {
	resp, err := j.Generator.Generate(ctx, buildAliasPrompt(entityType, textA, textB))
	if err != nil {
		return false, fmt.Errorf("canonical: alias judge: generate: %w", err)
	}
	return parseAliasVerdict(resp), nil
}

func buildAliasPrompt(entityType, textA, textB string) string {
	var sb strings.Builder
	sb.WriteString("You are judging whether two names, both referring to a ")
	sb.WriteString(entityType)
	sb.WriteString(", plausibly name the same real-world entity -- for example a full name and a surname alone, or a name and its standard abbreviation. If they clearly could name two different things (e.g. two different people who happen to share a surname), or you're not confident, say DIFFERENT rather than guessing.\n\n")
	fmt.Fprintf(&sb, "Name A: %s\n", textA)
	fmt.Fprintf(&sb, "Name B: %s\n\n", textB)
	sb.WriteString("Respond with exactly one line, nothing else:\nVERDICT: SAME\nor\nVERDICT: DIFFERENT\n")
	return sb.String()
}

func parseAliasVerdict(resp string) bool {
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, aliasVerdictPrefix); ok {
			return strings.EqualFold(strings.TrimSpace(rest), "SAME")
		}
	}
	return false
}

var _ AliasJudge = (*LLMAliasJudge)(nil)
