package evalcorpus_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/evalcorpus"
)

func TestResolvePairs_MapsEntityDocsToRealDocumentIDs(t *testing.T) {
	gt := evalcorpus.GroundTruth{
		Entities: []evalcorpus.Entity{
			{ID: "voss", CanonicalName: "Elena Voss", Docs: []string{"maritime/01_biography.txt"}},
			{ID: "steadfast_mount", CanonicalName: "Steadfast Compass Mount", Docs: []string{"maritime/02_product_history.txt"}},
		},
		CrossDocumentPairs: []evalcorpus.CrossDocumentPair{
			{ID: "maritime_1", EntityA: "voss", EntityB: "steadfast_mount", Difficulty: "easy"},
		},
	}
	docB := uuid.New()
	filenameToDocID := map[string]uuid.UUID{
		"maritime/01_biography.txt":       uuid.New(),
		"maritime/02_product_history.txt": docB,
	}

	pairs, err := evalcorpus.ResolvePairs(gt, filenameToDocID)
	if err != nil {
		t.Fatalf("ResolvePairs: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs, want 1", len(pairs))
	}
	if pairs[0].ID != "maritime_1" {
		t.Errorf("ID = %q, want maritime_1", pairs[0].ID)
	}
	if pairs[0].SeedQuery != "Elena Voss" {
		t.Errorf("SeedQuery = %q, want the seed entity's canonical name", pairs[0].SeedQuery)
	}
	if len(pairs[0].ExpectedDocumentIDs) != 1 || pairs[0].ExpectedDocumentIDs[0] != docB {
		t.Errorf("ExpectedDocumentIDs = %v, want [%v]", pairs[0].ExpectedDocumentIDs, docB)
	}
}

func TestResolvePairs_ErrorsOnUnknownEntityReference(t *testing.T) {
	gt := evalcorpus.GroundTruth{
		Entities: []evalcorpus.Entity{
			{ID: "voss", CanonicalName: "Elena Voss", Docs: []string{"maritime/01_biography.txt"}},
		},
		CrossDocumentPairs: []evalcorpus.CrossDocumentPair{
			{ID: "bad_pair", EntityA: "voss", EntityB: "does_not_exist", Difficulty: "easy"},
		},
	}
	_, err := evalcorpus.ResolvePairs(gt, map[string]uuid.UUID{"maritime/01_biography.txt": uuid.New()})
	if err == nil {
		t.Fatal("expected an error for an unresolvable entity_b reference, got nil")
	}
}

func TestResolvePairs_ErrorsWhenDocumentNotYetIngested(t *testing.T) {
	gt := evalcorpus.GroundTruth{
		Entities: []evalcorpus.Entity{
			{ID: "voss", CanonicalName: "Elena Voss", Docs: []string{"maritime/01_biography.txt"}},
			{ID: "steadfast_mount", CanonicalName: "Steadfast Compass Mount", Docs: []string{"maritime/02_product_history.txt"}},
		},
		CrossDocumentPairs: []evalcorpus.CrossDocumentPair{
			{ID: "maritime_1", EntityA: "voss", EntityB: "steadfast_mount", Difficulty: "easy"},
		},
	}
	// filenameToDocID is missing maritime/02_product_history.txt entirely --
	// simulates scoring before that document finished ingesting.
	_, err := evalcorpus.ResolvePairs(gt, map[string]uuid.UUID{"maritime/01_biography.txt": uuid.New()})
	if err == nil {
		t.Fatal("expected an error when an entity's document hasn't been ingested yet, got nil")
	}
}
