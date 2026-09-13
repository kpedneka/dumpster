package canonical

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/llm"
)

// LLMAliasJudge answers, for a batch of candidate mentions, which (if any)
// refer to the same real-world entity as a given text, using an LLM as the
// judge.
type LLMAliasJudge struct {
	Generator llm.Generator
}

const (
	aliasCandidatePrefix = "CANDIDATE:"
	aliasVerdictPrefix   = "VERDICT:"
)

// SameEntityBatch asks the LLM, in one call, whether each of candidates
// names the same real-world entity (of entityType) as textA. Returns one
// bool per candidate in the same order given -- true only for an exact
// SAME verdict; anything else (DIFFERENT, or a candidate the response never
// addressed at all, e.g. a malformed or truncated response) is false. An
// incorrectly-skipped merge just leaves two identities that could still be
// merged on a later run; an incorrectly-applied merge is much harder to
// notice or undo once entities have been repointed and the losing
// canonical row deleted -- so every failure mode here defaults to "not the
// same" rather than guessing.
func (j *LLMAliasJudge) SameEntityBatch(ctx context.Context, entityType, textA string, candidates []string) ([]bool, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	resp, err := j.Generator.Generate(ctx, buildAliasBatchPrompt(entityType, textA, candidates))
	if err != nil {
		return nil, fmt.Errorf("canonical: alias judge: generate: %w", err)
	}
	verdicts := parseAliasBatchVerdicts(resp)

	out := make([]bool, len(candidates))
	for i := range candidates {
		out[i] = verdicts[i+1]
	}
	return out, nil
}

func buildAliasBatchPrompt(entityType, textA string, candidates []string) string {
	var sb strings.Builder
	sb.WriteString("You are judging whether a name plausibly names the same real-world ")
	sb.WriteString(entityType)
	sb.WriteString(" as a reference name -- for example a full name and a surname alone, or a name and its standard abbreviation. If a candidate could clearly name a different thing (e.g. a different person who happens to share a surname), or you're not confident, say DIFFERENT rather than guessing.\n\n")
	fmt.Fprintf(&sb, "Reference name: %s\n\n", textA)
	sb.WriteString("Candidates:\n")
	for i, c := range candidates {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, c)
	}
	sb.WriteString("\nFor each candidate, respond with exactly this block (repeated once per candidate, in the same order given, nothing else in between):\nCANDIDATE: <candidate number>\nVERDICT: SAME\nor\nCANDIDATE: <candidate number>\nVERDICT: DIFFERENT\n")
	return sb.String()
}

// parseAliasBatchVerdicts turns the model's raw response into a
// 1-indexed-candidate-number -> verdict map. A block with an unparseable
// CANDIDATE number is skipped entirely (SameEntityBatch's caller treats an
// absent entry as false, same as any other unaddressed candidate).
func parseAliasBatchVerdicts(resp string) map[int]bool {
	verdicts := make(map[int]bool)
	var cur int
	var haveCandidate bool
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, aliasCandidatePrefix):
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, aliasCandidatePrefix)))
			haveCandidate = err == nil
			if haveCandidate {
				cur = n
			}
		case strings.HasPrefix(line, aliasVerdictPrefix):
			if !haveCandidate {
				continue
			}
			verdict := strings.TrimSpace(strings.TrimPrefix(line, aliasVerdictPrefix))
			verdicts[cur] = strings.EqualFold(verdict, "SAME")
		}
	}
	return verdicts
}

var _ AliasJudge = (*LLMAliasJudge)(nil)
