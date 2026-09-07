// Package multihopqa measures whether the full production query pipeline
// (router -> hybrid + graph retrieval -> RRF merge -> answerer) actually
// retrieves the right documents and produces a correct answer for
// questions whose facts span more than one document -- the
// answer-quality counterpart to graphrecall, which only checks that a
// path exists in the graph, not that a real query ever reaches or uses
// it.
package multihopqa

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/search"
)

// Question is one ground-truth multi-hop question.
type Question struct {
	ID             string
	Text           string
	ExpectedAnswer string
	// RequiredDocumentIDs are documents the answer genuinely depends on.
	// A question with none set (e.g. a same-topic control question with no
	// specific pair_id) is not scored on retrieval at all.
	RequiredDocumentIDs []uuid.UUID
}

// QuestionResult is one question's outcome.
type QuestionResult struct {
	QuestionID string
	// RetrievalReached is true if at least one of RequiredDocumentIDs
	// appeared in the retrieved/merged context, or if the question had no
	// RequiredDocumentIDs to check in the first place.
	RetrievalReached bool
	AnswerCorrect    bool
	// ActualAnswer is preserved verbatim so a human reviewing a failure can
	// see what the pipeline actually said, not just pass/fail.
	ActualAnswer string
}

// Result is one run's aggregate outcome.
type Result struct {
	// RetrievalScore is the fraction of questions whose required documents
	// were retrieved, in [0,1].
	RetrievalScore float64
	// AnswerScore is the fraction of questions the judge marked correct,
	// in [0,1]. Independent of RetrievalScore: a lucky guess without
	// retrieving the right documents still counts as answer-correct, and a
	// wrong answer despite retrieving the right documents still counts as
	// answer-incorrect -- comparing the two scores is what tells you
	// whether a failure is retrieval's fault or the answerer's.
	AnswerScore float64
	Results     []QuestionResult
}

// Searcher is the minimal slice of search.Service this package needs, so
// tests exercise a fake rather than the full production stack.
type Searcher interface {
	Search(ctx context.Context, kbID uuid.UUID, query string) (search.Result, error)
}

// AnswerJudge decides whether an actual answer correctly conveys an
// expected answer's key facts. Judging natural-language equivalence isn't
// exact-string-comparable, so this is normally backed by an LLM (see
// LLMJudge) -- kept as its own interface so the scoring logic in Run can
// be tested without one.
type AnswerJudge interface {
	JudgeCorrect(ctx context.Context, question, expectedAnswer, actualAnswer string) (bool, error)
}

// Tester runs the multi-hop QA eval.
type Tester struct {
	Searcher Searcher
	Judge    AnswerJudge
}

// Run executes every question through the real query pipeline (via
// Searcher) and judges the resulting answer (via Judge), scoring
// retrieval and answer correctness independently.
func (t *Tester) Run(ctx context.Context, kbID uuid.UUID, questions []Question) (Result, error) {
	if len(questions) == 0 {
		return Result{}, nil
	}

	results := make([]QuestionResult, 0, len(questions))
	var retrievalHits, answerHits int
	for _, q := range questions {
		res, err := t.Searcher.Search(ctx, kbID, q.Text)
		if err != nil {
			return Result{}, fmt.Errorf("multihopqa: question %s: search: %w", q.ID, err)
		}

		retrievalReached := len(q.RequiredDocumentIDs) == 0 || containsAny(res.RetrievedDocuments, q.RequiredDocumentIDs)
		if retrievalReached {
			retrievalHits++
		}

		correct, err := t.Judge.JudgeCorrect(ctx, q.Text, q.ExpectedAnswer, res.Summary)
		if err != nil {
			return Result{}, fmt.Errorf("multihopqa: question %s: judge: %w", q.ID, err)
		}
		if correct {
			answerHits++
		}

		results = append(results, QuestionResult{
			QuestionID:       q.ID,
			RetrievalReached: retrievalReached,
			AnswerCorrect:    correct,
			ActualAnswer:     res.Summary,
		})
	}

	return Result{
		RetrievalScore: float64(retrievalHits) / float64(len(questions)),
		AnswerScore:    float64(answerHits) / float64(len(questions)),
		Results:        results,
	}, nil
}

func containsAny(haystack []uuid.UUID, needles []uuid.UUID) bool {
	want := make(map[uuid.UUID]struct{}, len(needles))
	for _, id := range needles {
		want[id] = struct{}{}
	}
	for _, id := range haystack {
		if _, ok := want[id]; ok {
			return true
		}
	}
	return false
}
