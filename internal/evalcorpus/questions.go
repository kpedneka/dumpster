package evalcorpus

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/multihopqa"
)

// MultiHopQuestion is one hand-authored question in
// validation/ground_truth.json's multi_hop_questions array.
type MultiHopQuestion struct {
	ID             string   `json:"id"`
	Question       string   `json:"question"`
	ExpectedAnswer string   `json:"expected_answer"`
	RequiredDocs   []string `json:"required_documents"`
}

// ResolveQuestions turns gt's multi-hop questions into multihopqa.Question
// values against real, already-ingested documents. Unlike ResolvePairs,
// required_documents are already corpus-relative file paths in the fixture
// (not resolved indirectly through an entity's docs list), so this is a
// direct lookup per path.
//
// A question with no required_documents (e.g. a same-topic control
// question) resolves with an empty RequiredDocumentIDs, which
// multihopqa.Tester treats as "not scored on retrieval" rather than an
// error -- there's nothing specific to check.
func ResolveQuestions(gt GroundTruth, filenameToDocID map[string]uuid.UUID) ([]multihopqa.Question, error) {
	questions := make([]multihopqa.Question, 0, len(gt.MultiHopQuestions))
	for _, q := range gt.MultiHopQuestions {
		docIDs := make([]uuid.UUID, 0, len(q.RequiredDocs))
		for _, relPath := range q.RequiredDocs {
			docID, ok := filenameToDocID[relPath]
			if !ok {
				return nil, fmt.Errorf("evalcorpus: question %s: required document %q hasn't been ingested yet", q.ID, relPath)
			}
			docIDs = append(docIDs, docID)
		}
		questions = append(questions, multihopqa.Question{
			ID:                  q.ID,
			Text:                q.Question,
			ExpectedAnswer:      q.ExpectedAnswer,
			RequiredDocumentIDs: docIDs,
		})
	}
	return questions, nil
}
