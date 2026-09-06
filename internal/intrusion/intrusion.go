// Package intrusion implements the word/topic intrusion test (Chang et al.,
// "Reading Tea Leaves: How Humans Interpret Topic Models," 2009) as a
// quantitative coherence metric for a knowledge base's detected
// communities.
//
// For each tested community, a handful of its real top members are shown
// alongside one entity from a different community -- the "intruder" --
// and a judge (here, an LLM standing in for a human rater) is asked to
// spot which one doesn't belong. The fraction of communities where the
// judge succeeds is a real, repeatable number: it lets a pipeline change
// (chunking strategy, PMI edge weighting, entity filtering, relation
// extraction) be compared before and after by an actual score, instead of
// by re-reading a handful of examples and guessing whether it looks
// better.
package intrusion

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/theme"
)

// ErrNoResult is returned by Repository.GetResult when a KB has never had
// an intrusion test run for it.
var ErrNoResult = errors.New("intrusion: no result")

// CommunityResult is one tested community's outcome.
type CommunityResult struct {
	CommunityID int
	// Members is the real (non-intruder) entities shown for this
	// community, excluding IntruderText.
	Members []string
	// IntruderText is the entity actually injected from a different
	// community -- the correct answer the judge was asked to find.
	IntruderText string
	// JudgeAnswer is the judge's raw response for this set, kept even when
	// wrong or unparseable so a human reviewing the result can see what the
	// judge actually said, not just whether it was scored correct.
	JudgeAnswer string
	Correct     bool
}

// Result is one KB's most recent intrusion test run.
type Result struct {
	ComputedAt time.Time
	// Score is Correct/TestedCount in [0,1]. Zero (with TestedCount also
	// zero) when fewer than two communities in the KB were large enough to
	// test -- not a failure, just not enough structure yet to measure.
	Score       float64
	TestedCount int
	Communities []CommunityResult
}

// MemberSource is the data dependency for building intrusion tests: every
// community and its member entities. This is the exact same query
// theme.Repository.CommunityMembers already exposes -- reused via this
// narrower interface rather than duplicated, since theme.Store satisfies it
// structurally with no extra wiring.
type MemberSource interface {
	CommunityMembers(ctx context.Context, userID, kbID uuid.UUID) ([]theme.CommunityMembers, error)
}

// Repository is the persistence boundary for intrusion test results. Every
// method is tenant-scoped, following the same multi-tenancy contract as
// community.Repository and theme.Repository.
type Repository interface {
	// SaveResult replaces kbID's intrusion test result, fully overwriting
	// whatever was there before -- community ids have no meaning across
	// separate community-detection runs, same rationale as
	// community.Repository.SaveResult and theme.Repository.SaveResult.
	SaveResult(ctx context.Context, userID, kbID uuid.UUID, result Result) error

	// GetResult returns kbID's most recently run intrusion test. Returns
	// ErrNoResult if one has never been run.
	GetResult(ctx context.Context, userID, kbID uuid.UUID) (*Result, error)
}
