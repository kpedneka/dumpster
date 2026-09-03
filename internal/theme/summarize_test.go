package theme

import (
	"context"
	"fmt"
	"strings"
	"testing"

	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
)

func TestSummarize_ParsesLabelAndSummary(t *testing.T) {
	gen := llmmock.NewGenerator("LABEL: Distributed Systems\nSUMMARY: These entities relate to building resilient distributed software.")
	s := NewSummarizer(gen)

	top := []CommunityMembers{
		{CommunityID: 3, Entities: []EntityRef{{Text: "Kubernetes", Type: "concept"}, {Text: "etcd", Type: "concept"}}},
	}
	got, err := s.Summarize(context.Background(), top)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d themes, want 1", len(got))
	}
	if got[0].CommunityID != 3 {
		t.Errorf("CommunityID = %d, want 3", got[0].CommunityID)
	}
	if got[0].Label != "Distributed Systems" {
		t.Errorf("Label = %q, want %q", got[0].Label, "Distributed Systems")
	}
	if got[0].Summary != "These entities relate to building resilient distributed software." {
		t.Errorf("Summary = %q", got[0].Summary)
	}
	if got[0].EntityCount != 2 {
		t.Errorf("EntityCount = %d, want 2", got[0].EntityCount)
	}
}

func TestSummarize_MalformedResponse_FallsBackToUntitled(t *testing.T) {
	gen := llmmock.NewGenerator("I'm not sure what these have in common.")
	s := NewSummarizer(gen)

	got, err := s.Summarize(context.Background(), []CommunityMembers{
		{CommunityID: 0, Entities: []EntityRef{{Text: "X", Type: "concept"}}},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got[0].Label != fallbackLabel {
		t.Errorf("Label = %q, want fallback %q", got[0].Label, fallbackLabel)
	}
}

func TestSummarize_GeneratorError_FailsTheWholeBatch(t *testing.T) {
	gen := llmmock.NewErrorGenerator("rate limited")
	s := NewSummarizer(gen)

	_, err := s.Summarize(context.Background(), []CommunityMembers{
		{CommunityID: 0, Entities: []EntityRef{{Text: "X", Type: "concept"}}},
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestSummarize_CapsEntitiesSentToThePrompt(t *testing.T) {
	var capturedPrompt string
	gen := &llmmock.Generator{
		GenerateFn: func(_ context.Context, prompt string) (string, error) {
			capturedPrompt = prompt
			return "LABEL: x\nSUMMARY: y", nil
		},
	}
	s := NewSummarizer(gen)

	entities := make([]EntityRef, maxEntitiesPerPrompt+10)
	for i := range entities {
		entities[i] = EntityRef{Text: fmt.Sprintf("entity-%d", i), Type: "concept"}
	}
	_, err := s.Summarize(context.Background(), []CommunityMembers{{CommunityID: 0, Entities: entities}})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	got := strings.Count(capturedPrompt, "- entity-")
	if got != maxEntitiesPerPrompt {
		t.Errorf("prompt included %d entities, want %d (capped)", got, maxEntitiesPerPrompt)
	}
}

func TestSummarize_MultipleCommunities_PreservesOrder(t *testing.T) {
	calls := 0
	gen := &llmmock.Generator{
		GenerateFn: func(_ context.Context, _ string) (string, error) {
			calls++
			return fmt.Sprintf("LABEL: theme-%d\nSUMMARY: s-%d", calls, calls), nil
		},
	}
	s := NewSummarizer(gen)

	top := []CommunityMembers{
		{CommunityID: 5, Entities: []EntityRef{{Text: "A", Type: "concept"}}},
		{CommunityID: 2, Entities: []EntityRef{{Text: "B", Type: "concept"}}},
	}
	got, err := s.Summarize(context.Background(), top)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != 2 || got[0].CommunityID != 5 || got[1].CommunityID != 2 {
		t.Errorf("got %+v, want CommunityID order [5, 2] preserved", got)
	}
	if got[0].Label != "theme-1" || got[1].Label != "theme-2" {
		t.Errorf("got labels %q, %q, want theme-1, theme-2 in call order", got[0].Label, got[1].Label)
	}
}
