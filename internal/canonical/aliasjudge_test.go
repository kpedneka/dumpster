package canonical_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/canonical"
	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
)

func TestLLMAliasJudge_ParsesPerCandidateVerdicts(t *testing.T) {
	gen := &llmmock.Generator{GenerateFn: func(context.Context, string) (string, error) {
		return "CANDIDATE: 1\nVERDICT: SAME\nCANDIDATE: 2\nVERDICT: DIFFERENT\n", nil
	}}
	judge := &canonical.LLMAliasJudge{Generator: gen}

	same, err := judge.SameEntityBatch(context.Background(), "PERSON", "Kade", []string{"Rosalind Kade", "Wren Renn"})
	if err != nil {
		t.Fatalf("SameEntityBatch: %v", err)
	}
	if want := []bool{true, false}; len(same) != len(want) || same[0] != want[0] || same[1] != want[1] {
		t.Errorf("same = %v, want %v", same, want)
	}
}

// A candidate the model's response never addresses at all (not even a
// DIFFERENT verdict -- a truncated or malformed response) defaults to
// false, same as an explicit DIFFERENT -- merging is the harder-to-reverse
// action, so every failure mode favors not merging.
func TestLLMAliasJudge_UnaddressedCandidateTreatedAsDifferent(t *testing.T) {
	gen := &llmmock.Generator{GenerateFn: func(context.Context, string) (string, error) {
		return "CANDIDATE: 1\nVERDICT: SAME\n", nil // candidate 2 never answered
	}}
	judge := &canonical.LLMAliasJudge{Generator: gen}

	same, err := judge.SameEntityBatch(context.Background(), "PERSON", "Kade", []string{"Rosalind Kade", "Someone Else"})
	if err != nil {
		t.Fatalf("SameEntityBatch: %v", err)
	}
	if !same[0] {
		t.Error("candidate 1: expected same=true")
	}
	if same[1] {
		t.Error("candidate 2 (unaddressed): expected same=false")
	}
}

func TestLLMAliasJudge_MalformedResponseTreatedAsAllDifferent(t *testing.T) {
	gen := &llmmock.Generator{GenerateFn: func(context.Context, string) (string, error) {
		return "not sure", nil
	}}
	judge := &canonical.LLMAliasJudge{Generator: gen}

	same, err := judge.SameEntityBatch(context.Background(), "PERSON", "A", []string{"B", "C"})
	if err != nil {
		t.Fatalf("SameEntityBatch: %v", err)
	}
	for i, s := range same {
		if s {
			t.Errorf("candidate %d: a malformed response should never count as same", i)
		}
	}
}

func TestLLMAliasJudge_EmptyCandidates_NoGenerateCall(t *testing.T) {
	called := false
	gen := &llmmock.Generator{GenerateFn: func(context.Context, string) (string, error) {
		called = true
		return "VERDICT: SAME", nil
	}}
	judge := &canonical.LLMAliasJudge{Generator: gen}

	same, err := judge.SameEntityBatch(context.Background(), "PERSON", "A", nil)
	if err != nil {
		t.Fatalf("SameEntityBatch: %v", err)
	}
	if len(same) != 0 {
		t.Errorf("same = %v, want empty", same)
	}
	if called {
		t.Error("expected no Generate call for an empty candidate list -- nothing to ask")
	}
}

// The whole point of batching: N candidates against one reference name must
// cost exactly one Generate call, not N.
func TestLLMAliasJudge_ManyCandidatesInOneCall(t *testing.T) {
	calls := 0
	gen := &llmmock.Generator{GenerateFn: func(_ context.Context, prompt string) (string, error) {
		calls++
		var sb strings.Builder
		for i := 1; i <= 5; i++ {
			fmt.Fprintf(&sb, "CANDIDATE: %d\nVERDICT: DIFFERENT\n", i)
		}
		return sb.String(), nil
	}}
	judge := &canonical.LLMAliasJudge{Generator: gen}

	same, err := judge.SameEntityBatch(context.Background(), "PERSON", "Ref", []string{"a", "b", "c", "d", "e"})
	if err != nil {
		t.Fatalf("SameEntityBatch: %v", err)
	}
	if len(same) != 5 {
		t.Errorf("len(same) = %d, want 5", len(same))
	}
	if calls != 1 {
		t.Errorf("Generate called %d times, want exactly 1", calls)
	}
}

func TestLLMAliasJudge_PromptIncludesTypeReferenceAndAllCandidates(t *testing.T) {
	var gotPrompt string
	gen := &llmmock.Generator{GenerateFn: func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "CANDIDATE: 1\nVERDICT: SAME\n", nil
	}}
	judge := &canonical.LLMAliasJudge{Generator: gen}

	_, err := judge.SameEntityBatch(context.Background(), "PERSON", "Kade", []string{"Rosalind Kade"})
	if err != nil {
		t.Fatalf("SameEntityBatch: %v", err)
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

	_, err := judge.SameEntityBatch(context.Background(), "PERSON", "A", []string{"B"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}
