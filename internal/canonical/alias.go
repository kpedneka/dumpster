package canonical

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AliasJudge decides whether two entity mentions with different text refer
// to the same real-world entity, given their shared entity type. Judges
// from the two texts and type alone, not surrounding chunk context --
// FuzzyCandidates' token-subset matching already only proposes candidates
// with a real textual relationship (one's words are a subset of the
// other's), so this is a sanity check on a name-shaped signal, not
// open-ended disambiguation. A future version could add chunk context if
// empirical false positives show it's needed.
type AliasJudge interface {
	SameEntity(ctx context.Context, entityType, textA, textB string) (bool, error)
}

// ResolveAliases runs after ResolveNew's exact-match resolution: for each
// distinct canonical entity id in resolved, looks for fuzzy (token-subset)
// candidates via FuzzyCandidates and asks judge whether they're actually
// the same real-world entity, merging any confirmed pair.
//
// This is the fix for exactly the gap exact-match canonicalization
// structurally cannot close: two mentions of the same real entity with
// different text ("Kade" vs "Rosalind Kade") never collide on the unique
// (kb_id, user_id, normalized_text, entity_type) index Canonicalize
// upserts against, so without this step they stay two separate identities
// forever, regardless of how obviously related the text is.
func ResolveAliases(ctx context.Context, repo Repository, userID, kbID uuid.UUID, resolved map[uuid.UUID]uuid.UUID, judge AliasJudge) error {
	seen := make(map[uuid.UUID]bool, len(resolved))
	var ids []uuid.UUID
	for _, canonicalID := range resolved {
		if !seen[canonicalID] {
			seen[canonicalID] = true
			ids = append(ids, canonicalID)
		}
	}
	return resolveAliasesFor(ctx, repo, userID, kbID, ids, judge)
}

// ResolveAllAliases checks every existing canonical entity in kbID for
// fuzzy aliases, merging any confirmed pair -- a KB-wide sweep rather than
// scoped to one document's newly-resolved mentions. Intended as a
// manually-triggered, LLM-cost-bounded pass (the same shape as
// cmd/crosslink), both for backfilling a KB canonicalized before this
// mechanism existed and as the normal way to run alias resolution at all,
// since it isn't wired into the automatic per-document worker pipeline --
// matching this codebase's existing pattern for LLM-cost-incurring
// enrichment (see internal/relation, internal/crosslink): opt-in and
// separately triggered, not silently added to every future ingestion's
// cost. Returns the number of merges performed.
func ResolveAllAliases(ctx context.Context, repo Repository, userID, kbID uuid.UUID, judge AliasJudge) (int, error) {
	all, err := repo.ListByKB(ctx, userID, kbID)
	if err != nil {
		return 0, fmt.Errorf("canonical: resolve all aliases: list by kb: %w", err)
	}
	ids := make([]uuid.UUID, len(all))
	for i, ce := range all {
		ids[i] = ce.ID
	}
	before := len(ids)
	if err := resolveAliasesFor(ctx, repo, userID, kbID, ids, judge); err != nil {
		return 0, err
	}
	after, err := repo.ListByKB(ctx, userID, kbID)
	if err != nil {
		return 0, fmt.Errorf("canonical: resolve all aliases: list by kb (after): %w", err)
	}
	return before - len(after), nil
}

func resolveAliasesFor(ctx context.Context, repo Repository, userID, kbID uuid.UUID, ids []uuid.UUID, judge AliasJudge) error {
	for _, canonicalID := range ids {
		ce, err := repo.Get(ctx, userID, canonicalID)
		if err != nil {
			// Already merged away earlier in this same pass (two ids in
			// this batch turned out to be aliases of each other).
			continue
		}

		candidates, err := repo.FuzzyCandidates(ctx, userID, kbID, ce.NormalizedText, ce.Type, ce.ID)
		if err != nil {
			return fmt.Errorf("canonical: resolve aliases: fuzzy candidates for %s: %w", ce.ID, err)
		}

		for _, cand := range candidates {
			same, err := judge.SameEntity(ctx, string(ce.Type), ce.NormalizedText, cand.NormalizedText)
			if err != nil {
				return fmt.Errorf("canonical: resolve aliases: judge: %w", err)
			}
			if !same {
				continue
			}

			// from is merged away; to survives. Default assumes the
			// candidate survives; candidateSurvives==false flips it.
			from, to := ce.ID, cand.ID
			if !candidateSurvives(ce.MentionCount, ce.CreatedAt, ce.ID, cand.MentionCount, cand.CreatedAt, cand.ID) {
				from, to = cand.ID, ce.ID
			}
			if err := repo.MergeInto(ctx, userID, from, to); err != nil {
				return fmt.Errorf("canonical: resolve aliases: merge %s into %s: %w", from, to, err)
			}
			if from == ce.ID {
				// ce no longer exists once merged away -- nothing left to
				// check further candidates against.
				break
			}
			// ce survived (absorbed cand); keep checking its remaining
			// candidates in case more than one alias needs folding in.
		}
	}
	return nil
}

// candidateSurvives reports whether the candidate side of a confirmed
// merge should survive over ce -- true if the candidate has strictly more
// mentions, or on a mention-count tie, was created first. Preferring more
// mentions favors the more-established identity's display text and
// history; the CreatedAt tiebreak (rather than comparing UUIDs, which are
// random and carry no meaning) keeps the outcome deterministic and tied to
// something real -- which identity existed first -- instead of an
// arbitrary coin flip on a mention-count tie, the common case for two
// freshly-created aliases.
func candidateSurvives(ceMentions int, ceCreatedAt time.Time, ceID uuid.UUID, candMentions int, candCreatedAt time.Time, candID uuid.UUID) bool {
	if candMentions != ceMentions {
		return candMentions > ceMentions
	}
	if !candCreatedAt.Equal(ceCreatedAt) {
		return candCreatedAt.Before(ceCreatedAt)
	}
	return candID.String() < ceID.String()
}
