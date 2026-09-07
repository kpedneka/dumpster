package crosslink_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/crosslink"
	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
)

func candidate(aText, aChunkText, bText, bChunkText string) crosslink.Candidate {
	return crosslink.Candidate{
		EntityAID: uuid.New(), EntityAText: aText, ChunkAID: uuid.New(), ChunkAText: aChunkText,
		EntityBID: uuid.New(), EntityBText: bText, ChunkBID: uuid.New(), ChunkBText: bChunkText,
	}
}

func TestExtract_MapsAnswersBackToOriginalCandidates(t *testing.T) {
	c1 := candidate("Steadfast Compass Mount", "The compass mount used a weighted-ring approach.",
		"Harbor Beacon Leveler", "The Harbor Beacon Leveler applied the same weighted-ring approach.")
	c2 := candidate("Quoribal", "A game played by dockworkers.",
		"Hollow Verge", "A bronze sculpture completed after years of study.")

	gen := llmmock.NewGenerator("CANDIDATE: 1\nRELATION: design influenced\nCANDIDATE: 2\nRELATION: NONE\n")
	e := crosslink.NewExtractor(gen)

	updates, err := e.Extract(context.Background(), []crosslink.Candidate{c1, c2})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2", len(updates))
	}

	byPair := map[[2]uuid.UUID]crosslink.Update{}
	for _, u := range updates {
		byPair[[2]uuid.UUID{u.EntityAID, u.EntityBID}] = u
	}

	u1, ok := byPair[[2]uuid.UUID{c1.EntityAID, c1.EntityBID}]
	if !ok || u1.RelationType != "design influenced" {
		t.Errorf("candidate 1 update = %+v, ok=%v, want RelationType=%q", u1, ok, "design influenced")
	}
	u2, ok := byPair[[2]uuid.UUID{c2.EntityAID, c2.EntityBID}]
	if !ok || u2.RelationType != crosslink.NoneRelation {
		t.Errorf("candidate 2 update = %+v, ok=%v, want RelationType=%q", u2, ok, crosslink.NoneRelation)
	}
}

func TestExtract_NoCandidates_ReturnsNilWithoutCallingGenerator(t *testing.T) {
	called := false
	gen := &llmmock.Generator{GenerateFn: func(context.Context, string) (string, error) {
		called = true
		return "", nil
	}}
	e := crosslink.NewExtractor(gen)

	updates, err := e.Extract(context.Background(), nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if updates != nil {
		t.Errorf("updates = %v, want nil", updates)
	}
	if called {
		t.Error("Extract should not call the generator for an empty candidate list")
	}
}

func TestExtract_UnansweredCandidateIsOmitted(t *testing.T) {
	c1 := candidate("A", "chunk a", "B", "chunk b")
	gen := llmmock.NewGenerator("this response doesn't follow the format at all")
	e := crosslink.NewExtractor(gen)

	updates, err := e.Extract(context.Background(), []crosslink.Candidate{c1})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(updates) != 0 {
		t.Errorf("got %d updates, want 0 for an unparseable response", len(updates))
	}
}

func TestExtract_IncludesBridgeChunkInPromptWhenSet(t *testing.T) {
	c := candidate("Elena Voss", "chunk a text", "Harbor Beacon Leveler", "chunk b text")
	c.BridgeChunkText = "the connecting fact goes here"

	var gotPrompt string
	gen := &llmmock.Generator{GenerateFn: func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "CANDIDATE: 1\nRELATION: NONE\n", nil
	}}
	e := crosslink.NewExtractor(gen)

	_, err := e.Extract(context.Background(), []crosslink.Candidate{c})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(gotPrompt, "the connecting fact goes here") {
		t.Errorf("prompt missing bridge chunk text:\n%s", gotPrompt)
	}
}

func TestExtract_OmitsBridgeSectionWhenNotSet(t *testing.T) {
	c := candidate("A", "chunk a text", "B", "chunk b text")

	var gotPrompt string
	gen := &llmmock.Generator{GenerateFn: func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "CANDIDATE: 1\nRELATION: NONE\n", nil
	}}
	e := crosslink.NewExtractor(gen)

	_, err := e.Extract(context.Background(), []crosslink.Candidate{c})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if strings.Contains(gotPrompt, "Connecting passage") {
		t.Errorf("prompt should not mention a connecting passage when BridgeChunkText is empty:\n%s", gotPrompt)
	}
}

func TestExtract_PropagatesGenerateError(t *testing.T) {
	c1 := candidate("A", "chunk a", "B", "chunk b")
	gen := &llmmock.Generator{GenerateFn: func(context.Context, string) (string, error) {
		return "", context.DeadlineExceeded
	}}
	e := crosslink.NewExtractor(gen)

	_, err := e.Extract(context.Background(), []crosslink.Candidate{c1})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}
