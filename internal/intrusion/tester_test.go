package intrusion

import (
	"context"
	"reflect"
	"testing"

	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/theme"
)

func members(n int) []theme.EntityRef {
	out := make([]theme.EntityRef, n)
	for i := range out {
		out[i] = theme.EntityRef{Text: string(rune('a' + i)), Type: "concept"}
	}
	return out
}

func TestBuildSets_ExcludesCommunitiesBelowMinRealMembers(t *testing.T) {
	input := []theme.CommunityMembers{
		{CommunityID: 1, Entities: members(minRealMembers - 1)}, // too small
		{CommunityID: 2, Entities: members(minRealMembers)},
		{CommunityID: 3, Entities: members(minRealMembers)},
	}

	got := buildSets(input)

	for _, s := range got {
		if s.communityID == 1 {
			t.Fatalf("sets = %+v, want community 1 excluded (below minRealMembers)", got)
		}
	}
	if len(got) != 2 {
		t.Errorf("len(sets) = %d, want 2", len(got))
	}
}

func TestBuildSets_FewerThanTwoQualifyingCommunities_ReturnsNil(t *testing.T) {
	input := []theme.CommunityMembers{
		{CommunityID: 1, Entities: members(minRealMembers)},
	}
	if got := buildSets(input); got != nil {
		t.Errorf("buildSets = %+v, want nil (only one qualifying community, nothing to draw an intruder from)", got)
	}
}

func TestBuildSets_CapsMembersPerSet(t *testing.T) {
	input := []theme.CommunityMembers{
		{CommunityID: 1, Entities: members(realMembersPerSet + 5)},
		{CommunityID: 2, Entities: members(minRealMembers)},
	}
	got := buildSets(input)
	for _, s := range got {
		if s.communityID == 1 && len(s.memberTexts) != realMembersPerSet {
			t.Errorf("community 1 memberTexts = %d, want capped at %d", len(s.memberTexts), realMembersPerSet)
		}
	}
}

func TestBuildSets_CapsCommunitiesPerRun(t *testing.T) {
	var input []theme.CommunityMembers
	for i := 0; i < maxCommunitiesPerRun+10; i++ {
		input = append(input, theme.CommunityMembers{CommunityID: i, Entities: members(minRealMembers)})
	}
	got := buildSets(input)
	if len(got) != maxCommunitiesPerRun {
		t.Errorf("len(sets) = %d, want capped at %d", len(got), maxCommunitiesPerRun)
	}
}

func TestBuildSets_IsDeterministic(t *testing.T) {
	input := []theme.CommunityMembers{
		{CommunityID: 1, Entities: members(minRealMembers)},
		{CommunityID: 2, Entities: members(minRealMembers)},
		{CommunityID: 3, Entities: members(minRealMembers)},
	}

	first := buildSets(input)
	second := buildSets(input)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("buildSets is not deterministic:\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

func TestBuildSets_IntruderComesFromADifferentCommunity(t *testing.T) {
	input := []theme.CommunityMembers{
		{CommunityID: 1, Entities: []theme.EntityRef{{Text: "loop"}, {Text: "main"}, {Text: "i"}}},
		{CommunityID: 2, Entities: []theme.EntityRef{{Text: "Hogwarts"}, {Text: "wand"}, {Text: "spell"}}},
	}

	got := buildSets(input)

	for _, s := range got {
		for _, m := range s.memberTexts {
			if m == s.intruderText {
				t.Fatalf("set %+v: intruder %q also appears as a real member", s, s.intruderText)
			}
		}
	}
}

func TestBuildSets_ItemsIncludeAllMembersAndTheIntruderExactlyOnce(t *testing.T) {
	input := []theme.CommunityMembers{
		{CommunityID: 1, Entities: []theme.EntityRef{{Text: "loop"}, {Text: "main"}, {Text: "i"}}},
		{CommunityID: 2, Entities: []theme.EntityRef{{Text: "Hogwarts"}, {Text: "wand"}, {Text: "spell"}}},
	}

	got := buildSets(input)

	for _, s := range got {
		if len(s.items) != len(s.memberTexts)+1 {
			t.Fatalf("set %+v: items length = %d, want %d (members + intruder)", s, len(s.items), len(s.memberTexts)+1)
		}
		var sawIntruder int
		for _, item := range s.items {
			if item == s.intruderText {
				sawIntruder++
			}
		}
		if sawIntruder != 1 {
			t.Errorf("set %+v: intruder appears %d times in items, want exactly 1", s, sawIntruder)
		}
	}
}

func TestParseResponse_ParsesMultipleSets(t *testing.T) {
	resp := "SET: 1\nINTRUDER: Hogwarts\n\nSET: 2\nINTRUDER: loop\n"
	got := parseResponse(resp)
	want := map[int]string{1: "Hogwarts", 2: "loop"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseResponse = %+v, want %+v", got, want)
	}
}

func TestParseResponse_IgnoresIntruderLineWithNoPrecedingSet(t *testing.T) {
	resp := "INTRUDER: orphan\nSET: 1\nINTRUDER: real\n"
	got := parseResponse(resp)
	want := map[int]string{1: "real"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseResponse = %+v, want %+v", got, want)
	}
}

func TestParseResponse_MalformedSetNumberIsSkipped(t *testing.T) {
	resp := "SET: not-a-number\nINTRUDER: should-not-appear\n"
	got := parseResponse(resp)
	if len(got) != 0 {
		t.Errorf("parseResponse = %+v, want empty (malformed SET line)", got)
	}
}

func TestRun_ScoresCorrectAndIncorrectAnswers(t *testing.T) {
	input := []theme.CommunityMembers{
		{CommunityID: 1, Entities: []theme.EntityRef{{Text: "loop"}, {Text: "main"}, {Text: "i"}}},
		{CommunityID: 2, Entities: []theme.EntityRef{{Text: "Hogwarts"}, {Text: "wand"}, {Text: "spell"}}},
	}
	sets := buildSets(input)
	if len(sets) != 2 {
		t.Fatalf("test setup: got %d sets, want 2", len(sets))
	}

	// Answer set 1 correctly (its real intruder), set 2 with a wrong guess.
	resp := "SET: 1\nINTRUDER: " + sets[0].intruderText + "\n\nSET: 2\nINTRUDER: definitely-wrong\n"
	tester := NewTester(llmmock.NewGenerator(resp))

	got, err := tester.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.TestedCount != 2 {
		t.Fatalf("TestedCount = %d, want 2", got.TestedCount)
	}
	if got.Score != 0.5 {
		t.Errorf("Score = %v, want 0.5 (1 of 2 correct)", got.Score)
	}
	var correctCount int
	for _, c := range got.Communities {
		if c.Correct {
			correctCount++
		}
	}
	if correctCount != 1 {
		t.Errorf("correct communities = %d, want 1", correctCount)
	}
}

func TestRun_AnswerComparisonIsCaseAndWhitespaceInsensitive(t *testing.T) {
	input := []theme.CommunityMembers{
		{CommunityID: 1, Entities: []theme.EntityRef{{Text: "loop"}, {Text: "main"}, {Text: "i"}}},
		{CommunityID: 2, Entities: []theme.EntityRef{{Text: "Hogwarts"}, {Text: "wand"}, {Text: "spell"}}},
	}
	sets := buildSets(input)
	resp := "SET: 1\nINTRUDER:   " + sets[0].intruderText + "  \n\nSET: 2\nINTRUDER: " + sets[1].intruderText + "\n"
	tester := NewTester(llmmock.NewGenerator(resp))

	got, err := tester.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Score != 1.0 {
		t.Errorf("Score = %v, want 1.0 (both correct modulo whitespace)", got.Score)
	}
}

func TestRun_NotEnoughCommunities_ReturnsZeroTestedCountNoError(t *testing.T) {
	tester := NewTester(llmmock.NewGenerator("should not be called"))
	got, err := tester.Run(context.Background(), []theme.CommunityMembers{
		{CommunityID: 1, Entities: members(minRealMembers)},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.TestedCount != 0 || got.Score != 0 {
		t.Errorf("Result = %+v, want zero-value (not enough communities to test)", got)
	}
}

func TestRun_MissingAnswerForASet_ScoredIncorrectNotError(t *testing.T) {
	input := []theme.CommunityMembers{
		{CommunityID: 1, Entities: []theme.EntityRef{{Text: "loop"}, {Text: "main"}, {Text: "i"}}},
		{CommunityID: 2, Entities: []theme.EntityRef{{Text: "Hogwarts"}, {Text: "wand"}, {Text: "spell"}}},
	}
	// Judge never answers set 2 at all.
	tester := NewTester(llmmock.NewGenerator("SET: 1\nINTRUDER: whatever\n"))

	got, err := tester.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.TestedCount != 2 {
		t.Fatalf("TestedCount = %d, want 2", got.TestedCount)
	}
	if got.Communities[1].Correct {
		t.Errorf("set 2 (no answer given) scored correct, want incorrect")
	}
	if got.Communities[1].JudgeAnswer != "" {
		t.Errorf("set 2 JudgeAnswer = %q, want empty", got.Communities[1].JudgeAnswer)
	}
}
