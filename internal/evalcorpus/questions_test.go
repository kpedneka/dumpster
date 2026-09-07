package evalcorpus_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/evalcorpus"
)

func TestResolveQuestions_MapsRequiredDocumentsToRealIDs(t *testing.T) {
	gt := evalcorpus.GroundTruth{
		MultiHopQuestions: []evalcorpus.MultiHopQuestion{
			{
				ID:             "q1",
				Question:       "What is X?",
				ExpectedAnswer: "X is Y",
				RequiredDocs:   []string{"maritime/01_biography.txt", "maritime/02_product_history.txt"},
			},
		},
	}
	docA, docB := uuid.New(), uuid.New()
	filenameToDocID := map[string]uuid.UUID{
		"maritime/01_biography.txt":       docA,
		"maritime/02_product_history.txt": docB,
	}

	questions, err := evalcorpus.ResolveQuestions(gt, filenameToDocID)
	if err != nil {
		t.Fatalf("ResolveQuestions: %v", err)
	}
	if len(questions) != 1 {
		t.Fatalf("got %d questions, want 1", len(questions))
	}
	q := questions[0]
	if q.ID != "q1" || q.Text != "What is X?" || q.ExpectedAnswer != "X is Y" {
		t.Errorf("unexpected question: %+v", q)
	}
	if len(q.RequiredDocumentIDs) != 2 {
		t.Fatalf("RequiredDocumentIDs = %v, want 2 entries", q.RequiredDocumentIDs)
	}
	got := map[uuid.UUID]bool{q.RequiredDocumentIDs[0]: true, q.RequiredDocumentIDs[1]: true}
	if !got[docA] || !got[docB] {
		t.Errorf("RequiredDocumentIDs = %v, want [%v %v]", q.RequiredDocumentIDs, docA, docB)
	}
}

func TestResolveQuestions_NoRequiredDocumentsIsFineNotAnError(t *testing.T) {
	gt := evalcorpus.GroundTruth{
		MultiHopQuestions: []evalcorpus.MultiHopQuestion{
			{ID: "control", Question: "control question", ExpectedAnswer: "x", RequiredDocs: nil},
		},
	}
	questions, err := evalcorpus.ResolveQuestions(gt, map[string]uuid.UUID{})
	if err != nil {
		t.Fatalf("ResolveQuestions: %v", err)
	}
	if len(questions) != 1 || len(questions[0].RequiredDocumentIDs) != 0 {
		t.Errorf("unexpected result: %+v", questions)
	}
}

func TestResolveQuestions_ErrorsWhenRequiredDocumentNotYetIngested(t *testing.T) {
	gt := evalcorpus.GroundTruth{
		MultiHopQuestions: []evalcorpus.MultiHopQuestion{
			{ID: "q1", Question: "q", ExpectedAnswer: "a", RequiredDocs: []string{"missing/doc.txt"}},
		},
	}
	_, err := evalcorpus.ResolveQuestions(gt, map[string]uuid.UUID{})
	if err == nil {
		t.Fatal("expected an error for a required document that hasn't been ingested, got nil")
	}
}
