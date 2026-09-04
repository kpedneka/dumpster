package theme

import (
	"context"
	"fmt"
	"strings"
	"testing"

	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
)

func TestSummarize_ParsesLabelAndSummary(t *testing.T) {
	gen := llmmock.NewGenerator("COMMUNITY: 3\nLABEL: Distributed Systems\nSUMMARY: These entities relate to building resilient distributed software.")
	s := NewSummarizer(gen)

	candidates := []CommunityMembers{
		{CommunityID: 3, Entities: []EntityRef{{Text: "Kubernetes", Type: "concept"}, {Text: "etcd", Type: "concept"}}},
	}
	got, _, err := s.Summarize(context.Background(), candidates)
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

func TestSummarize_MalformedLabelWithinAValidBlock_FallsBackToUntitled(t *testing.T) {
	gen := llmmock.NewGenerator("COMMUNITY: 0\nSUMMARY: something connects these, unclear what.")
	s := NewSummarizer(gen)

	got, _, err := s.Summarize(context.Background(), []CommunityMembers{
		{CommunityID: 0, Entities: []EntityRef{{Text: "X", Type: "concept"}}},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d themes, want 1", len(got))
	}
	if got[0].Label != fallbackLabel {
		t.Errorf("Label = %q, want fallback %q", got[0].Label, fallbackLabel)
	}
}

func TestSummarize_NoCandidateStandsOut_ReturnsNoThemesWithoutError(t *testing.T) {
	gen := llmmock.NewGenerator(noneMarker)
	s := NewSummarizer(gen)

	got, note, err := s.Summarize(context.Background(), []CommunityMembers{
		{CommunityID: 0, Entities: []EntityRef{{Text: "X", Type: "concept"}}},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d themes, want 0", len(got))
	}
	if note != "" {
		t.Errorf("note = %q, want empty when no themes were returned", note)
	}
}

func TestSummarize_UnparseableResponse_ReturnsNoThemesWithoutError(t *testing.T) {
	gen := llmmock.NewGenerator("I'm not sure what these have in common.")
	s := NewSummarizer(gen)

	got, _, err := s.Summarize(context.Background(), []CommunityMembers{
		{CommunityID: 0, Entities: []EntityRef{{Text: "X", Type: "concept"}}},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d themes, want 0 (a response with no parseable COMMUNITY block shouldn't fabricate one)", len(got))
	}
}

func TestSummarize_NoCandidates_ReturnsWithoutCallingTheLLM(t *testing.T) {
	called := false
	gen := &llmmock.Generator{
		GenerateFn: func(_ context.Context, _ string) (string, error) {
			called = true
			return "", nil
		},
	}
	s := NewSummarizer(gen)

	got, _, err := s.Summarize(context.Background(), nil)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d themes, want 0", len(got))
	}
	if called {
		t.Error("expected the LLM not to be called for an empty candidate set")
	}
}

func TestSummarize_GeneratorError_FailsTheCall(t *testing.T) {
	gen := llmmock.NewErrorGenerator("rate limited")
	s := NewSummarizer(gen)

	_, _, err := s.Summarize(context.Background(), []CommunityMembers{
		{CommunityID: 0, Entities: []EntityRef{{Text: "X", Type: "concept"}}},
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestSummarize_AllCandidatesInOnePrompt(t *testing.T) {
	var capturedPrompt string
	gen := &llmmock.Generator{
		GenerateFn: func(_ context.Context, prompt string) (string, error) {
			capturedPrompt = prompt
			return noneMarker, nil
		},
	}
	s := NewSummarizer(gen)

	candidates := []CommunityMembers{
		{CommunityID: 5, Entities: []EntityRef{{Text: "A", Type: "concept"}}},
		{CommunityID: 2, Entities: []EntityRef{{Text: "B", Type: "concept"}}},
	}
	if _, _, err := s.Summarize(context.Background(), candidates); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if !strings.Contains(capturedPrompt, "Community 5:") || !strings.Contains(capturedPrompt, "Community 2:") {
		t.Errorf("expected a single prompt containing both candidates, got: %q", capturedPrompt)
	}
}

func TestSummarize_CapsEntitiesSentToThePrompt(t *testing.T) {
	var capturedPrompt string
	gen := &llmmock.Generator{
		GenerateFn: func(_ context.Context, prompt string) (string, error) {
			capturedPrompt = prompt
			return noneMarker, nil
		},
	}
	s := NewSummarizer(gen)

	entities := make([]EntityRef, maxEntitiesPerPrompt+10)
	for i := range entities {
		entities[i] = EntityRef{Text: fmt.Sprintf("entity-%d", i), Type: "concept"}
	}
	_, _, err := s.Summarize(context.Background(), []CommunityMembers{{CommunityID: 0, Entities: entities}})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	got := strings.Count(capturedPrompt, "- entity-")
	if got != maxEntitiesPerPrompt {
		t.Errorf("prompt included %d entities, want %d (capped)", got, maxEntitiesPerPrompt)
	}
}

func TestSummarize_MultipleThemes_PreservesModelOrder(t *testing.T) {
	gen := llmmock.NewGenerator(strings.Join([]string{
		"COMMUNITY: 5",
		"LABEL: theme-1",
		"SUMMARY: s-1",
		"COMMUNITY: 2",
		"LABEL: theme-2",
		"SUMMARY: s-2",
	}, "\n"))
	s := NewSummarizer(gen)

	candidates := []CommunityMembers{
		{CommunityID: 5, Entities: []EntityRef{{Text: "A", Type: "concept"}}},
		{CommunityID: 2, Entities: []EntityRef{{Text: "B", Type: "concept"}}},
	}
	got, _, err := s.Summarize(context.Background(), candidates)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != 2 || got[0].CommunityID != 5 || got[1].CommunityID != 2 {
		t.Errorf("got %+v, want CommunityID order [5, 2] preserved from the model's response", got)
	}
	if got[0].Label != "theme-1" || got[1].Label != "theme-2" {
		t.Errorf("got labels %q, %q, want theme-1, theme-2", got[0].Label, got[1].Label)
	}
}

func TestSummarize_MoreThanMaxSignificantThemes_TruncatedToTheCap(t *testing.T) {
	var blocks []string
	candidates := make([]CommunityMembers, maxSignificantThemes+2)
	for i := range candidates {
		candidates[i] = CommunityMembers{CommunityID: i, Entities: []EntityRef{{Text: "X", Type: "concept"}}}
		blocks = append(blocks, fmt.Sprintf("COMMUNITY: %d\nLABEL: theme-%d\nSUMMARY: s-%d", i, i, i))
	}
	gen := llmmock.NewGenerator(strings.Join(blocks, "\n"))
	s := NewSummarizer(gen)

	got, _, err := s.Summarize(context.Background(), candidates)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != maxSignificantThemes {
		t.Fatalf("got %d themes, want the cap of %d even though the model returned more", len(got), maxSignificantThemes)
	}
}

func TestSummarize_HallucinatedCommunityID_Dropped(t *testing.T) {
	gen := llmmock.NewGenerator("COMMUNITY: 999\nLABEL: made up\nSUMMARY: not a real candidate")
	s := NewSummarizer(gen)

	got, _, err := s.Summarize(context.Background(), []CommunityMembers{
		{CommunityID: 0, Entities: []EntityRef{{Text: "X", Type: "concept"}}},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d themes, want 0 (community id 999 wasn't a real candidate)", len(got))
	}
}

func TestSummarize_ParsesNoteAfterThemes(t *testing.T) {
	gen := llmmock.NewGenerator(strings.Join([]string{
		"COMMUNITY: 0",
		"LABEL: theme-1",
		"SUMMARY: s-1",
		"NOTE: these entities are mentioned far more often than anything else reviewed",
	}, "\n"))
	s := NewSummarizer(gen)

	got, note, err := s.Summarize(context.Background(), []CommunityMembers{
		{CommunityID: 0, Entities: []EntityRef{{Text: "X", Type: "concept"}}},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d themes, want 1", len(got))
	}
	want := "these entities are mentioned far more often than anything else reviewed"
	if note != want {
		t.Errorf("note = %q, want %q", note, want)
	}
}

func TestSummarize_NoteAbsentFromResponse_IsEmptyNotAnError(t *testing.T) {
	gen := llmmock.NewGenerator("COMMUNITY: 0\nLABEL: theme-1\nSUMMARY: s-1")
	s := NewSummarizer(gen)

	got, note, err := s.Summarize(context.Background(), []CommunityMembers{
		{CommunityID: 0, Entities: []EntityRef{{Text: "X", Type: "concept"}}},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d themes, want 1", len(got))
	}
	if note != "" {
		t.Errorf("note = %q, want empty when the model didn't supply one", note)
	}
}

func TestSummarize_NoteIgnoredWhenNoThemesStandOut(t *testing.T) {
	// A model that (incorrectly) attaches a NOTE to a NONE response
	// shouldn't leak a note into the result -- moreThemesNote's caller
	// relies on note being empty whenever themes is empty.
	gen := llmmock.NewGenerator(noneMarker + "\nNOTE: nothing stood out here")
	s := NewSummarizer(gen)

	got, note, err := s.Summarize(context.Background(), []CommunityMembers{
		{CommunityID: 0, Entities: []EntityRef{{Text: "X", Type: "concept"}}},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d themes, want 0", len(got))
	}
	if note != "" {
		t.Errorf("note = %q, want empty when no themes were returned", note)
	}
}
