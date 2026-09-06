package intrusion

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kunalpednekar/dumpster/internal/canonical"
	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/theme"
)

const (
	// minRealMembers is the fewest real (non-intruder) members a community
	// needs before it's worth testing at all -- with too few, spotting the
	// intruder is trivial regardless of whether the community is actually
	// coherent, which would inflate the score without measuring anything
	// real.
	minRealMembers = 3

	// realMembersPerSet caps how many of a community's members are shown
	// per set, keeping prompt size bounded regardless of community size --
	// same rationale as theme.maxEntitiesPerPrompt.
	realMembersPerSet = 4

	// maxCommunitiesPerRun bounds how many communities one run evaluates,
	// so cost and latency stay predictable regardless of how many
	// communities a KB has -- same rationale as theme.maxSelectedCommunities.
	maxCommunitiesPerRun = 30

	// testSeed is fixed, not time- or KB-derived. Re-running the test
	// against an *unchanged* community structure must produce the exact
	// same sample (which communities, which intruders, which shuffled
	// order) and therefore the exact same score -- a before/after
	// comparison needs to isolate an actual pipeline change, not sampling
	// noise from a different random draw each run.
	testSeed = 42
)

// Tester runs intrusion tests via an llm.Generator, reusing the same
// interface theme.Summarizer and internal/search.LLMAnswerer already build
// on.
type Tester struct {
	gen llm.Generator
}

// NewTester returns a Tester backed by gen.
func NewTester(gen llm.Generator) *Tester {
	return &Tester{gen: gen}
}

// Run builds and scores an intrusion test over members -- every community
// in a KB and its entities, as returned by MemberSource.CommunityMembers.
// Returns a zero-TestedCount Result (not an error) when fewer than two
// communities qualify: there's no meaningful test to run yet, not a
// failure.
func (t *Tester) Run(ctx context.Context, members []theme.CommunityMembers) (Result, error) {
	sets := buildSets(members)
	if len(sets) == 0 {
		return Result{ComputedAt: time.Now()}, nil
	}

	resp, err := t.gen.Generate(ctx, buildPrompt(sets))
	if err != nil {
		return Result{}, fmt.Errorf("intrusion: generate: %w", err)
	}
	answers := parseResponse(resp)

	communities := make([]CommunityResult, len(sets))
	var correct int
	for i, s := range sets {
		answer := answers[i+1] // sets are 1-indexed in the prompt
		isCorrect := answer != "" && canonical.Normalize(answer) == canonical.Normalize(s.intruderText)
		if isCorrect {
			correct++
		}
		communities[i] = CommunityResult{
			CommunityID:  s.communityID,
			Members:      s.memberTexts,
			IntruderText: s.intruderText,
			JudgeAnswer:  answer,
			Correct:      isCorrect,
		}
	}

	return Result{
		ComputedAt:  time.Now(),
		Score:       float64(correct) / float64(len(sets)),
		TestedCount: len(sets),
		Communities: communities,
	}, nil
}

// testSet is one community's fully-built intrusion test: memberTexts (the
// real entities) plus intruderText (the answer), and items -- the two
// combined and shuffled together, which is what actually gets shown to the
// judge so position never leaks which one is the intruder.
type testSet struct {
	communityID  int
	memberTexts  []string
	intruderText string
	items        []string
}

// buildSets selects which communities to test and builds each one's
// intrusion set, deterministically (see testSeed): communities are sorted
// by id before anything else so the result never depends on map/slice
// iteration order upstream, and rng is seeded fixed so the same input
// members always produces the same selection, intruder choice, and display
// order.
func buildSets(members []theme.CommunityMembers) []testSet {
	sorted := make([]theme.CommunityMembers, len(members))
	copy(sorted, members)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].CommunityID < sorted[j].CommunityID })

	var qualifying []theme.CommunityMembers
	for _, cm := range sorted {
		if len(cm.Entities) >= minRealMembers {
			qualifying = append(qualifying, cm)
		}
	}
	// Need at least one other qualifying community to draw an intruder
	// from -- a KB with only one community-sized cluster (or none) has
	// nothing to meaningfully test yet.
	if len(qualifying) < 2 {
		return nil
	}
	if len(qualifying) > maxCommunitiesPerRun {
		qualifying = qualifying[:maxCommunitiesPerRun]
	}

	rng := rand.New(rand.NewSource(testSeed))
	sets := make([]testSet, len(qualifying))
	for i, cm := range qualifying {
		realCount := realMembersPerSet
		if realCount > len(cm.Entities) {
			realCount = len(cm.Entities)
		}
		memberTexts := make([]string, realCount)
		for j := 0; j < realCount; j++ {
			memberTexts[j] = cm.Entities[j].Text
		}

		// A random offset in [1, len(qualifying)-1] guarantees the other
		// index differs from i regardless of rng's draw.
		offset := 1 + rng.Intn(len(qualifying)-1)
		other := qualifying[(i+offset)%len(qualifying)]
		intruderText := other.Entities[rng.Intn(len(other.Entities))].Text

		items := append(append([]string{}, memberTexts...), intruderText)
		rng.Shuffle(len(items), func(a, b int) { items[a], items[b] = items[b], items[a] })

		sets[i] = testSet{communityID: cm.CommunityID, memberTexts: memberTexts, intruderText: intruderText, items: items}
	}
	return sets
}

func buildPrompt(sets []testSet) string {
	var sb strings.Builder
	sb.WriteString("Each numbered set below lists several items found together in a knowledge base's entity graph. In every set, exactly one item was deliberately taken from a completely unrelated group and does not belong with the rest. Identify which one.\n\n")
	for i, s := range sets {
		fmt.Fprintf(&sb, "Set %d:\n", i+1)
		for _, item := range s.items {
			fmt.Fprintf(&sb, "- %s\n", item)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("For each set, respond with exactly this block (repeated once per set, nothing else in between):\nSET: <set number>\nINTRUDER: <the exact text of the item that doesn't belong>\n")
	return sb.String()
}

const (
	setPrefix      = "SET:"
	intruderPrefix = "INTRUDER:"
)

// parseResponse turns the judge's raw response into a set-number -> answer
// map. A set number with no parseable INTRUDER: line, or that never
// appears at all, is simply absent from the map -- Run treats a missing
// answer as incorrect rather than erroring the whole test, since a
// malformed response for one set shouldn't invalidate every other set's
// result.
func parseResponse(resp string) map[int]string {
	answers := make(map[int]string)
	curSet := 0
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, setPrefix); ok {
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				curSet = 0
				continue
			}
			curSet = n
			continue
		}
		if rest, ok := strings.CutPrefix(line, intruderPrefix); ok {
			if curSet != 0 {
				answers[curSet] = strings.TrimSpace(rest)
			}
			continue
		}
	}
	return answers
}
