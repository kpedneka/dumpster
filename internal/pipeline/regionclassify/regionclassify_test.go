package regionclassify_test

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
	"github.com/kunalpednekar/dumpster/internal/manifest"
	manifestmem "github.com/kunalpednekar/dumpster/internal/manifest/memory"
	objmock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/pipeline/regionclassify"
	"github.com/kunalpednekar/dumpster/internal/queue"
	statsmem "github.com/kunalpednekar/dumpster/internal/stats/memory"
)

// recordingPublisher records entity-extraction publishes; other methods
// panic via the nil embedded interface if called.
type recordingPublisher struct {
	queue.Publisher
	entity int
}

func (p *recordingPublisher) PublishEntityExtraction(context.Context, queue.EntityExtractionRequested) error {
	p.entity++
	return nil
}

type fixture struct {
	t         *testing.T
	ctx       context.Context
	docs      *docmem.Repository
	chunks    *chunkmem.Repository
	manifest  *manifestmem.Repository
	uploads   *objmock.Store
	scratch   *objmock.Store
	status    *statusmem.Store
	stats     *statsmem.Repository
	publisher *recordingPublisher
	handler   *regionclassify.Handler
	doc       *document.Document
	key       jobstatus.Key
}

func newFixture(t *testing.T, contentType string) *fixture {
	t.Helper()
	f := &fixture{
		t: t, docs: docmem.New(), chunks: chunkmem.New(), manifest: manifestmem.New(),
		uploads: objmock.New(), scratch: objmock.New(), status: statusmem.New(), stats: statsmem.New(),
		publisher: &recordingPublisher{},
	}
	f.handler = regionclassify.New(regionclassify.Deps{
		Docs: f.docs, Uploads: f.uploads, Scratch: f.scratch, Chunks: f.chunks, Manifest: f.manifest,
		Splitter: chunk.DefaultFixedWindow(), Publisher: f.publisher, Status: f.status, Stats: f.stats,
	}, regionclassify.Config{})

	userID := uuid.New()
	f.ctx = auth.WithUserID(context.Background(), userID)
	s3Key := "uploads/" + uuid.New().String()
	if err := f.uploads.Put(f.ctx, s3Key, strings.NewReader("%PDF-1.7 bytes"), 14, contentType); err != nil {
		t.Fatalf("put upload: %v", err)
	}
	doc, err := f.docs.Create(f.ctx, &document.Document{
		KBID: uuid.New(), UserID: userID, Filename: "file", S3Key: s3Key,
		ContentType: contentType, SizeBytes: 14, Status: document.StatusPending,
	})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	f.doc = doc
	f.key = jobstatus.Key{UserID: userID, DocumentID: doc.ID, JobType: queue.JobTypeRegionClassification}
	if _, err := f.status.Enqueue(f.ctx, f.key); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return f
}

func (f *fixture) input(attempt int) regionclassify.Input {
	return regionclassify.Input{Type: queue.JobTypeRegionClassification, DocumentID: f.doc.ID, UserID: f.doc.UserID, Attempt: attempt}
}

func (f *fixture) docStatus() document.Status {
	f.t.Helper()
	d, err := f.docs.Get(f.ctx, f.doc.UserID, f.doc.ID)
	if err != nil {
		f.t.Fatalf("get document: %v", err)
	}
	return d.Status
}

func (f *fixture) activeJob() []queue.JobStatus {
	got, _ := f.status.ActiveJobsForDocuments(f.ctx, f.doc.UserID, []uuid.UUID{f.doc.ID})
	return got[f.doc.ID]
}

func (f *fixture) stageLayout(attempt int) regionclassify.Layout {
	f.t.Helper()
	l, err := f.handler.StageLayout(context.Background(), f.input(attempt))
	if err != nil {
		f.t.Fatalf("StageLayout: %v", err)
	}
	return l
}

// writeLayoutResult stands in for the layout Batch job, using
// scripts/batch_regions_job.py's result shape.
func (f *fixture) writeLayoutResult(l regionclassify.Layout, regions []map[string]any) {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"regions": regions, "peak_rss_kb": 1})
	if err := f.scratch.Put(f.ctx, l.ResultKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		f.t.Fatalf("put layout result: %v", err)
	}
}

// sampleRegions covers every resolution path: indexed text and table,
// a figure, a suspected scan, and a text region with no text.
var sampleRegions = []map[string]any{
	{"region_type": "native_text", "page_number": 1, "bbox": []float64{0, 0, 1, 0.5}, "text": "Ada Lovelace wrote the first program.", "needs_vlm": ""},
	{"region_type": "figure", "page_number": 1, "bbox": []float64{0, 0.5, 1, 1}, "image_base64": "aGk=", "needs_vlm": "describe"},
	{"region_type": "native_table", "page_number": 2, "bbox": []float64{0, 0, 1, 0.3}, "text": "Year | Event", "needs_vlm": ""},
	{"region_type": "unconfirmed_table", "page_number": 2, "bbox": []float64{0, 0.3, 1, 0.6}, "needs_vlm": "confirm_scan"},
	{"region_type": "native_text", "page_number": 3, "bbox": []float64{0, 0, 1, 1}, "text": "", "needs_vlm": ""},
}

func (f *fixture) buildChunks(attempt int, l regionclassify.Layout) regionclassify.Prepared {
	f.t.Helper()
	in := f.input(attempt)
	in.Layout = &l
	p, err := f.handler.BuildChunks(context.Background(), in)
	if err != nil {
		f.t.Fatalf("BuildChunks: %v", err)
	}
	return p
}

func TestStageLayout_PDF_PresignsTheOriginalUpload(t *testing.T) {
	f := newFixture(t, "application/pdf")
	l := f.stageLayout(1)

	if !l.NeedsLayout || l.Stale || l.AlreadyIndexed {
		t.Fatalf("Layout = %+v, want NeedsLayout", l)
	}
	if l.PDFURL != "http://mock/"+f.doc.S3Key {
		t.Errorf("PDFURL = %q, want a presigned URL for the original upload", l.PDFURL)
	}
	if l.ResultURL == "" || !strings.Contains(l.ResultKey, f.doc.ID.String()+"-1") {
		t.Errorf("Layout = %+v, want a result URL and a key naming the document and attempt", l)
	}
	if want := "region-extraction-" + f.doc.ID.String() + "-1"; l.JobName != want {
		t.Errorf("JobName = %q, want %q", l.JobName, want)
	}
	if f.docStatus() != document.StatusProcessing {
		t.Errorf("document status = %s, want processing", f.docStatus())
	}
	if a := f.activeJob(); len(a) != 1 || a[0].Status != "processing" || a[0].Phase != "" {
		t.Errorf("job status = %+v, want processing with no phase (Analyzing)", a)
	}
}

func TestStageLayout_Image_SkipsTheLayoutJob(t *testing.T) {
	f := newFixture(t, "image/png")
	if l := f.stageLayout(1); l.NeedsLayout || l.PDFURL != "" {
		t.Errorf("Layout = %+v, want no layout job for an image", l)
	}
}

func TestStageLayout_AlreadyIndexed(t *testing.T) {
	f := newFixture(t, "application/pdf")
	if err := f.docs.UpdateStatus(f.ctx, f.doc.UserID, f.doc.ID, document.StatusIndexed); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if l := f.stageLayout(1); !l.AlreadyIndexed || l.NeedsLayout {
		t.Errorf("Layout = %+v, want AlreadyIndexed without a layout job", l)
	}
}

func TestStageLayout_SupersededAttempt_IsStale(t *testing.T) {
	f := newFixture(t, "application/pdf")
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "boom"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if l := f.stageLayout(1); !l.Stale || l.NeedsLayout {
		t.Errorf("Layout = %+v, want Stale", l)
	}
	if f.docStatus() != document.StatusPending {
		t.Errorf("document status = %s, want untouched", f.docStatus())
	}
}

func TestStageLayout_DeletedDocument_FailsWithoutRetry(t *testing.T) {
	f := newFixture(t, "application/pdf")
	if err := f.docs.Delete(f.ctx, f.doc.UserID, f.doc.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.handler.StageLayout(context.Background(), f.input(1)); err == nil || pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a non-transient error", err)
	}
}

func TestBuildChunks_ResolvesRegionsBuildsChunksAndStagesEmbedding(t *testing.T) {
	f := newFixture(t, "application/pdf")
	l := f.stageLayout(1)
	f.writeLayoutResult(l, sampleRegions)

	p := f.buildChunks(1, l)

	regions, err := f.manifest.ListByDocument(f.ctx, f.doc.UserID, f.doc.ID)
	if err != nil {
		t.Fatalf("list manifest: %v", err)
	}
	want := []struct {
		t manifest.RegionType
		s manifest.Status
	}{
		{manifest.RegionTypeNativeText, manifest.StatusIndexed},
		{manifest.RegionTypeFigure, manifest.StatusSkipped},
		{manifest.RegionTypeNativeTable, manifest.StatusIndexed},
		{manifest.RegionTypeScannedTable, manifest.StatusSkipped},
		{manifest.RegionTypeNativeText, manifest.StatusSkipped},
	}
	if len(regions) != len(want) {
		t.Fatalf("manifest has %d regions, want %d", len(regions), len(want))
	}
	for i, r := range regions {
		if r.RegionType != want[i].t || r.Status != want[i].s || r.ExtractorVersion == "" {
			t.Errorf("region %d = %s/%s (version %q), want %s/%s", i, r.RegionType, r.Status, r.ExtractorVersion, want[i].t, want[i].s)
		}
	}

	chunks, _ := f.chunks.ListByDocument(f.ctx, f.doc.UserID, f.doc.ID)
	if len(chunks) != 2 {
		t.Fatalf("persisted %d chunks, want 2 (one per indexed region)", len(chunks))
	}
	for i, c := range chunks {
		if c.Ordinal != i || c.RegionID == nil || c.PageNumber == nil || c.BoundingBox == nil || c.Embedding != nil {
			t.Errorf("chunk %d = %+v, want ordinal %d, region, page and box, and no embedding yet", i, c, i)
		}
	}
	if *chunks[0].RegionID != regions[0].ID || *chunks[1].RegionID != regions[2].ID || *chunks[1].PageNumber != 2 {
		t.Errorf("chunks aren't linked to their source regions")
	}

	if f.publisher.entity != 1 {
		t.Errorf("entity extraction publishes = %d, want 1", f.publisher.entity)
	}
	if a := f.activeJob(); len(a) != 1 || a[0].Phase != queue.PhaseEmbedding {
		t.Errorf("job status = %+v, want phase %q (the checklist's Embedding stage)", a, queue.PhaseEmbedding)
	}
	if !p.Embed || p.ChunkCount != 2 || p.TextsURL == "" {
		t.Errorf("Prepared = %+v, want 2 chunks staged for embedding", p)
	}
	if _, err := f.scratch.Get(f.ctx, p.InputKey); err != nil {
		t.Errorf("embed input not staged in scratch: %v", err)
	}
}

func TestBuildChunks_Image_IsOneSkippedFigureAndNothingToEmbed(t *testing.T) {
	f := newFixture(t, "image/jpeg")
	p := f.buildChunks(1, f.stageLayout(1))

	regions, _ := f.manifest.ListByDocument(f.ctx, f.doc.UserID, f.doc.ID)
	if len(regions) != 1 || regions[0].RegionType != manifest.RegionTypeFigure || regions[0].Status != manifest.StatusSkipped {
		t.Errorf("manifest = %+v, want one skipped figure", regions)
	}
	if p.Embed {
		t.Errorf("Prepared = %+v, want nothing to embed", p)
	}
	if f.publisher.entity != 1 {
		t.Errorf("entity extraction publishes = %d, want 1", f.publisher.entity)
	}
}

func TestBuildChunks_IsSafeToRepeat(t *testing.T) {
	f := newFixture(t, "application/pdf")
	l := f.stageLayout(1)
	f.writeLayoutResult(l, sampleRegions)
	f.buildChunks(1, l)
	f.buildChunks(1, l)

	regions, _ := f.manifest.ListByDocument(f.ctx, f.doc.UserID, f.doc.ID)
	chunks, _ := f.chunks.ListByDocument(f.ctx, f.doc.UserID, f.doc.ID)
	if len(regions) != 5 || len(chunks) != 2 {
		t.Errorf("after a repeat: %d regions, %d chunks; want 5 and 2", len(regions), len(chunks))
	}
}

func TestBuildChunks_PassesThroughStaleAndAlreadyIndexed(t *testing.T) {
	f := newFixture(t, "application/pdf")
	if p := f.buildChunks(1, regionclassify.Layout{Stale: true}); !p.Stale {
		t.Errorf("Prepared = %+v, want Stale", p)
	}
	if p := f.buildChunks(1, regionclassify.Layout{AlreadyIndexed: true}); !p.AlreadyIndexed {
		t.Errorf("Prepared = %+v, want AlreadyIndexed", p)
	}
}

func TestBuildChunks_SupersededMeanwhile_WritesNothing(t *testing.T) {
	f := newFixture(t, "application/pdf")
	l := f.stageLayout(1)
	f.writeLayoutResult(l, sampleRegions)
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "user gave up"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	if p := f.buildChunks(1, l); !p.Stale {
		t.Errorf("Prepared = %+v, want Stale", p)
	}
	if regions, _ := f.manifest.ListByDocument(f.ctx, f.doc.UserID, f.doc.ID); len(regions) != 0 {
		t.Errorf("a stale attempt wrote %d manifest regions", len(regions))
	}
}

func TestBuildChunks_MissingLayoutResult_IsTransient(t *testing.T) {
	f := newFixture(t, "application/pdf")
	in := f.input(1)
	l := f.stageLayout(1)
	in.Layout = &l
	if _, err := f.handler.BuildChunks(context.Background(), in); !pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a transient error", err)
	}
}

func TestBuildChunks_MalformedLayoutResult_FailsWithoutRetry(t *testing.T) {
	f := newFixture(t, "application/pdf")
	l := f.stageLayout(1)
	if err := f.scratch.Put(f.ctx, l.ResultKey, strings.NewReader("nope"), 4, "application/json"); err != nil {
		t.Fatalf("put: %v", err)
	}
	in := f.input(1)
	in.Layout = &l
	if _, err := f.handler.BuildChunks(context.Background(), in); err == nil || pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a non-transient error", err)
	}
}

func TestBuildChunks_WithoutLayoutResult_FailsWithoutRetry(t *testing.T) {
	f := newFixture(t, "application/pdf")
	if _, err := f.handler.BuildChunks(context.Background(), f.input(1)); err == nil || pipeline.IsTransient(err) {
		t.Errorf("err = %v, want a non-transient error", err)
	}
}

func TestFinalize_BackfillsMarksIndexedAndCleansUp(t *testing.T) {
	f := newFixture(t, "application/pdf")
	l := f.stageLayout(1)
	f.writeLayoutResult(l, sampleRegions)
	p := f.buildChunks(1, l)
	body, _ := json.Marshal(map[string]any{"embeddings": [][]float32{{1}, {2}}})
	if err := f.scratch.Put(f.ctx, p.ResultKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		t.Fatalf("put embed result: %v", err)
	}

	in := f.input(1)
	in.Layout, in.Prepare = &l, &p
	if err := f.handler.Finalize(context.Background(), in); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	chunks, _ := f.chunks.ListByDocument(f.ctx, f.doc.UserID, f.doc.ID)
	if chunks[0].Embedding[0] != 1 || chunks[1].Embedding[0] != 2 {
		t.Errorf("embeddings not backfilled in order")
	}
	if f.docStatus() != document.StatusIndexed {
		t.Errorf("document status = %s, want indexed", f.docStatus())
	}
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "late"); !errors.Is(err, jobstatus.ErrNotActive) {
		t.Errorf("job should be succeeded; MarkFailed err = %v", err)
	}
	for _, key := range []string{l.ResultKey, p.InputKey, p.ResultKey} {
		if _, err := f.scratch.Get(f.ctx, key); err == nil {
			t.Errorf("scratch object %s not cleaned up", key)
		}
	}
}

func TestFinalize_Image_MarksIndexed(t *testing.T) {
	f := newFixture(t, "image/png")
	l := f.stageLayout(1)
	p := f.buildChunks(1, l)
	in := f.input(1)
	in.Layout, in.Prepare = &l, &p
	if err := f.handler.Finalize(context.Background(), in); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if f.docStatus() != document.StatusIndexed {
		t.Errorf("document status = %s, want indexed", f.docStatus())
	}
}

func TestFinalize_StaleOrMissingPrepare(t *testing.T) {
	f := newFixture(t, "application/pdf")
	in := f.input(1)
	in.Prepare = &regionclassify.Prepared{Stale: true}
	if err := f.handler.Finalize(context.Background(), in); err != nil {
		t.Errorf("Finalize(stale) = %v, want nil", err)
	}
	in.Prepare = nil
	if err := f.handler.Finalize(context.Background(), in); err == nil || pipeline.IsTransient(err) {
		t.Errorf("Finalize(no prepare) = %v, want a non-transient error", err)
	}
}

func TestRecordFailure_MarksDocumentFailedAndCleansUpTheLayoutResult(t *testing.T) {
	f := newFixture(t, "application/pdf")
	l := f.stageLayout(1)
	f.writeLayoutResult(l, sampleRegions)

	in := f.input(1)
	in.Layout = &l
	in.Error = &pipeline.StepError{Error: "States.TaskFailed", Cause: "pdfplumber: encrypted PDF"}
	if err := f.handler.RecordFailure(context.Background(), in); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if f.docStatus() != document.StatusFailed {
		t.Errorf("document status = %s, want failed", f.docStatus())
	}
	if _, err := f.scratch.Get(f.ctx, l.ResultKey); err == nil {
		t.Error("layout result not cleaned up")
	}
	if res, err := f.status.Enqueue(f.ctx, f.key); err != nil || res.Attempt != 2 {
		t.Errorf("retry Enqueue = %+v, %v; want attempt 2", res, err)
	}
}

func TestEvent_DecodesTheStateMachinePayload(t *testing.T) {
	docID, userID := uuid.New(), uuid.New()
	payload := `{"step":"finalize","run":{"type":"region_classification","document_id":"` + docID.String() +
		`","user_id":"` + userID.String() + `","attempt":1,` +
		`"layout":{"needs_layout":true,"job_name":"j","pdf_url":"p","result_url":"r","result_key":"k"},` +
		`"prepare":{"embed":true,"chunk_count":2,"job_name":"e","texts_url":"t","result_url":"u","input_key":"i","result_key":"o"}}}`

	var ev regionclassify.Event
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := regionclassify.Event{Step: regionclassify.StepFinalize, Run: regionclassify.Input{
		Type: queue.JobTypeRegionClassification, DocumentID: docID, UserID: userID, Attempt: 1,
		Layout:  &regionclassify.Layout{NeedsLayout: true, JobName: "j", PDFURL: "p", ResultURL: "r", ResultKey: "k"},
		Prepare: &regionclassify.Prepared{Embed: true, ChunkCount: 2, JobName: "e", TextsURL: "t", ResultURL: "u", InputKey: "i", ResultKey: "o"},
	}}
	if !reflect.DeepEqual(ev, want) {
		t.Errorf("decoded %+v\nwant    %+v", ev, want)
	}
}

func TestHandle_RoutesEveryStep(t *testing.T) {
	f := newFixture(t, "image/png")
	ctx := context.Background()

	out, err := f.handler.Handle(ctx, regionclassify.Event{Step: regionclassify.StepStageLayout, Run: f.input(1)})
	l, ok := out.(regionclassify.Layout)
	if err != nil || !ok {
		t.Fatalf("Handle(stage_layout) = %#v, %v", out, err)
	}
	in := f.input(1)
	in.Layout = &l
	out, err = f.handler.Handle(ctx, regionclassify.Event{Step: regionclassify.StepBuildChunks, Run: in})
	p, ok := out.(regionclassify.Prepared)
	if err != nil || !ok {
		t.Fatalf("Handle(build_chunks) = %#v, %v", out, err)
	}
	in.Prepare = &p
	if _, err := f.handler.Handle(ctx, regionclassify.Event{Step: regionclassify.StepFinalize, Run: in}); err != nil {
		t.Fatalf("Handle(finalize): %v", err)
	}
	in.Error = &pipeline.StepError{Error: "x", Cause: "y"}
	if _, err := f.handler.Handle(ctx, regionclassify.Event{Step: regionclassify.StepRecordFailure, Run: in}); err != nil {
		t.Fatalf("Handle(record_failure): %v", err)
	}
	if _, err := f.handler.Handle(ctx, regionclassify.Event{Step: "explode", Run: in}); err == nil {
		t.Error("Handle(unknown step) err = nil")
	}
}

func TestHandle_TransientErrorsReachTheStateMachineAsTransientError(t *testing.T) {
	f := newFixture(t, "application/pdf")
	in := f.input(1)
	l := f.stageLayout(1)
	in.Layout = &l

	_, err := f.handler.Handle(context.Background(), regionclassify.Event{Step: regionclassify.StepBuildChunks, Run: in})
	var te *pipeline.TransientError
	if !errors.As(err, &te) || reflect.TypeOf(err) != reflect.TypeOf(te) {
		t.Errorf("err = %T %v, want a top-level *pipeline.TransientError", err, err)
	}
}
