package multihopqa

import (
	"context"
	"fmt"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/llm"
)

// LLMJudge answers whether an actual answer correctly conveys an expected
// answer's key facts, using an LLM as the judge -- natural-language
// equivalence isn't something exact string comparison can check.
type LLMJudge struct {
	Generator llm.Generator
}

const verdictPrefix = "VERDICT:"

// JudgeCorrect asks the LLM whether actualAnswer correctly conveys
// expectedAnswer's key facts for question. A response with no parseable
// VERDICT: line is treated as incorrect rather than erroring -- a
// malformed judge response shouldn't be silently counted as a pass.
func (j *LLMJudge) JudgeCorrect(ctx context.Context, question, expectedAnswer, actualAnswer string) (bool, error) {
	resp, err := j.Generator.Generate(ctx, buildJudgePrompt(question, expectedAnswer, actualAnswer))
	if err != nil {
		return false, fmt.Errorf("multihopqa: judge: generate: %w", err)
	}
	return parseVerdict(resp), nil
}

func buildJudgePrompt(question, expectedAnswer, actualAnswer string) string {
	var sb strings.Builder
	sb.WriteString("You are judging whether a generated answer correctly conveys the key facts of an expected answer, for the question below. Minor differences in wording or extra detail are fine; a missing or contradicted key fact is not.\n\n")
	fmt.Fprintf(&sb, "Question: %s\n\n", question)
	fmt.Fprintf(&sb, "Expected answer: %s\n\n", expectedAnswer)
	fmt.Fprintf(&sb, "Generated answer: %s\n\n", actualAnswer)
	sb.WriteString("Respond with exactly one line, nothing else:\nVERDICT: CORRECT\nor\nVERDICT: INCORRECT\n")
	return sb.String()
}

func parseVerdict(resp string) bool {
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, verdictPrefix); ok {
			return strings.EqualFold(strings.TrimSpace(rest), "CORRECT")
		}
	}
	return false
}

var _ AnswerJudge = (*LLMJudge)(nil)
