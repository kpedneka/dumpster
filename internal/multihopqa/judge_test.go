package multihopqa_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/multihopqa"
)

func TestLLMJudge_ParsesCorrectVerdict(t *testing.T) {
	gen := &llmmock.Generator{GenerateFn: func(_ context.Context, _ string) (string, error) {
		return "VERDICT: CORRECT\n", nil
	}}
	judge := &multihopqa.LLMJudge{Generator: gen}

	correct, err := judge.JudgeCorrect(context.Background(), "q", "expected", "actual")
	if err != nil {
		t.Fatalf("JudgeCorrect: %v", err)
	}
	if !correct {
		t.Error("expected correct=true")
	}
}

func TestLLMJudge_ParsesIncorrectVerdict(t *testing.T) {
	gen := &llmmock.Generator{GenerateFn: func(_ context.Context, _ string) (string, error) {
		return "VERDICT: INCORRECT\n", nil
	}}
	judge := &multihopqa.LLMJudge{Generator: gen}

	correct, err := judge.JudgeCorrect(context.Background(), "q", "expected", "actual")
	if err != nil {
		t.Fatalf("JudgeCorrect: %v", err)
	}
	if correct {
		t.Error("expected correct=false")
	}
}

func TestLLMJudge_MalformedResponseTreatedAsIncorrect(t *testing.T) {
	gen := &llmmock.Generator{GenerateFn: func(_ context.Context, _ string) (string, error) {
		return "I'm not sure how to answer this.", nil
	}}
	judge := &multihopqa.LLMJudge{Generator: gen}

	correct, err := judge.JudgeCorrect(context.Background(), "q", "expected", "actual")
	if err != nil {
		t.Fatalf("JudgeCorrect: %v", err)
	}
	if correct {
		t.Error("a malformed judge response should never count as correct")
	}
}

func TestLLMJudge_PromptIncludesAllThreeInputs(t *testing.T) {
	var gotPrompt string
	gen := &llmmock.Generator{GenerateFn: func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "VERDICT: CORRECT", nil
	}}
	judge := &multihopqa.LLMJudge{Generator: gen}

	_, err := judge.JudgeCorrect(context.Background(), "What is X?", "X is Y", "X is definitely Y")
	if err != nil {
		t.Fatalf("JudgeCorrect: %v", err)
	}
	for _, want := range []string{"What is X?", "X is Y", "X is definitely Y"} {
		if !strings.Contains(gotPrompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, gotPrompt)
		}
	}
}

func TestLLMJudge_PropagatesGenerateError(t *testing.T) {
	gen := &llmmock.Generator{GenerateFn: func(_ context.Context, _ string) (string, error) {
		return "", errors.New("boom")
	}}
	judge := &multihopqa.LLMJudge{Generator: gen}

	_, err := judge.JudgeCorrect(context.Background(), "q", "expected", "actual")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}
