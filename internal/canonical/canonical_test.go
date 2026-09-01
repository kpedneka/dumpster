package canonical_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/canonical"
	canonicalmem "github.com/kunalpednekar/dumpster/internal/canonical/memory"
	"github.com/kunalpednekar/dumpster/internal/entity"
	entitymem "github.com/kunalpednekar/dumpster/internal/entity/memory"
)

// Compile-time check that the test double satisfies the domain interface.
var _ canonical.Repository = (*canonicalmem.Repository)(nil)

func TestNormalize_LowercasesAndCollapsesWhitespace(t *testing.T) {
	got := canonical.Normalize("  Ada   Lovelace\n")
	want := "ada lovelace"
	if got != want {
		t.Errorf("Normalize: got %q, want %q", got, want)
	}
}

func TestNormalize_DoesNotStripPunctuation(t *testing.T) {
	// Explicitly out of scope per the card: "Apple, Inc." and "Apple Inc"
	// remain distinct identities under exact-match normalization.
	a := canonical.Normalize("Apple, Inc.")
	b := canonical.Normalize("Apple Inc")
	if a == b {
		t.Errorf("Normalize should not strip punctuation: %q collapsed onto %q", a, b)
	}
}

func TestNormalize_UnicodeCompatibilityForms(t *testing.T) {
	// U+FB01 LATIN SMALL LIGATURE FI ("ﬁle") vs the plain two-character
	// spelling ("file") are compatibility-equivalent under NFKC.
	got := canonical.Normalize("ﬁle")
	want := canonical.Normalize("file")
	if got != want {
		t.Errorf("NFKC compatibility forms should normalize identically: got %q, want %q", got, want)
	}
}

func mention(kbID, userID, docID uuid.UUID, text string, typ entity.Type) *entity.Entity {
	return &entity.Entity{
		ID:         uuid.New(),
		KBID:       kbID,
		UserID:     userID,
		DocumentID: docID,
		ChunkID:    uuid.New(),
		Type:       typ,
		Text:       text,
	}
}

func TestCanonicalize_NewMention_CreatesCanonicalEntity(t *testing.T) {
	repo := canonicalmem.New()
	kbID, userID, docID := uuid.New(), uuid.New(), uuid.New()
	m := mention(kbID, userID, docID, "Ada Lovelace", "person")

	resolved, err := repo.Canonicalize(context.Background(), []*entity.Entity{m})
	if err != nil {
		t.Fatal(err)
	}
	canonicalID, ok := resolved[m.ID]
	if !ok {
		t.Fatal("expected mention to be resolved")
	}

	ce, err := repo.Get(context.Background(), userID, canonicalID)
	if err != nil {
		t.Fatal(err)
	}
	if ce.MentionCount != 1 || ce.DocumentCount != 1 {
		t.Errorf("got mention_count=%d document_count=%d, want 1, 1", ce.MentionCount, ce.DocumentCount)
	}
	if ce.CanonicalText != "Ada Lovelace" {
		t.Errorf("CanonicalText: got %q, want %q", ce.CanonicalText, "Ada Lovelace")
	}
}

func TestCanonicalize_SameNormalizedTextAndType_MergesIntoOneCanonicalEntity(t *testing.T) {
	repo := canonicalmem.New()
	kbID, userID, docID := uuid.New(), uuid.New(), uuid.New()
	m1 := mention(kbID, userID, docID, "Ada Lovelace", "person")
	m2 := mention(kbID, userID, docID, "ADA LOVELACE", "person") // same identity, different casing

	resolved, err := repo.Canonicalize(context.Background(), []*entity.Entity{m1, m2})
	if err != nil {
		t.Fatal(err)
	}
	if resolved[m1.ID] != resolved[m2.ID] {
		t.Fatal("expected both mentions to resolve to the same canonical entity")
	}

	ce, err := repo.Get(context.Background(), userID, resolved[m1.ID])
	if err != nil {
		t.Fatal(err)
	}
	if ce.MentionCount != 2 {
		t.Errorf("MentionCount: got %d, want 2", ce.MentionCount)
	}
	if ce.DocumentCount != 1 {
		t.Errorf("DocumentCount: got %d, want 1 (both mentions came from the same document)", ce.DocumentCount)
	}
}

func TestCanonicalize_DifferentType_DoesNotMerge(t *testing.T) {
	repo := canonicalmem.New()
	kbID, userID, docID := uuid.New(), uuid.New(), uuid.New()
	m1 := mention(kbID, userID, docID, "Washington", "person")
	m2 := mention(kbID, userID, docID, "Washington", "location")

	resolved, err := repo.Canonicalize(context.Background(), []*entity.Entity{m1, m2})
	if err != nil {
		t.Fatal(err)
	}
	if resolved[m1.ID] == resolved[m2.ID] {
		t.Fatal("mentions with the same text but different types must not merge")
	}
}

func TestCanonicalize_DocumentCountIsOncePerDocumentNotPerMention(t *testing.T) {
	repo := canonicalmem.New()
	kbID, userID, doc1, doc2 := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	// 3 mentions in doc1, then 1 mention in doc2, all the same identity.
	_, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc1, "FEMA", "org"),
		mention(kbID, userID, doc1, "FEMA", "org"),
		mention(kbID, userID, doc1, "FEMA", "org"),
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved2, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc2, "FEMA", "org"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var canonicalID uuid.UUID
	for _, id := range resolved2 {
		canonicalID = id
	}

	ce, err := repo.Get(context.Background(), userID, canonicalID)
	if err != nil {
		t.Fatal(err)
	}
	if ce.MentionCount != 4 {
		t.Errorf("MentionCount: got %d, want 4", ce.MentionCount)
	}
	if ce.DocumentCount != 2 {
		t.Errorf("DocumentCount: got %d, want 2 (one per contributing document)", ce.DocumentCount)
	}
}

func TestDecrementForDocument_ReversesThatDocumentsContributionOnly(t *testing.T) {
	repo := canonicalmem.New()
	kbID, userID, doc1, doc2 := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	resolved1, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc1, "FEMA", "org"),
		mention(kbID, userID, doc1, "FEMA", "org"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, doc2, "FEMA", "org"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var canonicalID uuid.UUID
	for _, id := range resolved1 {
		canonicalID = id
	}

	if err := repo.DecrementForDocument(context.Background(), userID, doc1); err != nil {
		t.Fatal(err)
	}

	ce, err := repo.Get(context.Background(), userID, canonicalID)
	if err != nil {
		t.Fatal(err)
	}
	if ce.MentionCount != 1 {
		t.Errorf("MentionCount after decrement: got %d, want 1 (doc2's mention remains)", ce.MentionCount)
	}
	if ce.DocumentCount != 1 {
		t.Errorf("DocumentCount after decrement: got %d, want 1", ce.DocumentCount)
	}
}

func TestDecrementForDocument_DeletesCanonicalEntityWhenMentionCountReachesZero(t *testing.T) {
	repo := canonicalmem.New()
	kbID, userID, docID := uuid.New(), uuid.New(), uuid.New()

	resolved, err := repo.Canonicalize(context.Background(), []*entity.Entity{
		mention(kbID, userID, docID, "FEMA", "org"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var canonicalID uuid.UUID
	for _, id := range resolved {
		canonicalID = id
	}

	if err := repo.DecrementForDocument(context.Background(), userID, docID); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.Get(context.Background(), userID, canonicalID); err != canonical.ErrNotFound {
		t.Errorf("expected ErrNotFound after mention_count reached zero, got %v", err)
	}
}

func TestDecrementForDocument_NoOpForUncanonicalizedDocument(t *testing.T) {
	repo := canonicalmem.New()
	userID, docID := uuid.New(), uuid.New()
	if err := repo.DecrementForDocument(context.Background(), userID, docID); err != nil {
		t.Errorf("expected no-op, got error: %v", err)
	}
}

func TestResolveNew_SkipsAlreadyResolvedMentions(t *testing.T) {
	repo := canonicalmem.New()
	entities := entitymem.New()
	kbID, userID, docID := uuid.New(), uuid.New(), uuid.New()

	m1 := mention(kbID, userID, docID, "FEMA", "org")
	m2 := mention(kbID, userID, docID, "FEMA", "org")
	alreadyResolved := uuid.New()
	m2.CanonicalEntityID = &alreadyResolved // simulates a prior successful run

	if err := entities.BulkCreate(context.Background(), []*entity.Entity{m1}); err != nil {
		t.Fatal(err)
	}
	all, _ := entities.ListByDocument(context.Background(), userID, docID)
	// entitymem.BulkCreate reassigns IDs; rebuild m1's reference from storage
	// so entities.BulkSetCanonicalEntityID has a real row to update.
	stored := all[0]
	stored.CanonicalEntityID = nil

	if err := canonical.ResolveNew(context.Background(), repo, entities, userID, []*entity.Entity{stored, m2}); err != nil {
		t.Fatal(err)
	}

	afterList, _ := entities.ListByDocument(context.Background(), userID, docID)
	if len(afterList) != 1 || afterList[0].CanonicalEntityID == nil {
		t.Fatalf("expected the pending mention to be resolved and linked")
	}

	// m2 was never persisted (it stood in only to prove it's excluded from
	// resolution) — confirm resolving didn't touch/duplicate canonical state
	// beyond the one pending mention.
	kb, err := repo.ListByKB(context.Background(), userID, kbID)
	if err != nil {
		t.Fatal(err)
	}
	if len(kb) != 1 || kb[0].MentionCount != 1 {
		t.Fatalf("expected exactly one canonical entity with mention_count 1 (m2 skipped), got %+v", kb)
	}
}

// TestCrossTenantCanonicalLookupDoesNotCollide proves the user_id-scoped
// unique identity holds: two different users both mentioning "Apple" in the
// same normalized form, in the same KB id (an adversarial choice — if
// tenant scoping were missing, this is exactly the case that would
// collide), must resolve to two separate canonical entities, each visible
// only to its own tenant. canonical_entities is the first table in this
// schema where uniqueness is content-keyed rather than incidental to a
// random UUID, so this needs its own coverage rather than inheriting
// confidence from the rest of the schema.
func TestCrossTenantCanonicalLookupDoesNotCollide(t *testing.T) {
	repo := canonicalmem.New()
	kbID := uuid.New() // deliberately shared across both tenants
	userA, userB := uuid.New(), uuid.New()
	docA, docB := uuid.New(), uuid.New()

	mA := mention(kbID, userA, docA, "Apple", "org")
	mB := mention(kbID, userB, docB, "Apple", "org")

	resolvedA, err := repo.Canonicalize(context.Background(), []*entity.Entity{mA})
	if err != nil {
		t.Fatal(err)
	}
	resolvedB, err := repo.Canonicalize(context.Background(), []*entity.Entity{mB})
	if err != nil {
		t.Fatal(err)
	}

	idA, idB := resolvedA[mA.ID], resolvedB[mB.ID]
	if idA == idB {
		t.Fatal("two tenants' mentions of the same text collided onto one canonical entity")
	}

	ceA, err := repo.Get(context.Background(), userA, idA)
	if err != nil {
		t.Fatal(err)
	}
	if ceA.MentionCount != 1 {
		t.Errorf("tenant A's canonical entity: got mention_count %d, want 1 (tenant B's mention must not count toward it)", ceA.MentionCount)
	}

	// Tenant A cannot read tenant B's canonical entity by ID.
	if _, err := repo.Get(context.Background(), userA, idB); err != canonical.ErrNotFound {
		t.Errorf("cross-tenant Get: got err %v, want ErrNotFound", err)
	}

	// Tenant A's KB listing does not include tenant B's row, despite sharing kb_id.
	listA, err := repo.ListByKB(context.Background(), userA, kbID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ce := range listA {
		if ce.ID == idB {
			t.Fatal("tenant A's ListByKB leaked tenant B's canonical entity")
		}
	}
}
