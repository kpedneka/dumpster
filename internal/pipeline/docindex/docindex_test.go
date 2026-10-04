package docindex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkmem "github.com/kunalpednekar/dumpster/internal/chunk/memory"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	statusmem "github.com/kunalpednekar/dumpster/internal/jobstatus/memory"
	objmock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/pipeline/docindex"
	"github.com/kunalpednekar/dumpster/internal/queue"
	statsmem "github.com/kunalpednekar/dumpster/internal/stats/memory"
)

// recordingPublisher records entity-extraction publishes. The other
// queue.Publisher methods are never called by docindex and panic if they
// are, via the nil embedded interface.
type recordingPublisher struct {
	queue.Publisher
	entity []queue.EntityExtractionRequested
	err    error
}

func (p *recordingPublisher) PublishEntityExtraction(_ context.Context, evt queue.EntityExtractionRequested) error {
	p.entity = append(p.entity, evt)
	return p.err
}

type fixture struct {
	t         *testing.T
	ctx       context.Context
	docs      *docmem.Repository
	chunks    *chunkmem.Repository
	objects   *objmock.Store
	status    *statusmem.Store
	stats     *statsmem.Repository
	publisher *recordingPublisher
	handler   *docindex.Handler
	doc       *document.Document
	key       jobstatus.Key
}

// longText is long enough for the default splitter (3000-char windows) to
// produce more than one chunk.
var longText = strings.Repeat("The quick brown fox jumps over the lazy dog. ", 150)

func newFixture(t *testing.T, content string) *fixture {
	t.Helper()
	f := &fixture{
		t: t, docs: docmem.New(), chunks: chunkmem.New(), objects: objmock.New(),
		status: statusmem.New(), stats: statsmem.New(), publisher: &recordingPublisher{},
	}
	f.handler = docindex.New(docindex.Deps{
		Docs: f.docs, Objects: f.objects, Chunks: f.chunks, Splitter: chunk.DefaultFixedWindow(),
		Publisher: f.publisher, Status: f.status, Stats: f.stats,
	}, docindex.Config{})

	userID := uuid.New()
	f.ctx = auth.WithUserID(context.Background(), userID)
	s3Key := "uploads/" + uuid.New().String()
	if err := f.objects.Put(f.ctx, s3Key, strings.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		t.Fatalf("put object: %v", err)
	}
	doc, err := f.docs.Create(f.ctx, &document.Document{
		KBID: uuid.New(), UserID: userID, Filename: "doc.txt", S3Key: s3Key,
		ContentType: "text/plain", SizeBytes: int64(len(content)), Status: document.StatusPending,
	})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	f.doc = doc
	f.key = jobstatus.Key{UserID: userID, DocumentID: doc.ID, JobType: queue.JobTypeDocumentIndexing}
	if _, err := f.status.Enqueue(f.ctx, f.key); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return f
}

// input is the execution input dispatch hands the state machine for
// attempt.
func (f *fixture) input(attempt int) docindex.Input {
	return docindex.Input{Type: queue.JobTypeDocumentIndexing, DocumentID: f.doc.ID, UserID: f.doc.UserID, Attempt: attempt}
}

func (f *fixture) docStatus() document.Status {
	f.t.Helper()
	d, err := f.docs.Get(f.ctx, f.doc.UserID, f.doc.ID)
	if err != nil {
		f.t.Fatalf("get document: %v", err)
	}
	return d.Status
}

func (f *fixture) persistedChunks() []*chunk.Chunk {
	f.t.Helper()
	cs, err := f.chunks.ListByDocument(f.ctx, f.doc.UserID, f.doc.ID)
	if err != nil {
		f.t.Fatalf("list chunks: %v", err)
	}
	return cs
}

func (f *fixture) prepare(attempt int) docindex.Prepared {
	f.t.Helper()
	p, err := f.handler.Prepare(context.Background(), f.input(attempt))
	if err != nil {
		f.t.Fatalf("Prepare: %v", err)
	}
	return p
}

// writeBatchResult stands in for the Batch embedding job: one vector per
// staged text, whose first component encodes its position.
func (f *fixture) writeBatchResult(p docindex.Prepared, n int) {
	f.t.Helper()
	vecs := make([][]float32, n)
	for i := range vecs {
		vecs[i] = []float32{float32(i), 0.5}
	}
	body, _ := json.Marshal(map[string]any{"embeddings": vecs, "dims": 2})
	if err := f.objects.Put(f.ctx, p.ResultKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		f.t.Fatalf("put result: %v", err)
	}
}

func (f *fixture) objectExists(key string) bool {
	_, err := f.objects.Get(f.ctx, key)
	return err == nil
}

func TestPrepare_PersistsChunksPublishesEntityExtractionAndStagesEmbedInput(t *testing.T) {
	f := newFixture(t, longText)
	p := f.prepare(1)

	chunks := f.persistedChunks()
	if len(chunks) < 2 {
		t.Fatalf("persisted %d chunks, want at least 2 for this text", len(chunks))
	}
	for _, c := range chunks {
		if c.Embedding != nil {
			t.Errorf("chunk %d persisted with an embedding before the embed job ran", c.Ordinal)
		}
	}
	if !p.Embed || p.Stale || p.AlreadyIndexed || p.ChunkCount != len(chunks) {
		t.Errorf("Prepared = %+v, want Embed with ChunkCount %d", p, len(chunks))
	}
	if !strings.Contains(p.JobName, f.doc.ID.String()) || !strings.HasSuffix(p.JobName, "-1") {
		t.Errorf("JobName = %q, want it to name the document and attempt 1", p.JobName)
	}
	if p.TextsURL == "" || p.ResultURL == "" {
		t.Errorf("Prepared = %+v, want presigned texts and result URLs", p)
	}

	rc, err := f.objects.Get(f.ctx, p.InputKey)
	if err != nil {
		t.Fatalf("embed input not staged at %q: %v", p.InputKey, err)
	}
	var staged struct{ Texts []string }
	if err := json.NewDecoder(rc).Decode(&staged); err != nil {
		t.Fatalf("decode staged input: %v", err)
	}
	if len(staged.Texts) != len(chunks) || staged.Texts[0] != chunks[0].Text {
		t.Errorf("staged %d texts, want the %d chunk texts in ordinal order", len(staged.Texts), len(chunks))
	}

	if got := f.docStatus(); got != document.StatusProcessing {
		t.Errorf("document status = %s, want processing", got)
	}
	active, _ := f.status.ActiveJobsForDocuments(f.ctx, f.doc.UserID, []uuid.UUID{f.doc.ID})
	if a := active[f.doc.ID]; len(a) != 1 || a[0].Status != "processing" {
		t.Errorf("job status = %+v, want processing", a)
	}
	if len(f.publisher.entity) != 1 || f.publisher.entity[0].DocumentID != f.doc.ID {
		t.Errorf("entity extraction publishes = %+v, want one for this document", f.publisher.entity)
	}
}

func TestPrepare_EntityExtractionPublishFailureIsNotFatal(t *testing.T) {
	f := newFixture(t, longText)
	f.publisher.err = errors.New("sqs unavailable")

	if p := f.prepare(1); !p.Embed {
		t.Errorf("Prepared = %+v, want indexing to continue", p)
	}
}

func TestPrepare_EmptyDocument_SkipsEmbeddingButStillPublishes(t *testing.T) {
	f := newFixture(t, "")
	p := f.prepare(1)

	if p.Embed || p.Stale || p.AlreadyIndexed {
		t.Errorf("Prepared = %+v, want nothing to embed", p)
	}
	if n := len(f.persistedChunks()); n != 0 {
		t.Errorf("persisted %d chunks, want 0", n)
	}
	if len(f.publisher.entity) != 1 {
		t.Errorf("entity extraction publishes = %d, want 1", len(f.publisher.entity))
	}
}

func TestPrepare_AlreadyIndexed_DoesNoWork(t *testing.T) {
	f := newFixture(t, longText)
	if err := f.docs.UpdateStatus(f.ctx, f.doc.UserID, f.doc.ID, document.StatusIndexed); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	p := f.prepare(1)

	if !p.AlreadyIndexed || p.Embed {
		t.Errorf("Prepared = %+v, want AlreadyIndexed and nothing to embed", p)
	}
	if n := len(f.persistedChunks()); n != 0 || len(f.publisher.entity) != 0 {
		t.Errorf("did work for an indexed document: %d chunks, %d publishes", n, len(f.publisher.entity))
	}
}

func TestPrepare_SupersededAttempt_IsStaleAndTouchesNothing(t *testing.T) {
	f := newFixture(t, longText)
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "boom"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if _, err := f.status.Enqueue(f.ctx, f.key); err != nil {
		t.Fatalf("re-Enqueue: %v", err)
	}

	p := f.prepare(1)
	if !p.Stale || p.Embed {
		t.Errorf("Prepared = %+v, want Stale", p)
	}
	if got := f.docStatus(); got != document.StatusPending {
		t.Errorf("document status = %s, want it untouched (pending)", got)
	}
}

func TestPrepare_DeletedDocument_FailsWithoutRetry(t *testing.T) {
	f := newFixture(t, longText)
	if err := f.docs.Delete(f.ctx, f.doc.UserID, f.doc.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := f.handler.Prepare(context.Background(), f.input(1))
	if err == nil || pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a non-transient error", err)
	}
}

func TestPrepare_UnreadableObject_IsTransient(t *testing.T) {
	f := newFixture(t, longText)
	if err := f.objects.Delete(f.ctx, f.doc.S3Key); err != nil {
		t.Fatalf("Delete object: %v", err)
	}

	_, err := f.handler.Prepare(context.Background(), f.input(1))
	if !pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a transient error", err)
	}
}

func TestFinalize_BackfillsEmbeddingsAndMarksIndexed(t *testing.T) {
	f := newFixture(t, longText)
	p := f.prepare(1)
	f.writeBatchResult(p, p.ChunkCount)

	in := f.input(1)
	in.Prepare = &p
	if err := f.handler.Finalize(context.Background(), in); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	for i, c := range f.persistedChunks() {
		if len(c.Embedding) != 2 || c.Embedding[0] != float32(i) {
			t.Errorf("chunk %d embedding = %v, want the %dth vector", c.Ordinal, c.Embedding, i)
		}
	}
	if got := f.docStatus(); got != document.StatusIndexed {
		t.Errorf("document status = %s, want indexed", got)
	}
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "late"); !errors.Is(err, jobstatus.ErrNotActive) {
		t.Errorf("job should be succeeded; MarkFailed err = %v, want ErrNotActive", err)
	}
	if snap, _ := f.stats.Get(f.ctx); snap.DocumentsIndexed != 1 {
		t.Errorf("DocumentsIndexed = %d, want 1", snap.DocumentsIndexed)
	}
	if f.objectExists(p.InputKey) || f.objectExists(p.ResultKey) {
		t.Error("staged embed input/result objects should be deleted after finalizing")
	}
}

func TestFinalize_WrongEmbeddingCount_FailsWithoutRetry(t *testing.T) {
	f := newFixture(t, longText)
	p := f.prepare(1)
	f.writeBatchResult(p, p.ChunkCount-1)

	in := f.input(1)
	in.Prepare = &p
	err := f.handler.Finalize(context.Background(), in)
	if err == nil || pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a non-transient error", err)
	}
}

func TestFinalize_MissingResult_IsTransient(t *testing.T) {
	f := newFixture(t, longText)
	p := f.prepare(1)

	in := f.input(1)
	in.Prepare = &p
	if err := f.handler.Finalize(context.Background(), in); !pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a transient error", err)
	}
}

func TestFinalize_NothingToEmbed_MarksIndexed(t *testing.T) {
	f := newFixture(t, "")
	p := f.prepare(1)

	in := f.input(1)
	in.Prepare = &p
	if err := f.handler.Finalize(context.Background(), in); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if got := f.docStatus(); got != document.StatusIndexed {
		t.Errorf("document status = %s, want indexed", got)
	}
}

func TestFinalize_AlreadyIndexed_SucceedsWithoutCountingAgain(t *testing.T) {
	f := newFixture(t, longText)
	if err := f.docs.UpdateStatus(f.ctx, f.doc.UserID, f.doc.ID, document.StatusIndexed); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	p := f.prepare(1)

	in := f.input(1)
	in.Prepare = &p
	if err := f.handler.Finalize(context.Background(), in); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "late"); !errors.Is(err, jobstatus.ErrNotActive) {
		t.Errorf("job should be succeeded; MarkFailed err = %v", err)
	}
	if snap, _ := f.stats.Get(f.ctx); snap.DocumentsIndexed != 0 {
		t.Errorf("DocumentsIndexed = %d, want 0 for a document indexed before", snap.DocumentsIndexed)
	}
}

func TestFinalize_Stale_TouchesNothing(t *testing.T) {
	f := newFixture(t, longText)
	in := f.input(1)
	in.Prepare = &docindex.Prepared{Stale: true}

	if err := f.handler.Finalize(context.Background(), in); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if got := f.docStatus(); got != document.StatusPending {
		t.Errorf("document status = %s, want untouched", got)
	}
}

func TestFinalize_WithoutPrepareOutput_FailsWithoutRetry(t *testing.T) {
	f := newFixture(t, longText)
	err := f.handler.Finalize(context.Background(), f.input(1))
	if err == nil || pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a non-transient error", err)
	}
}

func TestRecordFailure_MarksDocumentAndAttemptFailedAndAllowsRetry(t *testing.T) {
	f := newFixture(t, longText)
	p := f.prepare(1)

	in := f.input(1)
	in.Prepare = &p
	in.Error = &docindex.StepError{Error: "States.TaskFailed", Cause: "Essential container in task exited"}
	if err := f.handler.RecordFailure(context.Background(), in); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	if got := f.docStatus(); got != document.StatusFailed {
		t.Errorf("document status = %s, want failed", got)
	}
	res, err := f.status.Enqueue(f.ctx, f.key)
	if err != nil || !res.Started || res.Attempt != 2 {
		t.Errorf("retry Enqueue = %+v, %v; want attempt 2 to start", res, err)
	}
	if f.objectExists(p.InputKey) {
		t.Error("staged embed input should be cleaned up after a failure")
	}
}

func TestRecordFailure_SupersededAttempt_LeavesDocumentAlone(t *testing.T) {
	f := newFixture(t, longText)
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "first"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if _, err := f.status.Enqueue(f.ctx, f.key); err != nil {
		t.Fatalf("re-Enqueue: %v", err)
	}

	in := f.input(1)
	in.Error = &docindex.StepError{Error: "States.TaskFailed", Cause: "late"}
	if err := f.handler.RecordFailure(context.Background(), in); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if got := f.docStatus(); got != document.StatusPending {
		t.Errorf("document status = %s, want untouched while attempt 2 runs", got)
	}
}

func TestRecordFailure_DeletedDocument_Succeeds(t *testing.T) {
	f := newFixture(t, longText)
	if err := f.docs.Delete(f.ctx, f.doc.UserID, f.doc.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	in := f.input(1)
	in.Error = &docindex.StepError{Error: "TransientError", Cause: "gave up"}
	if err := f.handler.RecordFailure(context.Background(), in); err != nil {
		t.Errorf("RecordFailure err = %v, want nil when the document is already gone", err)
	}
}

// TestEvent_DecodesTheStateMachinePayload checks the JSON contract
// between the state machine definition and the handler: the payload a
// Task state sends after Prepare has run and a Catch has fired.
func TestEvent_DecodesTheStateMachinePayload(t *testing.T) {
	docID, userID := uuid.New(), uuid.New()
	payload := `{"step":"record_failure","run":{"type":"document_indexing","document_id":"` + docID.String() +
		`","user_id":"` + userID.String() + `","attempt":2,` +
		`"prepare":{"embed":true,"chunk_count":3,"job_name":"j","texts_url":"t","result_url":"r","input_key":"i","result_key":"k"},` +
		`"error":{"Error":"States.TaskFailed","Cause":"boom"}}}`

	var ev docindex.Event
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := docindex.Event{Step: docindex.StepRecordFailure, Run: docindex.Input{
		Type: queue.JobTypeDocumentIndexing, DocumentID: docID, UserID: userID, Attempt: 2,
		Prepare: &docindex.Prepared{Embed: true, ChunkCount: 3, JobName: "j", TextsURL: "t", ResultURL: "r", InputKey: "i", ResultKey: "k"},
		Error:   &docindex.StepError{Error: "States.TaskFailed", Cause: "boom"},
	}}
	if !reflect.DeepEqual(ev, want) {
		t.Errorf("decoded %+v\nwant    %+v", ev, want)
	}
}

func TestHandle_RoutesEachStep(t *testing.T) {
	f := newFixture(t, longText)

	out, err := f.handler.Handle(context.Background(), docindex.Event{Step: docindex.StepPrepare, Run: f.input(1)})
	if err != nil {
		t.Fatalf("Handle(prepare): %v", err)
	}
	p, ok := out.(docindex.Prepared)
	if !ok || !p.Embed {
		t.Fatalf("Handle(prepare) = %#v, want a Prepared with Embed", out)
	}
	f.writeBatchResult(p, p.ChunkCount)

	in := f.input(1)
	in.Prepare = &p
	if _, err := f.handler.Handle(context.Background(), docindex.Event{Step: docindex.StepFinalize, Run: in}); err != nil {
		t.Fatalf("Handle(finalize): %v", err)
	}
	if got := f.docStatus(); got != document.StatusIndexed {
		t.Errorf("document status = %s, want indexed", got)
	}

	in.Error = &docindex.StepError{Error: "x", Cause: "y"}
	if _, err := f.handler.Handle(context.Background(), docindex.Event{Step: docindex.StepRecordFailure, Run: in}); err != nil {
		t.Errorf("Handle(record_failure): %v", err)
	}
}

func TestHandle_UnknownStep(t *testing.T) {
	f := newFixture(t, longText)
	if _, err := f.handler.Handle(context.Background(), docindex.Event{Step: "explode", Run: f.input(1)}); err == nil {
		t.Error("Handle err = nil, want an error for an unknown step")
	}
}

func TestHandle_TransientErrorsReachTheStateMachineAsTransientError(t *testing.T) {
	f := newFixture(t, longText)
	if err := f.objects.Delete(f.ctx, f.doc.S3Key); err != nil {
		t.Fatalf("Delete object: %v", err)
	}

	_, err := f.handler.Handle(context.Background(), docindex.Event{Step: docindex.StepPrepare, Run: f.input(1)})
	var te *pipeline.TransientError
	if !errors.As(err, &te) || reflect.TypeOf(err) != reflect.TypeOf(te) {
		t.Errorf("err = %T %v, want a top-level *pipeline.TransientError", err, err)
	}
}
