package server

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/kunalpednekar/dumpster/internal/telemetry/telemetrytest"
)

var errFakeGeneration = errors.New("generation failed")

// sseFrame is one parsed "event: <name>\ndata: <json>\n\n" frame.
type sseFrame struct {
	name string
	data string
}

// parseSSE splits a recorded SSE response body into its frames.
func parseSSE(t *testing.T, body string) []sseFrame {
	t.Helper()
	var frames []sseFrame
	for _, raw := range strings.Split(strings.TrimRight(body, "\n"), "\n\n") {
		if raw == "" {
			continue
		}
		var f sseFrame
		for _, line := range strings.Split(raw, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				f.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = strings.TrimPrefix(line, "data: ")
			}
		}
		frames = append(frames, f)
	}
	return frames
}

// frameNamed returns the first frame with the given name, failing the test
// if none exists.
func frameNamed(t *testing.T, frames []sseFrame, name string) sseFrame {
	t.Helper()
	for _, f := range frames {
		if f.name == name {
			return f
		}
	}
	t.Fatalf("no %q frame found among %+v", name, frames)
	return sseFrame{}
}

func decodeFrame[T any](t *testing.T, f sseFrame) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(f.data), &v); err != nil {
		t.Fatalf("decode %q frame data %q: %v", f.name, f.data, err)
	}
	return v
}

// decodeJSON decodes a plain (non-SSE) JSON response body.
func decodeJSON[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode body %q: %v", body, err)
	}
	return v
}

func TestSearch_StreamsSSEWithContentType(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "the answer"})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"what is the answer?"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}
}

func TestSearch_DoneEventCarriesSummaryAndCitations(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	doc, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: k.ID, UserID: userID, Filename: "notes.txt", ContentType: "text/plain",
	})

	chunkID := uuid.New()
	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary: "the answer",
		Citations: []search.Citation{
			{Number: 1, DocumentID: doc.ID, ChunkID: chunkID, CharStart: 0, CharEnd: 42, Text: "the source text"},
		},
	})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"what is the answer?"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	frames := parseSSE(t, w.Body.String())
	got := decodeFrame[doneEvent](t, frameNamed(t, frames, "done"))

	if got.Summary != "the answer" {
		t.Errorf("summary: got %q, want %q", got.Summary, "the answer")
	}
	if len(got.Citations) != 1 {
		t.Fatalf("citations: got %d, want 1", len(got.Citations))
	}
	c := got.Citations[0]
	if c.Number != 1 || c.DocumentID != doc.ID.String() || c.CharStart != 0 || c.CharEnd != 42 {
		t.Errorf("citation mismatch: %+v", c)
	}
	if c.FileName != "notes.txt" {
		t.Errorf("citation file_name: got %q, want %q", c.FileName, "notes.txt")
	}
	if c.Locator != nil {
		t.Errorf("citation locator: got %+v, want nil for a plain-text chunk", c.Locator)
	}
}

// TestSearch_PersistsUserAndAssistantMessages verifies that a successful
// search records both turns of the exchange as Inquiry messages, with the
// assistant message snapshotting a FileName onto its citation from the
// same request-time lookup used for the live response.
func TestSearch_PersistsUserAndAssistantMessages(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	inquiryRepo := deps.Inquiries.(*inquirymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	doc, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: k.ID, UserID: userID, Filename: "notes.txt", ContentType: "text/plain",
	})
	chunkID := uuid.New()
	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary:            "the answer",
		Citations:          []search.Citation{{Number: 1, DocumentID: doc.ID, ChunkID: chunkID, CharStart: 0, CharEnd: 42}},
		RetrievedDocuments: []uuid.UUID{doc.ID},
	})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"what is the answer?"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(httptest.NewRecorder(), req)

	inq, err := inquiryRepo.Get(context.TODO(), userID, k.ID)
	if err != nil {
		t.Fatalf("expected an Inquiry to have been created, got: %v", err)
	}
	messages, err := inquiryRepo.ListMessages(context.TODO(), userID, inq.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages: got %d, want 2 (one user, one assistant)", len(messages))
	}
	if messages[0].Role != inquiry.RoleUser || messages[0].Content != "what is the answer?" {
		t.Errorf("first message: got role=%q content=%q", messages[0].Role, messages[0].Content)
	}
	if messages[1].Role != inquiry.RoleAssistant || messages[1].Content != "the answer" {
		t.Errorf("second message: got role=%q content=%q", messages[1].Role, messages[1].Content)
	}
	if len(messages[1].Citations) != 1 || messages[1].Citations[0].FileName != "notes.txt" {
		t.Errorf("assistant message citation FileName: got %+v, want notes.txt", messages[1].Citations)
	}
	if len(messages[1].RetrievedDocuments) != 1 || messages[1].RetrievedDocuments[0].DocumentID != doc.ID ||
		messages[1].RetrievedDocuments[0].FileName != "notes.txt" {
		t.Errorf("assistant message retrieved documents: got %+v, want one entry {%v, notes.txt}", messages[1].RetrievedDocuments, doc.ID)
	}
}

// TestSearch_MultipleSearchesAccumulateAsTurns verifies that a second search
// against the same KB appends to the existing Inquiry rather than replacing
// it or starting a new one — the core "one Inquiry per (kb, user)" contract.
func TestSearch_MultipleSearchesAccumulateAsTurns(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	inquiryRepo := deps.Inquiries.(*inquirymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "an answer"})
	router := NewRouter(deps)

	for _, q := range []string{"first question", "second question"} {
		req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
			strings.NewReader(`{"query":"`+q+`"}`), userID)
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(httptest.NewRecorder(), req)
	}

	inq, err := inquiryRepo.Get(context.TODO(), userID, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := inquiryRepo.ListMessages(context.TODO(), userID, inq.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 {
		t.Fatalf("messages: got %d, want 4 (2 searches x user+assistant)", len(messages))
	}
	if messages[0].Content != "first question" || messages[2].Content != "second question" {
		t.Errorf("turn order: got %q then %q, want the two questions in submission order",
			messages[0].Content, messages[2].Content)
	}
}

// TestSearch_NilInquiries_SkipsPersistenceWithoutFailingTheRequest verifies
// that Inquiry persistence is genuinely optional: a nil Deps.Inquiries must
// not prevent search from working, matching how Manifest/Instruments are
// already optional dependencies elsewhere in Deps.
func TestSearch_NilInquiries_SkipsPersistenceWithoutFailingTheRequest(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.Inquiries = nil
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "an answer"})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"a question"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}
	frames := parseSSE(t, w.Body.String())
	got := decodeFrame[doneEvent](t, frameNamed(t, frames, "done"))
	if got.Summary != "an answer" {
		t.Errorf("summary: got %q, want %q", got.Summary, "an answer")
	}
}

// TestSearch_CitationLocator_PDFPage verifies that a citation resolved from a
// PDF-derived chunk (PageNumber set) carries a page locator, mirroring how
// search engines cite the source page rather than a byte range within it.
func TestSearch_CitationLocator_PDFPage(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	doc, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: k.ID, UserID: userID, Filename: "paper.pdf", ContentType: "application/pdf",
	})

	page := 4
	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary: "the answer",
		Citations: []search.Citation{
			{Number: 1, DocumentID: doc.ID, ChunkID: uuid.New(), CharStart: 0, CharEnd: 10, Text: "conclusion text", PageNumber: &page},
		},
	})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"what is the conclusion?"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	frames := parseSSE(t, w.Body.String())
	got := decodeFrame[doneEvent](t, frameNamed(t, frames, "done"))

	if got.Citations[0].FileName != "paper.pdf" {
		t.Errorf("citation file_name: got %q, want %q", got.Citations[0].FileName, "paper.pdf")
	}
	if got.Citations[0].Locator == nil {
		t.Fatalf("citation locator: got nil, want a page locator")
	}
	if got.Citations[0].Locator.Type != "page" || got.Citations[0].Locator.Value != 4 {
		t.Errorf("citation locator: got %+v, want {type:page value:4}", got.Citations[0].Locator)
	}
}

// TestSearch_CitationLocator_UnknownDocument verifies a citation still
// renders (with an empty file name) if its document can't be resolved,
// rather than failing the whole search response.
func TestSearch_CitationLocator_UnknownDocument(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")

	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary: "the answer",
		Citations: []search.Citation{
			{Number: 1, DocumentID: uuid.New(), ChunkID: uuid.New(), CharStart: 0, CharEnd: 10, Text: "text"},
		},
	})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", w.Code, w.Body)
	}
	frames := parseSSE(t, w.Body.String())
	got := decodeFrame[doneEvent](t, frameNamed(t, frames, "done"))
	if got.Citations[0].FileName != "" {
		t.Errorf("citation file_name: got %q, want empty for an unresolvable document", got.Citations[0].FileName)
	}
}

// TestSearch_RetrievedFilesEvent verifies the retrieved_files SSE event
// covers every document retrieval surfaced, ranked, not just the subset
// that ended up cited inline in the summary — and that it arrives before
// the done event.
func TestSearch_RetrievedFilesEvent(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	cited, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: k.ID, UserID: userID, Filename: "cited.txt", ContentType: "text/plain",
	})
	uncited, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: k.ID, UserID: userID, Filename: "uncited.txt", ContentType: "text/plain",
	})

	deps.Searcher = searchmock.NewSearcher(search.Result{
		Summary: "the answer",
		Citations: []search.Citation{
			{Number: 1, DocumentID: cited.ID, ChunkID: uuid.New(), CharStart: 0, CharEnd: 5, Text: "text"},
		},
		// Retrieval surfaced both documents; only cited.ID made it into the answer.
		RetrievedDocuments: []uuid.UUID{cited.ID, uncited.ID},
	})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	frames := parseSSE(t, w.Body.String())
	if frames[0].name != "retrieved_files" {
		t.Fatalf("first frame: got %q, want retrieved_files", frames[0].name)
	}
	got := decodeFrame[retrievedFilesEvent](t, frames[0])
	if len(got.RetrievedFiles) != 2 {
		t.Fatalf("retrieved_files: got %d, want 2 (superset of citations)", len(got.RetrievedFiles))
	}
	if got.RetrievedFiles[0].FileName != "cited.txt" || got.RetrievedFiles[1].FileName != "uncited.txt" {
		t.Errorf("retrieved_files order/content: got %+v", got.RetrievedFiles)
	}

	doneIdx := -1
	for i, f := range frames {
		if f.name == "done" {
			doneIdx = i
		}
	}
	if doneIdx <= 0 {
		t.Fatalf("done frame: got index %d, want after retrieved_files (index 0)", doneIdx)
	}
}

// TestSearch_DeltaEventsCarryGeneratedText verifies that delta events
// forward the generated text chunks, in order, between retrieved_files and
// done.
func TestSearch_DeltaEventsCarryGeneratedText(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")

	searcher := &searchmock.Searcher{
		SearchStreamFn: func(_ context.Context, _ uuid.UUID, _ string, onEvent func(search.StreamEvent)) (search.Result, error) {
			onEvent(search.StreamEvent{Type: search.EventRetrievedFiles})
			onEvent(search.StreamEvent{Type: search.EventDelta, Delta: "Hello, "})
			onEvent(search.StreamEvent{Type: search.EventDelta, Delta: "world."})
			return search.Result{Summary: "Hello, world."}, nil
		},
	}
	deps.Searcher = searcher
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"hi"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	frames := parseSSE(t, w.Body.String())
	var deltas []string
	for _, f := range frames {
		if f.name == "delta" {
			deltas = append(deltas, decodeFrame[deltaEvent](t, f).Text)
		}
	}
	if len(deltas) != 2 || deltas[0] != "Hello, " || deltas[1] != "world." {
		t.Errorf("deltas: got %+v, want [\"Hello, \" \"world.\"]", deltas)
	}
}

func TestSearch_MissingQuery(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

func TestSearch_QueryTooLong(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	router := NewRouter(deps)

	tooLong := strings.Repeat("a", maxQueryBytes+1)
	reqBody, err := json.Marshal(map[string]string{"query": tooLong})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(string(reqBody)), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

func TestSearch_KBNotFound(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+uuid.New().String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", w.Code)
	}
}

// TestSearch_NoSession verifies that a request with no session cookie
// receives a fresh session (middleware never 401s) and returns 404 since
// the KB doesn't belong to the newly-minted session.
func TestSearch_NoSession(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)

	req := unauthRequest(http.MethodPost, "/kbs/"+uuid.New().String()+"/search",
		strings.NewReader(`{"query":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("no-session search: got %d, want 404", w.Code)
	}
}

func TestSearch_TenantIsolation(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	user1, user2 := uuid.New(), uuid.New()

	k, _ := kbRepo.Create(context.TODO(), user1, "kb1")

	// user2 tries to search user1's KB
	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"secret"}`), user2)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant search: got %d, want 404", w.Code)
	}
}

// TestSearch_ServiceError_BeforeAnyEvent_ReturnsNormalErrorStatus verifies
// that a search error occurring before the searcher emits any event (e.g.
// retrieval failing) still gets a plain JSON 500 response — the SSE stream
// is never started for this case, so the status code can still change.
func TestSearch_ServiceError_BeforeAnyEvent_ReturnsNormalErrorStatus(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewErrorSearcher("index unavailable")
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct == "text/event-stream" {
		t.Errorf("Content-Type: got %q, want a normal JSON error response, not SSE", ct)
	}
}

// TestSearch_ServiceError_AfterStreamStarted_SendsErrorEvent verifies that
// an error occurring after at least one event has been flushed can no
// longer change the HTTP status (already committed to 200), so it's
// signaled via an error SSE event inside the still-open stream instead.
func TestSearch_ServiceError_AfterStreamStarted_SendsErrorEvent(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")

	searcher := &searchmock.Searcher{
		SearchStreamFn: func(_ context.Context, _ uuid.UUID, _ string, onEvent func(search.StreamEvent)) (search.Result, error) {
			onEvent(search.StreamEvent{Type: search.EventRetrievedFiles})
			onEvent(search.StreamEvent{Type: search.EventDelta, Delta: "partial answer"})
			return search.Result{}, errFakeGeneration
		},
	}
	deps.Searcher = searcher
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (already committed before the error)", w.Code)
	}
	frames := parseSSE(t, w.Body.String())
	got := decodeFrame[errorEvent](t, frameNamed(t, frames, "error"))
	if got.Error == "" {
		t.Error("expected a non-empty error message in the error event")
	}
}

func TestSearch_RecordsLatencyMetric(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "the answer"})
	inst, metrics := telemetrytest.New(t)
	deps.Instruments = inst
	router := NewRouter(deps)

	body := strings.NewReader(`{"query":"what is the answer?"}`)
	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search", body, userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", w.Code, w.Body)
	}

	if got := metrics.HistogramCount("search_latency_ms"); got != 1 {
		t.Errorf("search_latency_ms count: got %d, want 1", got)
	}
}

func TestSearch_RecordsLatencyMetric_OnError(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewErrorSearcher("index unavailable")
	inst, metrics := telemetrytest.New(t)
	deps.Instruments = inst
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", w.Code)
	}

	if got := metrics.HistogramCount("search_latency_ms"); got != 1 {
		t.Errorf("search_latency_ms count even on error: got %d, want 1", got)
	}
}

// TestSearch_RecordsQueryExecuted verifies a completed search is recorded
// into the durable, cross-tenant usage counters (see internal/stats) —
// distinct from search_latency_ms, which is in-process only and resets on
// restart.
func TestSearch_RecordsQueryExecuted(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	statsRepo := deps.Stats.(*statsmem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewSearcher(search.Result{Summary: "the answer"})
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"what is the answer?"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", w.Code, w.Body)
	}
	snap, err := statsRepo.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if snap.QueriesExecuted != 1 {
		t.Errorf("QueriesExecuted = %d, want 1", snap.QueriesExecuted)
	}
}

// TestSearch_RecordsQueryExecuted_OnError verifies a search that fails still
// counts as an executed query, matching recordSearchLatency's existing
// regardless-of-outcome behavior.
func TestSearch_RecordsQueryExecuted_OnError(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	statsRepo := deps.Stats.(*statsmem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	deps.Searcher = searchmock.NewErrorSearcher("index unavailable")
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/search",
		strings.NewReader(`{"query":"hello"}`), userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", w.Code)
	}
	snap, err := statsRepo.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if snap.QueriesExecuted != 1 {
		t.Errorf("QueriesExecuted = %d, want 1 even on error", snap.QueriesExecuted)
	}
}
