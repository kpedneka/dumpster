package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/inquiry"
	inquirymem "github.com/kunalpednekar/dumpster/internal/inquiry/memory"
	"github.com/kunalpednekar/dumpster/internal/search"
	searchmock "github.com/kunalpednekar/dumpster/internal/search/mock"
	statsmem "github.com/kunalpednekar/dumpster/internal/stats/memory"
)

// seedOneTurn runs a real search through the handler so the resulting
// Inquiry/messages are created exactly the way production traffic creates
// them, rather than hand-constructing rows that might not match
// persistUserMessage/persistAssistantMessage's actual invariants.
func seedOneTurn(t *testing.T, deps Deps, kbID, userID uuid.UUID, query string) {
	t.Helper()
	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+kbID.String()+"/search",
		strings.NewReader(`{"query":"`+query+`"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(httptest.NewRecorder(), req)
}

// TestReevaluate_ReevaluatingAnAlreadyReevaluatedAnswer is a regression
// test: a re-evaluation is always appended at the end of the message list
// (see AppendMessage's ordinal assignment), never adjacent to the query it
// re-answers. Re-evaluating the current answer in a turn that has already
// been re-evaluated once used to 400, since queryForReEvaluation looked
// only at the message immediately before the target by ordinal — which is
// the wrong message once the target isn't the original answer anymore.
func TestReevaluate_ReevaluatingAnAlreadyReevaluatedAnswer(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	inquiryRepo := deps.Inquiries.(*inquirymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "first answer"})
	seedOneTurn(t, deps, k.ID, userID, "what changed?")

	inq, _ := inquiryRepo.Get(context.TODO(), userID, k.ID)
	before, _ := inquiryRepo.ListMessages(context.TODO(), userID, inq.ID)
	original := before[1]

	// First re-evaluation: succeeds today even without the fix, since the
	// original answer IS adjacent to its query.
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "second answer"})
	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodPost,
		"/kbs/"+k.ID.String()+"/inquiry/messages/"+original.ID.String()+"/reevaluate", nil, userID)
	router.ServeHTTP(httptest.NewRecorder(), req)

	afterFirst, _ := inquiryRepo.ListMessages(context.TODO(), userID, inq.ID)
	firstReeval := afterFirst[2]

	// Second re-evaluation: targets the first re-evaluation, which sits at
	// the end of the message list, nowhere near the original query.
	var gotQuery string
	deps.Searcher = &searchmock.Searcher{
		SearchFn: func(_ context.Context, _ uuid.UUID, query string) (search.Result, error) {
			gotQuery = query
			return search.Result{Summary: "third answer"}, nil
		},
	}
	router = NewRouter(deps)
	req2 := authedRequest(t, deps, http.MethodPost,
		"/kbs/"+k.ID.String()+"/inquiry/messages/"+firstReeval.ID.String()+"/reevaluate", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req2)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotQuery != "what changed?" {
		t.Errorf("re-evaluated query: got %q, want %q", gotQuery, "what changed?")
	}

	after, _ := inquiryRepo.ListMessages(context.TODO(), userID, inq.ID)
	if len(after) != 4 {
		t.Fatalf("messages: got %d, want 4", len(after))
	}
	newest := after[3]
	if newest.Content != "third answer" {
		t.Errorf("newest answer: got %q, want %q", newest.Content, "third answer")
	}
	if newest.SupersedesMessageID == nil || *newest.SupersedesMessageID != firstReeval.ID {
		t.Errorf("SupersedesMessageID: got %v, want %v", newest.SupersedesMessageID, firstReeval.ID)
	}
}

func TestReevaluate_AppendsRatherThanReplaces(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	inquiryRepo := deps.Inquiries.(*inquirymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	doc, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: k.ID, UserID: userID, Filename: "notes.txt", ContentType: "text/plain",
	})
	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary:   "the original answer",
		Citations: []search.Citation{{Number: 1, DocumentID: doc.ID, ChunkID: uuid.New(), CharStart: 0, CharEnd: 10}},
	})
	seedOneTurn(t, deps, k.ID, userID, "what changed?")

	inq, _ := inquiryRepo.Get(context.TODO(), userID, k.ID)
	before, _ := inquiryRepo.ListMessages(context.TODO(), userID, inq.ID)
	if len(before) != 2 {
		t.Fatalf("setup: got %d messages, want 2", len(before))
	}
	original := before[1] // the assistant message

	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "the updated answer"})
	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodPost,
		"/kbs/"+k.ID.String()+"/inquiry/messages/"+original.ID.String()+"/reevaluate", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	frames := parseSSE(t, w.Body.String())
	got := decodeFrame[doneEvent](t, frameNamed(t, frames, "done"))
	if got.Summary != "the updated answer" {
		t.Errorf("summary: got %q, want %q", got.Summary, "the updated answer")
	}

	after, err := inquiryRepo.ListMessages(context.TODO(), userID, inq.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 3 {
		t.Fatalf("messages after reevaluate: got %d, want 3 (original 2 plus one new answer)", len(after))
	}
	if after[1].Content != "the original answer" {
		t.Errorf("original answer should be untouched, got %q", after[1].Content)
	}
	newMsg := after[2]
	if newMsg.Content != "the updated answer" {
		t.Errorf("new message content: got %q, want %q", newMsg.Content, "the updated answer")
	}
	if newMsg.SupersedesMessageID == nil || *newMsg.SupersedesMessageID != original.ID {
		t.Errorf("SupersedesMessageID: got %v, want %v", newMsg.SupersedesMessageID, original.ID)
	}
	// The re-evaluation answers the same underlying question without
	// duplicating the user's query as a new turn.
	if newMsg.Role != inquiry.RoleAssistant {
		t.Errorf("new message role: got %q, want assistant", newMsg.Role)
	}
}

func TestReevaluate_RerunsTheOriginalQueryText(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	inquiryRepo := deps.Inquiries.(*inquirymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "answer 1"})
	seedOneTurn(t, deps, k.ID, userID, "what is the capital of France?")

	inq, _ := inquiryRepo.Get(context.TODO(), userID, k.ID)
	before, _ := inquiryRepo.ListMessages(context.TODO(), userID, inq.ID)
	original := before[1]

	var gotQuery string
	deps.Searcher = &searchmock.Searcher{
		SearchFn: func(_ context.Context, _ uuid.UUID, query string) (search.Result, error) {
			gotQuery = query
			return search.Result{Summary: "answer 2"}, nil
		},
	}
	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodPost,
		"/kbs/"+k.ID.String()+"/inquiry/messages/"+original.ID.String()+"/reevaluate", nil, userID)
	router.ServeHTTP(httptest.NewRecorder(), req)

	if gotQuery != "what is the capital of France?" {
		t.Errorf("re-evaluated query: got %q, want %q", gotQuery, "what is the capital of France?")
	}
}

func TestReevaluate_UserMessageIsNotReevaluatable(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	inquiryRepo := deps.Inquiries.(*inquirymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "an answer"})
	seedOneTurn(t, deps, k.ID, userID, "a question")

	inq, _ := inquiryRepo.Get(context.TODO(), userID, k.ID)
	before, _ := inquiryRepo.ListMessages(context.TODO(), userID, inq.ID)
	userMessage := before[0]

	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodPost,
		"/kbs/"+k.ID.String()+"/inquiry/messages/"+userMessage.ID.String()+"/reevaluate", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestReevaluate_UnknownMessage(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "an answer"})
	seedOneTurn(t, deps, k.ID, userID, "a question")

	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodPost,
		"/kbs/"+k.ID.String()+"/inquiry/messages/"+uuid.New().String()+"/reevaluate", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestReevaluate_NoInquiryYet(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")

	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodPost,
		"/kbs/"+k.ID.String()+"/inquiry/messages/"+uuid.New().String()+"/reevaluate", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestReevaluate_CrossTenant(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	inquiryRepo := deps.Inquiries.(*inquirymem.Repository)
	owner, other := uuid.New(), uuid.New()
	k, _ := kbRepo.Create(context.TODO(), owner, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "an answer"})
	seedOneTurn(t, deps, k.ID, owner, "a question")

	inq, _ := inquiryRepo.Get(context.TODO(), owner, k.ID)
	before, _ := inquiryRepo.ListMessages(context.TODO(), owner, inq.ID)
	original := before[1]

	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodPost,
		"/kbs/"+k.ID.String()+"/inquiry/messages/"+original.ID.String()+"/reevaluate", nil, other)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("other tenant should not be able to reevaluate owner's message")
	}
}

// TestReevaluate_CountsAsASeparateQueryExecuted verifies a re-evaluation
// increments the durable usage counters (see internal/stats) same as an
// original search — it re-runs retrieval and generation in full, so it's a
// distinct query execution, not a repeat of the one it supersedes.
func TestReevaluate_CountsAsASeparateQueryExecuted(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	statsRepo := deps.Stats.(*statsmem.Repository)
	inquiryRepo := deps.Inquiries.(*inquirymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "first answer"})
	seedOneTurn(t, deps, k.ID, userID, "what changed?")

	inq, _ := inquiryRepo.Get(context.TODO(), userID, k.ID)
	before, _ := inquiryRepo.ListMessages(context.TODO(), userID, inq.ID)
	original := before[1]

	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "second answer"})
	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodPost,
		"/kbs/"+k.ID.String()+"/inquiry/messages/"+original.ID.String()+"/reevaluate", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	snap, err := statsRepo.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if snap.QueriesExecuted != 2 {
		t.Errorf("QueriesExecuted = %d, want 2 (original search + reevaluate)", snap.QueriesExecuted)
	}
}
