package canonical_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/canonical"
	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
)

func TestLLMAliasJudge_ParsesSameVerdict(t *testing.T) {
	gen := &llmmock.Generator{GenerateFn: func(context.Context, string) (string, error) {
		return "VERDICT: SAME\n", nil
	}}
	judge := &canonical.LLMAliasJudge{Generator: gen}

	same, err := judge.SameEntity(context.Background(), "PERSON", "Kade", "Rosalind Kade")
	if err != nil {
		t.Fatalf("SameEntity: %v", err)
	}
	if !same {
		t.Error("expected same=true")
	}
}

func TestLLMAliasJudge_ParsesDifferentVerdict(t *testing.T) {
	gen := &llmmock.Generator{GenerateFn: func(context.Context, string) (string, error) {
		return "VERDICT: DIFFERENT\n", nil
	}}
	judge := &canonical.LLMAliasJudge{Generator: gen}

	same, err := judge.SameEntity(context.Background(), "PERSON", "Talia Renn", "Wren Renn")
	if err != nil {
		t.Fatalf("SameEntity: %v", err)
	}
	if same {
		t.Error("expected same=false")
	}
}

func TestLLMAliasJudge_MalformedResponseTreatedAsDifferent(t *testing.T) {
	gen := &llmmock.Generator{GenerateFn: func(context.Context, string) (string, error) {
		return "not sure", nil
	}}
	judge := &canonical.LLMAliasJudge{Generator: gen}

	same, err := judge.SameEntity(context.Background(), "PERSON", "A", "B")
	if err != nil {
		t.Fatalf("SameEntity: %v", err)
	}
	if same {
		t.Error("a malformed response should never count as same -- merging is the harder-to-reverse action")
	}
}

func TestLLMAliasJudge_PromptIncludesTypeAndBothTexts(t *testing.T) {
	var gotPrompt string
	gen := &llmmock.Generator{GenerateFn: func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "VERDICT: SAME", nil
	}}
	judge := &canonical.LLMAliasJudge{Generator: gen}

	_, err := judge.SameEntity(context.Background(), "PERSON", "Kade", "Rosalind Kade")
	if err != nil {
		t.Fatalf("SameEntity: %v", err)
	}
	for _, want := range []string{"PERSON", "Kade", "Rosalind Kade"} {
		if !strings.Contains(gotPrompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, gotPrompt)
		}
	}
}

func TestLLMAliasJudge_PropagatesGenerateError(t *testing.T) {
	gen := &llmmock.Generator{GenerateFn: func(context.Context, string) (string, error) {
		return "", errors.New("boom")
	}}
	judge := &canonical.LLMAliasJudge{Generator: gen}

	_, err := judge.SameEntity(context.Background(), "PERSON", "A", "B")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}
