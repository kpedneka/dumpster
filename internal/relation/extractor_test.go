package relation

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
)

func TestParseResponse_ParsesMultipleChunksAndPairs(t *testing.T) {
	resp := "CHUNK: 1\nPAIR: 1\nRELATION: founded\nCHUNK: 1\nPAIR: 2\nRELATION: NONE\nCHUNK: 2\nPAIR: 1\nRELATION: works at\n"
	got := parseResponse(resp)
	want := map[answerKey]string{
		{chunk: 1, pair: 1}: "founded",
		{chunk: 1, pair: 2}: NoneRelation,
		{chunk: 2, pair: 1}: "works at",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseResponse = %+v, want %+v", got, want)
	}
}

func TestParseResponse_NoneIsCaseInsensitiveAndNormalized(t *testing.T) {
	resp := "CHUNK: 1\nPAIR: 1\nRELATION: none\n"
	got := parseResponse(resp)
	if got[answerKey{1, 1}] != NoneRelation {
		t.Errorf("got %q, want %q", got[answerKey{1, 1}], NoneRelation)
	}
}

func TestParseResponse_RelationLineWithNoPrecedingChunkOrPair_Ignored(t *testing.T) {
	resp := "RELATION: founded\nCHUNK: 1\nPAIR: 1\nRELATION: works at\n"
	got := parseResponse(resp)
	want := map[answerKey]string{{chunk: 1, pair: 1}: "works at"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseResponse = %+v, want %+v", got, want)
	}
}

func TestParseResponse_MalformedChunkNumber_Skipped(t *testing.T) {
	resp := "CHUNK: not-a-number\nPAIR: 1\nRELATION: founded\n"
	got := parseResponse(resp)
	if len(got) != 0 {
		t.Errorf("parseResponse = %+v, want empty (malformed CHUNK line)", got)
	}
}

func pair(a, b string) EdgePair {
	return EdgePair{EntityAID: uuid.New(), TextA: a, EntityBID: uuid.New(), TextB: b}
}

func TestExtract_MapsAnswersBackToOriginalPairs(t *testing.T) {
	c1p1 := pair("Gerald Combs", "Wireshark")
	c1p2 := pair("Wireshark", "Npcap")
	chunks := []ChunkCandidates{
		{ChunkID: uuid.New(), Text: "Gerald Combs founded Wireshark, which bundles Npcap.", Pairs: []EdgePair{c1p1, c1p2}},
	}
	gen := llmmock.NewGenerator("CHUNK: 1\nPAIR: 1\nRELATION: founded\nCHUNK: 1\nPAIR: 2\nRELATION: NONE\n")
	e := NewExtractor(gen)

	got, err := e.Extract(context.Background(), chunks)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d updates, want 2", len(got))
	}
	byPair := make(map[uuid.UUID]Update, len(got))
	for _, u := range got {
		byPair[u.EntityAID] = u
	}
	if byPair[c1p1.EntityAID].RelationType != "founded" {
		t.Errorf("pair 1 RelationType = %q, want %q", byPair[c1p1.EntityAID].RelationType, "founded")
	}
	if byPair[c1p2.EntityAID].RelationType != NoneRelation {
		t.Errorf("pair 2 RelationType = %q, want %q", byPair[c1p2.EntityAID].RelationType, NoneRelation)
	}
}

func TestExtract_UnaddressedPairIsOmittedNotGuessed(t *testing.T) {
	chunks := []ChunkCandidates{
		{ChunkID: uuid.New(), Text: "text", Pairs: []EdgePair{pair("A", "B"), pair("C", "D")}},
	}
	// Only answers pair 1 -- pair 2 is left unaddressed, as if the
	// response were truncated.
	gen := llmmock.NewGenerator("CHUNK: 1\nPAIR: 1\nRELATION: related to\n")
	e := NewExtractor(gen)

	got, err := e.Extract(context.Background(), chunks)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d updates, want 1 (unaddressed pair left for a later run)", len(got))
	}
}

func TestExtract_NoCandidatePairsAtAll_ReturnsNilWithoutCallingLLM(t *testing.T) {
	gen := llmmock.NewErrorGenerator("should not be called")
	e := NewExtractor(gen)

	got, err := e.Extract(context.Background(), []ChunkCandidates{
		{ChunkID: uuid.New(), Text: "text", Pairs: nil},
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestExtract_CapsPairsPerChunk(t *testing.T) {
	var pairs []EdgePair
	for i := 0; i < maxPairsPerChunk+5; i++ {
		pairs = append(pairs, pair("A", "B"))
	}
	chunks := []ChunkCandidates{{ChunkID: uuid.New(), Text: "text", Pairs: pairs}}

	prompt := ""
	gen := &llmmock.Generator{GenerateFn: func(_ context.Context, p string) (string, error) {
		prompt = p
		return "", nil
	}}
	e := NewExtractor(gen)
	if _, err := e.Extract(context.Background(), chunks); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	// The prompt should only list maxPairsPerChunk pairs, not all of them --
	// check the last pair number that appears is exactly the cap.
	wantLastLine := "15. A -- B"
	if !containsLine(prompt, wantLastLine) {
		t.Errorf("prompt missing %q (expected exactly %d pairs listed)", wantLastLine, maxPairsPerChunk)
	}
	if containsLine(prompt, "16. A -- B") {
		t.Error("prompt lists a 16th pair, want capped at maxPairsPerChunk")
	}
}

func containsLine(text, line string) bool {
	for _, l := range splitLines(text) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, r := range s {
		if r == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}
