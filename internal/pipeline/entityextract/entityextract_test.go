package entityextract_test

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
	canonicalmem "github.com/kunalpednekar/dumpster/internal/canonical/memory"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkmem "github.com/kunalpednekar/dumpster/internal/chunk/memory"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/entity"
	entitymem "github.com/kunalpednekar/dumpster/internal/entity/memory"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	statusmem "github.com/kunalpednekar/dumpster/internal/jobstatus/memory"
	objmock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/pipeline/entityextract"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// recordingPublisher records the downstream publishes. Other
// queue.Publisher methods are never called and panic via the nil
// embedded interface if they are.
type recordingPublisher struct {
	queue.Publisher
	edges, canon int
	err          error
}

func (p *recordingPublisher) PublishEdgeExtraction(context.Context, queue.EdgeExtractionRequested) error {
	p.edges++
	return p.err
}

func (p *recordingPublisher) PublishCanonicalization(context.Context, queue.CanonicalizationRequested) error {
	p.canon++
	return p.err
}

var allowedTypes = []string{"person", "org"}

type fixture struct {
	t         *testing.T
	ctx       context.Context
	docs      *docmem.Repository
	chunks    *chunkmem.Repository
	entities  *entitymem.Repository
	canonical *canonicalmem.Repository
	objects   *objmock.Store
	status    *statusmem.Store
	publisher *recordingPublisher
	handler   *entityextract.Handler
	doc       *document.Document
	key       jobstatus.Key
}

// newFixture creates a document with nChunks chunks and enqueues attempt 1
// of its entity extraction. The batch size is 2.
func newFixture(t *testing.T, nChunks int) *fixture {
	t.Helper()
	f := &fixture{
		t: t, docs: docmem.New(), chunks: chunkmem.New(), entities: entitymem.New(), canonical: canonicalmem.New(),
		objects: objmock.New(), status: statusmem.New(), publisher: &recordingPublisher{},
	}
	f.handler = entityextract.New(entityextract.Deps{
		Docs: f.docs, Chunks: f.chunks, Entities: f.entities, Canonical: f.canonical,
		Objects: f.objects, Publisher: f.publisher, Status: f.status,
	}, entityextract.Config{AllowedTypes: allowedTypes, BatchSize: 2})

	userID := uuid.New()
	f.ctx = auth.WithUserID(context.Background(), userID)
	doc, err := f.docs.Create(f.ctx, &document.Document{
		KBID: uuid.New(), UserID: userID, Filename: "doc.txt", S3Key: "uploads/x",
		ContentType: "text/plain", Status: document.StatusIndexed,
	})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	f.doc = doc
	var cs []*chunk.Chunk
	for i := 0; i < nChunks; i++ {
		cs = append(cs, &chunk.Chunk{DocumentID: doc.ID, KBID: doc.KBID, UserID: userID, Ordinal: i, Text: "chunk text " + string(rune('a'+i))})
	}
	if err := f.chunks.BulkCreate(f.ctx, cs); err != nil {
		t.Fatalf("create chunks: %v", err)
	}
	f.key = jobstatus.Key{UserID: userID, DocumentID: doc.ID, JobType: queue.JobTypeEntityExtraction}
	if _, err := f.status.Enqueue(f.ctx, f.key); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return f
}

func (f *fixture) input(attempt int) entityextract.Input {
	return entityextract.Input{Type: queue.JobTypeEntityExtraction, DocumentID: f.doc.ID, UserID: f.doc.UserID, Attempt: attempt}
}

func (f *fixture) docChunks() []*chunk.Chunk {
	f.t.Helper()
	cs, err := f.chunks.ListByDocument(f.ctx, f.doc.UserID, f.doc.ID)
	if err != nil {
		f.t.Fatalf("list chunks: %v", err)
	}
	return cs
}

func (f *fixture) docEntities() []*entity.Entity {
	f.t.Helper()
	es, err := f.entities.ListByDocument(f.ctx, f.doc.UserID, f.doc.ID)
	if err != nil {
		f.t.Fatalf("list entities: %v", err)
	}
	return es
}

func (f *fixture) plan(attempt int) entityextract.Plan {
	f.t.Helper()
	p, err := f.handler.Plan(context.Background(), f.input(attempt))
	if err != nil {
		f.t.Fatalf("Plan: %v", err)
	}
	return p
}

type stagedInput struct {
	AllowedTypes []string `json:"allowed_types"`
	Chunks       []struct {
		ChunkID string `json:"chunk_id"`
		Text    string `json:"text"`
	} `json:"chunks"`
}

func (f *fixture) stagedInput(b entityextract.Batch) stagedInput {
	f.t.Helper()
	rc, err := f.objects.Get(f.ctx, b.InputKey)
	if err != nil {
		f.t.Fatalf("batch %d input not staged: %v", b.Index, err)
	}
	var in stagedInput
	if err := json.NewDecoder(rc).Decode(&in); err != nil {
		f.t.Fatalf("decode staged input: %v", err)
	}
	return in
}

// writeResult stands in for the Batch extraction job: one "person" entity
// per chunk ID given.
func (f *fixture) writeResult(b entityextract.Batch, chunkIDs ...string) {
	f.t.Helper()
	var es []map[string]any
	for _, id := range chunkIDs {
		es = append(es, map[string]any{"chunk_id": id, "type": "person", "text": "Ada", "start": 0, "end": 3, "score": 0.9})
	}
	body, _ := json.Marshal(map[string]any{"entities": es})
	if err := f.objects.Put(f.ctx, b.ResultKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		f.t.Fatalf("put result: %v", err)
	}
}

func (f *fixture) exists(key string) bool {
	_, err := f.objects.Get(f.ctx, key)
	return err == nil
}

func (f *fixture) seedEntity() {
	f.t.Helper()
	c := f.docChunks()[0]
	if err := f.entities.BulkCreate(f.ctx, []*entity.Entity{{
		DocumentID: f.doc.ID, KBID: f.doc.KBID, UserID: f.doc.UserID, ChunkID: c.ID, Type: "org", Text: "Old Corp",
	}}); err != nil {
		f.t.Fatalf("seed entity: %v", err)
	}
}

func TestPlan_ClearsPriorEntitiesAndStagesOneInputPerBatch(t *testing.T) {
	f := newFixture(t, 5)
	f.seedEntity()

	p := f.plan(1)

	if n := len(f.docEntities()); n != 0 {
		t.Errorf("%d entities from the previous run remain, want 0", n)
	}
	if p.Stale || p.NoChunks || len(p.Batches) != 3 {
		t.Fatalf("Plan = %+v, want 3 batches for 5 chunks at batch size 2", p)
	}

	chunks := f.docChunks()
	seenKeys := map[string]bool{}
	next := 0
	for i, b := range p.Batches {
		if b.Index != i {
			t.Errorf("batch %d has Index %d", i, b.Index)
		}
		if seenKeys[b.InputKey] || seenKeys[b.ResultKey] || b.InputKey == b.ResultKey {
			t.Errorf("batch %d reuses an object key: %+v", i, b)
		}
		seenKeys[b.InputKey], seenKeys[b.ResultKey] = true, true
		if !strings.Contains(b.InputKey, f.doc.ID.String()+"-1") {
			t.Errorf("batch %d input key %q should name the document and attempt", i, b.InputKey)
		}

		in := f.stagedInput(b)
		if !reflect.DeepEqual(in.AllowedTypes, allowedTypes) {
			t.Errorf("batch %d allowed types = %v, want %v", i, in.AllowedTypes, allowedTypes)
		}
		for _, c := range in.Chunks {
			if c.ChunkID != chunks[next].ID.String() || c.Text != chunks[next].Text {
				t.Errorf("batch %d staged chunk %+v, want chunk %d in order", i, c, next)
			}
			next++
		}
	}
	if next != len(chunks) {
		t.Errorf("staged %d chunks across batches, want all %d", next, len(chunks))
	}

	active, _ := f.status.ActiveJobsForDocuments(f.ctx, f.doc.UserID, []uuid.UUID{f.doc.ID})
	if a := active[f.doc.ID]; len(a) != 1 || a[0].Status != "processing" {
		t.Errorf("job status = %+v, want processing", a)
	}
}

// A Map state needs an array to iterate, even an empty one.
func TestPlan_NoChunks_SerializesAnEmptyBatchList(t *testing.T) {
	f := newFixture(t, 0)
	p := f.plan(1)

	if !p.NoChunks || p.Stale {
		t.Errorf("Plan = %+v, want NoChunks", p)
	}
	body, _ := json.Marshal(p)
	if !strings.Contains(string(body), `"batches":[]`) {
		t.Errorf("Plan JSON = %s, want an empty batches array", body)
	}
}

func TestPlan_SupersededAttempt_IsStaleAndKeepsEntities(t *testing.T) {
	f := newFixture(t, 3)
	f.seedEntity()
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "boom"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if _, err := f.status.Enqueue(f.ctx, f.key); err != nil {
		t.Fatalf("re-Enqueue: %v", err)
	}

	p := f.plan(1)
	body, _ := json.Marshal(p)
	if !p.Stale || len(p.Batches) != 0 || !strings.Contains(string(body), `"batches":[]`) {
		t.Errorf("Plan = %s, want Stale with an empty batches array", body)
	}
	if n := len(f.docEntities()); n != 1 {
		t.Errorf("a stale attempt removed entities: %d left, want 1", n)
	}
}

func TestPlan_DeletedDocument_FailsWithoutRetry(t *testing.T) {
	f := newFixture(t, 3)
	if err := f.docs.Delete(f.ctx, f.doc.UserID, f.doc.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.handler.Plan(context.Background(), f.input(1)); err == nil || pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a non-transient error", err)
	}
}

func TestPresignBatch_ReturnsTheJobNameAndURLs(t *testing.T) {
	f := newFixture(t, 3)
	b := f.plan(1).Batches[1]

	urls, err := f.handler.PresignBatch(context.Background(), f.input(1), b)
	if err != nil {
		t.Fatalf("PresignBatch: %v", err)
	}
	if want := "entity-extraction-" + f.doc.ID.String() + "-1-batch-1"; urls.JobName != want {
		t.Errorf("JobName = %q, want %q", urls.JobName, want)
	}
	if urls.ChunksURL == "" || urls.ResultURL == "" {
		t.Errorf("urls = %+v, want both URLs", urls)
	}
}

func TestPresignBatch_MissingInput_IsTransient(t *testing.T) {
	f := newFixture(t, 3)
	b := f.plan(1).Batches[0]
	if err := f.objects.Delete(f.ctx, b.InputKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.handler.PresignBatch(context.Background(), f.input(1), b); !pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a transient error", err)
	}
}

func TestPersistBatch_SavesTheBatchEntitiesAndIgnoresUnknownChunks(t *testing.T) {
	f := newFixture(t, 3)
	b := f.plan(1).Batches[0]
	staged := f.stagedInput(b)
	f.writeResult(b, staged.Chunks[0].ChunkID, staged.Chunks[1].ChunkID, uuid.New().String())

	if err := f.handler.PersistBatch(context.Background(), f.input(1), b); err != nil {
		t.Fatalf("PersistBatch: %v", err)
	}

	es := f.docEntities()
	if len(es) != 2 {
		t.Fatalf("persisted %d entities, want 2 (the unknown chunk's dropped)", len(es))
	}
	for _, e := range es {
		if e.DocumentID != f.doc.ID || e.KBID != f.doc.KBID || e.UserID != f.doc.UserID || e.Type != "person" || e.Text != "Ada" {
			t.Errorf("entity = %+v, want this document's tenant, KB and the extracted mention", e)
		}
	}
}

func TestPersistBatch_IsSafeToRepeat(t *testing.T) {
	f := newFixture(t, 3)
	b := f.plan(1).Batches[0]
	f.writeResult(b, f.stagedInput(b).Chunks[0].ChunkID)

	for i := 0; i < 2; i++ {
		if err := f.handler.PersistBatch(context.Background(), f.input(1), b); err != nil {
			t.Fatalf("PersistBatch call %d: %v", i+1, err)
		}
	}
	if n := len(f.docEntities()); n != 1 {
		t.Errorf("persisted %d entities after a repeat, want 1", n)
	}
}

func TestPersistBatch_SupersededAttempt_PersistsNothing(t *testing.T) {
	f := newFixture(t, 3)
	b := f.plan(1).Batches[0]
	f.writeResult(b, f.stagedInput(b).Chunks[0].ChunkID)
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "boom"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	if err := f.handler.PersistBatch(context.Background(), f.input(1), b); err != nil {
		t.Fatalf("PersistBatch: %v", err)
	}
	if n := len(f.docEntities()); n != 0 {
		t.Errorf("a finished attempt persisted %d entities, want 0", n)
	}
}

func TestPersistBatch_MissingResult_IsTransient(t *testing.T) {
	f := newFixture(t, 3)
	b := f.plan(1).Batches[0]
	if err := f.handler.PersistBatch(context.Background(), f.input(1), b); !pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a transient error", err)
	}
}

func TestPersistBatch_MalformedResult_FailsWithoutRetry(t *testing.T) {
	f := newFixture(t, 3)
	b := f.plan(1).Batches[0]
	if err := f.objects.Put(f.ctx, b.ResultKey, strings.NewReader("not json"), 8, "application/json"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := f.handler.PersistBatch(context.Background(), f.input(1), b); err == nil || pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a non-transient error", err)
	}
}

func TestFinalize_PublishesDownstreamMarksSucceededAndCleansUp(t *testing.T) {
	f := newFixture(t, 3)
	p := f.plan(1)
	for _, b := range p.Batches {
		f.writeResult(b)
	}

	in := f.input(1)
	in.Plan = &p
	if err := f.handler.Finalize(context.Background(), in); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if f.publisher.edges != 1 || f.publisher.canon != 1 {
		t.Errorf("publishes: edges=%d canonicalization=%d, want 1 each", f.publisher.edges, f.publisher.canon)
	}
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "late"); !errors.Is(err, jobstatus.ErrNotActive) {
		t.Errorf("job should be succeeded; MarkFailed err = %v", err)
	}
	for _, b := range p.Batches {
		if f.exists(b.InputKey) || f.exists(b.ResultKey) {
			t.Errorf("batch %d staged objects not cleaned up", b.Index)
		}
	}
}

func TestFinalize_PublishFailureIsNotFatal(t *testing.T) {
	f := newFixture(t, 3)
	f.publisher.err = errors.New("sqs unavailable")
	p := f.plan(1)
	in := f.input(1)
	in.Plan = &p
	if err := f.handler.Finalize(context.Background(), in); err != nil {
		t.Errorf("Finalize err = %v, want nil: entities are already persisted", err)
	}
}

func TestFinalize_NoChunks_SucceedsWithoutPublishing(t *testing.T) {
	f := newFixture(t, 0)
	p := f.plan(1)
	in := f.input(1)
	in.Plan = &p
	if err := f.handler.Finalize(context.Background(), in); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if f.publisher.edges+f.publisher.canon != 0 {
		t.Errorf("published downstream jobs for a document with no chunks")
	}
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "late"); !errors.Is(err, jobstatus.ErrNotActive) {
		t.Errorf("job should be succeeded; MarkFailed err = %v", err)
	}
}

func TestFinalize_Stale_DoesNothing(t *testing.T) {
	f := newFixture(t, 3)
	in := f.input(1)
	in.Plan = &entityextract.Plan{Stale: true}
	if err := f.handler.Finalize(context.Background(), in); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if f.publisher.edges+f.publisher.canon != 0 {
		t.Error("a stale attempt published downstream jobs")
	}
}

func TestFinalize_WithoutPlan_FailsWithoutRetry(t *testing.T) {
	f := newFixture(t, 3)
	if err := f.handler.Finalize(context.Background(), f.input(1)); err == nil || pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a non-transient error", err)
	}
}

func TestRecordFailure_MarksTheAttemptFailedButNotTheDocument(t *testing.T) {
	f := newFixture(t, 3)
	p := f.plan(1)
	in := f.input(1)
	in.Plan = &p
	in.Error = &pipeline.StepError{Error: "States.TaskFailed", Cause: "CUDA out of memory"}

	if err := f.handler.RecordFailure(context.Background(), in); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	d, _ := f.docs.Get(f.ctx, f.doc.UserID, f.doc.ID)
	if d.Status != document.StatusIndexed {
		t.Errorf("document status = %s, want it untouched (entity extraction doesn't change it)", d.Status)
	}
	if res, err := f.status.Enqueue(f.ctx, f.key); err != nil || !res.Started || res.Attempt != 2 {
		t.Errorf("retry Enqueue = %+v, %v; want attempt 2", res, err)
	}
	if f.exists(p.Batches[0].InputKey) {
		t.Error("staged inputs should be cleaned up after a failure")
	}
}

func TestRecordFailure_SupersededAttempt_IsANoOp(t *testing.T) {
	f := newFixture(t, 3)
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "first"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if _, err := f.status.Enqueue(f.ctx, f.key); err != nil {
		t.Fatalf("re-Enqueue: %v", err)
	}
	in := f.input(1)
	in.Error = &pipeline.StepError{Error: "x", Cause: "late"}
	if err := f.handler.RecordFailure(context.Background(), in); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	active, _ := f.status.ActiveJobsForDocuments(f.ctx, f.doc.UserID, []uuid.UUID{f.doc.ID})
	if a := active[f.doc.ID]; len(a) != 1 || a[0].Status != "pending" {
		t.Errorf("attempt 2 = %+v, want still pending", a)
	}
}

// TestEvent_DecodesAMapIterationPayload checks the JSON contract for the
// payload a Map iteration's Lambda task sends.
func TestEvent_DecodesAMapIterationPayload(t *testing.T) {
	docID, userID := uuid.New(), uuid.New()
	payload := `{"step":"persist_batch","run":{"type":"entity_extraction","document_id":"` + docID.String() +
		`","user_id":"` + userID.String() + `","attempt":3},"batch":{"index":4,"input_key":"i","result_key":"r"}}`

	var ev entityextract.Event
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := entityextract.Event{
		Step:  entityextract.StepPersistBatch,
		Run:   entityextract.Input{Type: queue.JobTypeEntityExtraction, DocumentID: docID, UserID: userID, Attempt: 3},
		Batch: &entityextract.Batch{Index: 4, InputKey: "i", ResultKey: "r"},
	}
	if !reflect.DeepEqual(ev, want) {
		t.Errorf("decoded %+v\nwant    %+v", ev, want)
	}
}

func TestHandle_RoutesEveryStep(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()

	out, err := f.handler.Handle(ctx, entityextract.Event{Step: entityextract.StepPlan, Run: f.input(1)})
	if err != nil {
		t.Fatalf("Handle(plan): %v", err)
	}
	p, ok := out.(entityextract.Plan)
	if !ok || len(p.Batches) != 2 {
		t.Fatalf("Handle(plan) = %#v, want a Plan with 2 batches", out)
	}

	b := p.Batches[0]
	out, err = f.handler.Handle(ctx, entityextract.Event{Step: entityextract.StepPresignBatch, Run: f.input(1), Batch: &b})
	if _, ok := out.(entityextract.BatchURLs); err != nil || !ok {
		t.Fatalf("Handle(presign_batch) = %#v, %v", out, err)
	}

	f.writeResult(b, f.stagedInput(b).Chunks[0].ChunkID)
	if _, err := f.handler.Handle(ctx, entityextract.Event{Step: entityextract.StepPersistBatch, Run: f.input(1), Batch: &b}); err != nil {
		t.Fatalf("Handle(persist_batch): %v", err)
	}

	in := f.input(1)
	in.Plan = &p
	if _, err := f.handler.Handle(ctx, entityextract.Event{Step: entityextract.StepFinalize, Run: in}); err != nil {
		t.Fatalf("Handle(finalize): %v", err)
	}
	in.Error = &pipeline.StepError{Error: "x", Cause: "y"}
	if _, err := f.handler.Handle(ctx, entityextract.Event{Step: entityextract.StepRecordFailure, Run: in}); err != nil {
		t.Fatalf("Handle(record_failure): %v", err)
	}
}

func TestHandle_BatchStepsWithoutABatch_Fail(t *testing.T) {
	f := newFixture(t, 3)
	for _, step := range []string{entityextract.StepPresignBatch, entityextract.StepPersistBatch} {
		if _, err := f.handler.Handle(context.Background(), entityextract.Event{Step: step, Run: f.input(1)}); err == nil {
			t.Errorf("Handle(%s) without a batch: err = nil", step)
		}
	}
}

func TestHandle_UnknownStep(t *testing.T) {
	f := newFixture(t, 3)
	if _, err := f.handler.Handle(context.Background(), entityextract.Event{Step: "explode", Run: f.input(1)}); err == nil {
		t.Error("err = nil, want an error for an unknown step")
	}
}

func TestHandle_TransientErrorsReachTheStateMachineAsTransientError(t *testing.T) {
	f := newFixture(t, 3)
	b := f.plan(1).Batches[0]

	_, err := f.handler.Handle(context.Background(), entityextract.Event{Step: entityextract.StepPersistBatch, Run: f.input(1), Batch: &b})
	var te *pipeline.TransientError
	if !errors.As(err, &te) || reflect.TypeOf(err) != reflect.TypeOf(te) {
		t.Errorf("err = %T %v, want a top-level *pipeline.TransientError", err, err)
	}
}
