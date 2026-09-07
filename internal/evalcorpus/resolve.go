package evalcorpus

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/graphrecall"
)

// GroundTruth is the subset of validation/ground_truth.json this package
// needs -- entity identities, cross-document pairs (for the graph-recall
// metric), and multi-hop questions (for the QA eval). Deliberately not the
// whole schema -- canonicalization_test_cases lives entirely in the
// fixture file and isn't needed by any Go code yet.
type GroundTruth struct {
	Entities           []Entity            `json:"entities"`
	CrossDocumentPairs []CrossDocumentPair `json:"cross_document_pairs"`
	MultiHopQuestions  []MultiHopQuestion  `json:"multi_hop_questions"`
}

// Entity is one named entity in the corpus, with the corpus-relative file
// paths it appears in.
type Entity struct {
	ID            string   `json:"id"`
	CanonicalName string   `json:"canonical_name"`
	Docs          []string `json:"docs"`
}

// CrossDocumentPair is one known cross-chunk/cross-document relationship,
// referencing two Entity.ID values.
type CrossDocumentPair struct {
	ID         string `json:"id"`
	EntityA    string `json:"entity_a"`
	EntityB    string `json:"entity_b"`
	Difficulty string `json:"difficulty"`
}

// ResolvePairs turns gt's cross-document pairs into graphrecall.Pair values
// against real, already-ingested documents: entity_a's canonical name
// becomes the seed query (matching how both graphrag legs seed from query
// text), and entity_b's corpus-relative docs are resolved to real document
// IDs via filenameToDocID -- normally built from a document.Repository
// listing for the benchmark KB, keyed by Filename (which IngestFile sets
// to the exact corpus-relative path).
//
// Returns an error naming the missing entity or document rather than
// silently skipping a pair, since a silently-skipped pair would make the
// score look better than it is -- a KB with only half its documents
// ingested should fail loudly, not report a deceptively high score over
// whatever happened to be ready.
func ResolvePairs(gt GroundTruth, filenameToDocID map[string]uuid.UUID) ([]graphrecall.Pair, error) {
	byID := make(map[string]Entity, len(gt.Entities))
	for _, e := range gt.Entities {
		byID[e.ID] = e
	}

	pairs := make([]graphrecall.Pair, 0, len(gt.CrossDocumentPairs))
	for _, cdp := range gt.CrossDocumentPairs {
		a, ok := byID[cdp.EntityA]
		if !ok {
			return nil, fmt.Errorf("evalcorpus: pair %s: unknown entity_a %q", cdp.ID, cdp.EntityA)
		}
		b, ok := byID[cdp.EntityB]
		if !ok {
			return nil, fmt.Errorf("evalcorpus: pair %s: unknown entity_b %q", cdp.ID, cdp.EntityB)
		}

		expectedDocs := make([]uuid.UUID, 0, len(b.Docs))
		for _, relPath := range b.Docs {
			docID, ok := filenameToDocID[relPath]
			if !ok {
				return nil, fmt.Errorf("evalcorpus: pair %s: entity_b %q's document %q hasn't been ingested yet", cdp.ID, cdp.EntityB, relPath)
			}
			expectedDocs = append(expectedDocs, docID)
		}

		pairs = append(pairs, graphrecall.Pair{
			ID:                  cdp.ID,
			SeedQuery:           a.CanonicalName,
			ExpectedDocumentIDs: expectedDocs,
		})
	}
	return pairs, nil
}
