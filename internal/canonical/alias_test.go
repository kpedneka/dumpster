package canonical_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/canonical"
	canonicalmem "github.com/kunalpednekar/dumpster/internal/canonical/memory"
	"github.com/kunalpednekar/dumpster/internal/entity"
)

type fakeJudge struct {
	sameByPair map[[2]string]bool
	err        error
}

func (f *fakeJudge) SameEntityBatch(_ context.Context, _ string, textA string, candidates []string) ([]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]bool, len(candidates))
	for i, textB := range candidates {
		if v, ok := f.sameByPair[[2]string{textA, textB}]; ok {
			out[i] = v
			continue
		}
		if v, ok := f.sameByPair[[2]string{textB, textA}]; ok {
			out[i] = v
		}
	}
	return out, nil
}

func TestResolveAliases_MergesConfirmedAlias(t *testing.T) {
	repo := canonicalmem.New()
	kbID, userID := uuid.New(), uuid.New()
	doc1, doc2 := uuid.New(), uuid.New()

	// "Rosalind Kade" resolves first, establishing the canonical entity.
	resolvedFull, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc1, "Rosalind Kade", "PERSON"),
	})
	if err != nil {
		t.Fatalf("Canonicalize (full): %v", err)
	}

	// "Kade" resolves later, as its own new canonical entity (no exact match).
	kadeMention := mention(kbID, userID, doc2, "Kade", "PERSON")
	resolvedShort, err := repo.Canonicalize(context.Background(), []*entity.Entity{kadeMention})
	if err != nil {
		t.Fatalf("Canonicalize (short): %v", err)
	}

	judge := &fakeJudge{sameByPair: map[[2]string]bool{{"kade", "rosalind kade"}: true}}

	err = canonical.ResolveAliases(context.Background(), repo, userID, kbID, resolvedShort, judge)
	if err != nil {
		t.Fatalf("ResolveAliases: %v", err)
	}

	fullID := firstValue(resolvedFull)
	shortID := firstValue(resolvedShort)

	if _, err := repo.Get(context.Background(), userID, shortID); !errors.Is(err, canonical.ErrNotFound) {
		t.Errorf("expected the short-form canonical entity to be deleted after merge, got err=%v", err)
	}
	merged, err := repo.Get(context.Background(), userID, fullID)
	if err != nil {
		t.Fatalf("Get survivor: %v", err)
	}
	if merged.MentionCount != 2 {
		t.Errorf("survivor MentionCount = %d, want 2 (1 original + 1 merged)", merged.MentionCount)
	}
}

func TestResolveAliases_DoesNotMergeWhenJudgeSaysNo(t *testing.T) {
	repo := canonicalmem.New()
	kbID, userID := uuid.New(), uuid.New()
	doc1, doc2 := uuid.New(), uuid.New()

	resolvedA, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc1, "Talia Renn", "PERSON"),
	})
	if err != nil {
		t.Fatalf("Canonicalize (A): %v", err)
	}
	resolvedB, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc2, "Wren Renn", "PERSON"),
	})
	if err != nil {
		t.Fatalf("Canonicalize (B): %v", err)
	}

	// Neither is a token subset of the other ("talia"/"renn" vs "wren"/"renn"),
	// so FuzzyCandidates shouldn't even propose this pair -- but assert the
	// outcome (no merge), not the mechanism, in case that changes.
	judge := &fakeJudge{sameByPair: map[[2]string]bool{}}

	if err := canonical.ResolveAliases(context.Background(), repo, userID, kbID, resolvedB, judge); err != nil {
		t.Fatalf("ResolveAliases: %v", err)
	}

	idA, idB := firstValue(resolvedA), firstValue(resolvedB)
	if _, err := repo.Get(context.Background(), userID, idA); err != nil {
		t.Errorf("Talia Renn's canonical entity should still exist: %v", err)
	}
	if _, err := repo.Get(context.Background(), userID, idB); err != nil {
		t.Errorf("Wren Renn's canonical entity should still exist (not merged): %v", err)
	}
}

func TestResolveAllAliases_SweepsWholeKBAndReportsMergeCount(t *testing.T) {
	repo := canonicalmem.New()
	kbID, userID := uuid.New(), uuid.New()
	doc1, doc2, doc3 := uuid.New(), uuid.New(), uuid.New()

	if _, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc1, "Rosalind Kade", "PERSON"),
	}); err != nil {
		t.Fatalf("Canonicalize (full): %v", err)
	}
	if _, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc2, "Kade", "PERSON"),
	}); err != nil {
		t.Fatalf("Canonicalize (short): %v", err)
	}
	// An unrelated entity that should be left alone.
	if _, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc3, "Hollow Verge", "WORK_OF_ART"),
	}); err != nil {
		t.Fatalf("Canonicalize (unrelated): %v", err)
	}

	judge := &fakeJudge{sameByPair: map[[2]string]bool{{"kade", "rosalind kade"}: true}}

	merged, err := canonical.ResolveAllAliases(context.Background(), repo, userID, kbID, judge)
	if err != nil {
		t.Fatalf("ResolveAllAliases: %v", err)
	}
	if merged != 1 {
		t.Errorf("merged = %d, want 1", merged)
	}

	remaining, err := repo.ListByKB(context.Background(), userID, kbID)
	if err != nil {
		t.Fatalf("ListByKB: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("got %d canonical entities remaining, want 2 (Rosalind Kade survivor + Hollow Verge untouched)", len(remaining))
	}
}

func TestResolveAliases_PropagatesJudgeError(t *testing.T) {
	repo := canonicalmem.New()
	kbID, userID := uuid.New(), uuid.New()
	doc1, doc2 := uuid.New(), uuid.New()

	if _, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc1, "Rosalind Kade", "PERSON"),
	}); err != nil {
		t.Fatalf("Canonicalize (full): %v", err)
	}
	resolvedShort, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc2, "Kade", "PERSON"),
	})
	if err != nil {
		t.Fatalf("Canonicalize (short): %v", err)
	}

	judge := &fakeJudge{err: errors.New("boom")}
	if err := canonical.ResolveAliases(context.Background(), repo, userID, kbID, resolvedShort, judge); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func firstValue(m map[uuid.UUID]uuid.UUID) uuid.UUID {
	for _, v := range m {
		return v
	}
	return uuid.Nil
}
