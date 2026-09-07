package multihopqa_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/multihopqa"
	"github.com/kunalpednekar/dumpster/internal/search"
)

type fakeSearcher struct {
	results map[string]search.Result
	err     error
}

func (f *fakeSearcher) Search(_ context.Context, _ uuid.UUID, query string) (search.Result, error) {
	if f.err != nil {
		return search.Result{}, f.err
	}
	return f.results[query], nil
}

type fakeJudge struct {
	correct map[string]bool
	err     error
}

func (f *fakeJudge) JudgeCorrect(_ context.Context, question, _, _ string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.correct[question], nil
}

func TestRun_ScoresRetrievalAndAnswerIndependently(t *testing.T) {
	docA, docB := uuid.New(), uuid.New()
	kbID := uuid.New()

	searcher := &fakeSearcher{results: map[string]search.Result{
		"q1": {Summary: "answer one", RetrievedDocuments: []uuid.UUID{docA, docB}},
		"q2": {Summary: "wrong answer", RetrievedDocuments: []uuid.UUID{docB}},
	}}
	judge := &fakeJudge{correct: map[string]bool{"q1": true, "q2": false}}

	tester := &multihopqa.Tester{Searcher: searcher, Judge: judge}
	result, err := tester.Run(context.Background(), kbID, []multihopqa.Question{
		{ID: "q1id", Text: "q1", ExpectedAnswer: "expected one", RequiredDocumentIDs: []uuid.UUID{docA}},
		{ID: "q2id", Text: "q2", ExpectedAnswer: "expected two", RequiredDocumentIDs: []uuid.UUID{docA}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.RetrievalScore != 0.5 {
		t.Errorf("RetrievalScore = %v, want 0.5 (only q1 retrieved a required doc)", result.RetrievalScore)
	}
	if result.AnswerScore != 0.5 {
		t.Errorf("AnswerScore = %v, want 0.5 (only q1 judged correct)", result.AnswerScore)
	}
	if len(result.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(result.Results))
	}

	byID := map[string]multihopqa.QuestionResult{}
	for _, r := range result.Results {
		byID[r.QuestionID] = r
	}
	if !byID["q1id"].RetrievalReached || !byID["q1id"].AnswerCorrect {
		t.Errorf("q1id = %+v, want both true", byID["q1id"])
	}
	if byID["q2id"].RetrievalReached {
		t.Error("q2id.RetrievalReached = true, want false (docA never retrieved)")
	}
	if byID["q2id"].AnswerCorrect {
		t.Errorf("q2id.AnswerCorrect = true, want false")
	}
	if byID["q2id"].ActualAnswer != "wrong answer" {
		t.Errorf("ActualAnswer = %q, want the raw answer preserved for inspection", byID["q2id"].ActualAnswer)
	}
}

func TestRun_QuestionWithNoRequiredDocuments_RetrievalReachedTrueByDefault(t *testing.T) {
	kbID := uuid.New()
	searcher := &fakeSearcher{results: map[string]search.Result{
		"control": {Summary: "some answer", RetrievedDocuments: nil},
	}}
	judge := &fakeJudge{correct: map[string]bool{"control": true}}

	tester := &multihopqa.Tester{Searcher: searcher, Judge: judge}
	result, err := tester.Run(context.Background(), kbID, []multihopqa.Question{
		{ID: "control", Text: "control", ExpectedAnswer: "x", RequiredDocumentIDs: nil},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Results[0].RetrievalReached {
		t.Error("a question with no RequiredDocumentIDs should not be penalized on retrieval -- it has nothing specific to check")
	}
}

func TestRun_PropagatesSearchError(t *testing.T) {
	kbID := uuid.New()
	searcher := &fakeSearcher{err: errors.New("boom")}
	tester := &multihopqa.Tester{Searcher: searcher, Judge: &fakeJudge{}}

	_, err := tester.Run(context.Background(), kbID, []multihopqa.Question{
		{ID: "q1", Text: "q1", ExpectedAnswer: "x"},
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestRun_PropagatesJudgeError(t *testing.T) {
	kbID := uuid.New()
	searcher := &fakeSearcher{results: map[string]search.Result{"q1": {Summary: "a"}}}
	judge := &fakeJudge{err: errors.New("judge boom")}
	tester := &multihopqa.Tester{Searcher: searcher, Judge: judge}

	_, err := tester.Run(context.Background(), kbID, []multihopqa.Question{
		{ID: "q1", Text: "q1", ExpectedAnswer: "x"},
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestRun_EmptyQuestions_ReturnsZeroResult(t *testing.T) {
	tester := &multihopqa.Tester{Searcher: &fakeSearcher{}, Judge: &fakeJudge{}}
	result, err := tester.Run(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Results) != 0 {
		t.Errorf("expected no results for no questions, got %+v", result.Results)
	}
}
