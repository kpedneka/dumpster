package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/inquiry"
	"github.com/kunalpednekar/dumpster/internal/search"
	searchmock "github.com/kunalpednekar/dumpster/internal/search/mock"
)

// TestBuildInquiryMessages_RecoversRetrievedDocumentNameFromCitation is a
// regression test: found on a real message written between two fixes —
// Citation.FileName was already being snapshotted, RetrievedDocument's
// wasn't yet — leaving retrieved_documents blank for a document a citation
// in the very same message already named correctly. The name is right
// there; there's no reason to show blank when it's recoverable from the
// message's own data.
func TestBuildInquiryMessages_RecoversRetrievedDocumentNameFromCitation(t *testing.T) {
	docID := uuid.New()
	messages := []*inquiry.Message{
		{
			ID:   uuid.New(),
			Role: inquiry.RoleAssistant,
			Citations: []inquiry.Citation{
				{Number: 1, DocumentID: docID, FileName: "notes.txt"},
			},
			RetrievedDocuments: []inquiry.RetrievedDocument{
				{DocumentID: docID, FileName: ""}, // old-format row, unmarshaled with a blank name
			},
		},
	}

	out := buildInquiryMessages(messages)

	if len(out[0].RetrievedDocuments) != 1 || out[0].RetrievedDocuments[0].FileName != "notes.txt" {
		t.Errorf("got %+v, want file_name recovered as notes.txt", out[0].RetrievedDocuments)
	}
}

// TestBuildInquiryMessages_NoFallbackAvailable confirms the fallback
// degrades to blank, not a crash or a wrong name, when no citation for the
// document exists to recover a name from either.
func TestBuildInquiryMessages_NoFallbackAvailable(t *testing.T) {
	docID := uuid.New()
	messages := []*inquiry.Message{
		{
			ID:                 uuid.New(),
			Role:               inquiry.RoleAssistant,
			RetrievedDocuments: []inquiry.RetrievedDocument{{DocumentID: docID, FileName: ""}},
		},
	}

	out := buildInquiryMessages(messages)

	if len(out[0].RetrievedDocuments) != 1 || out[0].RetrievedDocuments[0].FileName != "" {
		t.Errorf("got %+v, want blank file_name with no citation to recover from", out[0].RetrievedDocuments)
	}
}

func TestGetInquiry_EmptyWhenNoSearchHasRunYet(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")

	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/inquiry", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	got := decodeJSON[inquiryResponse](t, w.Body.Bytes())
	if got.ID != nil {
		t.Errorf("ID: got %v, want nil", *got.ID)
	}
	if len(got.Messages) != 0 {
		t.Errorf("messages: got %d, want 0", len(got.Messages))
	}
}

func TestGetInquiry_ReturnsAccumulatedTurns(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	doc, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: k.ID, UserID: userID, Filename: "notes.txt", ContentType: "text/plain",
	})
	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary:            "the answer",
		Citations:          []search.Citation{{Number: 1, DocumentID: doc.ID, ChunkID: uuid.New(), CharStart: 0, CharEnd: 10}},
		RetrievedDocuments: []uuid.UUID{doc.ID},
	})
	seedOneTurn(t, deps, k.ID, userID, "what is the answer?")

	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/inquiry", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	got := decodeJSON[inquiryResponse](t, w.Body.Bytes())
	if got.ID == nil {
		t.Fatal("expected a non-nil Inquiry ID")
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages: got %d, want 2", len(got.Messages))
	}
	if got.Messages[0].Role != "user" || got.Messages[0].Content != "what is the answer?" {
		t.Errorf("first message: got role=%q content=%q", got.Messages[0].Role, got.Messages[0].Content)
	}
	assistant := got.Messages[1]
	if assistant.Role != "assistant" || assistant.Content != "the answer" {
		t.Errorf("second message: got role=%q content=%q", assistant.Role, assistant.Content)
	}
	if len(assistant.Citations) != 1 || assistant.Citations[0].FileName != "notes.txt" {
		t.Errorf("citation: got %+v, want file_name=notes.txt", assistant.Citations)
	}
	if len(assistant.RetrievedDocuments) != 1 || assistant.RetrievedDocuments[0].FileName != "notes.txt" {
		t.Errorf("retrieved documents: got %+v, want one entry named notes.txt", assistant.RetrievedDocuments)
	}
	if assistant.SupersedesMessageID != nil {
		t.Errorf("SupersedesMessageID: got %v, want nil for an ordinary turn", *assistant.SupersedesMessageID)
	}
}

// TestGetInquiry_RetrievedDocumentFileNameSurvivesDeletion is a regression
// test: RetrievedDocuments' file names used to be resolved via a live
// document lookup at GET-time, going blank the moment the source document
// was deleted — unlike citations, which were already snapshotted for
// exactly this reason. Both are snapshotted now.
func TestGetInquiry_RetrievedDocumentFileNameSurvivesDeletion(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	doc, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: k.ID, UserID: userID, Filename: "notes.txt", ContentType: "text/plain",
	})
	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary:            "the answer",
		Citations:          []search.Citation{{Number: 1, DocumentID: doc.ID, ChunkID: uuid.New(), CharStart: 0, CharEnd: 10}},
		RetrievedDocuments: []uuid.UUID{doc.ID},
	})
	seedOneTurn(t, deps, k.ID, userID, "what is the answer?")

	if err := docRepo.Delete(context.TODO(), userID, doc.ID); err != nil {
		t.Fatal(err)
	}

	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/inquiry", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	got := decodeJSON[inquiryResponse](t, w.Body.Bytes())
	assistant := got.Messages[1]
	if len(assistant.Citations) != 1 || assistant.Citations[0].FileName != "notes.txt" {
		t.Errorf("citation file_name after document deletion: got %+v, want notes.txt", assistant.Citations)
	}
	if len(assistant.RetrievedDocuments) != 1 || assistant.RetrievedDocuments[0].FileName != "notes.txt" {
		t.Errorf("retrieved document file_name after document deletion: got %+v, want notes.txt", assistant.RetrievedDocuments)
	}
}

func TestGetInquiry_MarksSupersedesOnReevaluatedMessage(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "first answer"})
	seedOneTurn(t, deps, k.ID, userID, "a question")

	router := NewRouter(deps)
	getReq := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/inquiry", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, getReq)
	before := decodeJSON[inquiryResponse](t, w.Body.Bytes())
	originalID := before.Messages[1].ID

	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "updated answer"})
	router = NewRouter(deps)
	reevalReq := authedRequest(t, deps, http.MethodPost,
		"/kbs/"+k.ID.String()+"/inquiry/messages/"+originalID+"/reevaluate", nil, userID)
	router.ServeHTTP(httptest.NewRecorder(), reevalReq)

	getReq2 := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/inquiry", nil, userID)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, getReq2)
	after := decodeJSON[inquiryResponse](t, w2.Body.Bytes())

	if len(after.Messages) != 3 {
		t.Fatalf("messages: got %d, want 3", len(after.Messages))
	}
	newMsg := after.Messages[2]
	if newMsg.SupersedesMessageID == nil || *newMsg.SupersedesMessageID != originalID {
		t.Errorf("SupersedesMessageID: got %v, want %v", newMsg.SupersedesMessageID, originalID)
	}
}

func TestGetInquiry_TenantIsolation(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	owner, other := uuid.New(), uuid.New()
	k, _ := kbRepo.Create(context.TODO(), owner, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "an answer"})
	seedOneTurn(t, deps, k.ID, owner, "a question")

	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/inquiry", nil, other)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("other tenant should not be able to read owner's inquiry")
	}
}

func TestGetInquiry_NilInquiries_ReturnsEmptyWithoutFailing(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.Inquiries = nil
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")

	router := NewRouter(deps)
	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/inquiry", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}
	got := decodeJSON[inquiryResponse](t, w.Body.Bytes())
	if got.ID != nil || len(got.Messages) != 0 {
		t.Errorf("got %+v, want empty response", got)
	}
}
