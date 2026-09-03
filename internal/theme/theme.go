// Package theme generates short, human-readable labels for a knowledge
// base's largest entity communities (see internal/community for detection
// itself). Where community detection answers "is there structure, and how
// much" via aggregate stats, this answers "what is this KB about, in plain
// language" for a curated handful of the largest clusters — neither is a
// superset of the other.
package theme

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNoResult is returned by Repository.GetResult when a KB has never had
// themes generated for it.
var ErrNoResult = errors.New("theme: no result")

// EntityRef is one canonical entity's display text and type, as needed to
// build a summarization prompt — not the full canonical entity, since
// nothing else about it matters here.
type EntityRef struct {
	Text string
	Type string
}

// CommunityMembers is one community's member canonical entities.
type CommunityMembers struct {
	CommunityID int
	Entities    []EntityRef
}

// Theme is one LLM-generated label/summary for a notable community.
type Theme struct {
	CommunityID int
	Label       string
	Summary     string
	EntityCount int
}

// Result is a KB's most recently generated theme set.
type Result struct {
	ComputedAt time.Time
	Themes     []Theme
}

// Repository is the persistence boundary for theme generation. Every
// method is tenant-scoped, following the same multi-tenancy contract as
// community.Repository.
type Repository interface {
	// CommunityMembers returns every community's member canonical entities
	// for kbID (only entities with a non-null community_id — i.e.
	// community detection has run at least once), grouped by community_id.
	// SelectTopCommunities picks which of these actually get summarized;
	// this returns all of them so selection stays a pure, easily testable
	// function rather than living in SQL.
	CommunityMembers(ctx context.Context, userID, kbID uuid.UUID) ([]CommunityMembers, error)

	// SaveResult replaces kbID's theme set with themes, fully overwriting
	// whatever was there before — community ids (and therefore which
	// clusters these themes describe) have no meaning across separate
	// community-detection runs, same rationale as
	// community.Repository.SaveResult.
	SaveResult(ctx context.Context, userID, kbID uuid.UUID, themes []Theme, computedAt time.Time) error

	// GetResult returns kbID's most recently generated theme set. Returns
	// ErrNoResult if themes have never been generated for this KB.
	GetResult(ctx context.Context, userID, kbID uuid.UUID) (*Result, error)
}
