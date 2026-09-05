package worker_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	chunkmem "github.com/kunalpednekar/dumpster/internal/chunk/memory"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/manifest"
	"github.com/kunalpednekar/dumpster/internal/manifest/layout"
	manifestmem "github.com/kunalpednekar/dumpster/internal/manifest/memory"
	"github.com/kunalpednekar/dumpster/internal/objectstore/mock"
	"github.com/kunalpednekar/dumpster/internal/queue"
	qmem "github.com/kunalpednekar/dumpster/internal/queue/memory"
	statsmem "github.com/kunalpednekar/dumpster/internal/stats/memory"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

// fakeLayoutExtractor is a minimal layout.RegionExtractor for tests.
type fakeLayoutExtractor struct {
	regions []*layout.RawRegion
	err     error
}

func (f *fakeLayoutExtractor) ExtractRegions(_ context.Context, _ []byte) ([]*layout.RawRegion, error) {
	return f.regions, f.err
}

// fakeEmbedder satisfies the llm.Embedder interface for tests.
type fakeEmbedder struct{}

func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i := range vecs {
		vecs[i] = []float32{0.1, 0.2}
	}
	return vecs, nil
}

func (fakeEmbedder) Dims() int { return 2 }

// fakePhaseSetter is a minimal worker.PhaseSetter for tests, recording
// every SetPhase call in order.
type fakePhaseSetter struct {
	calls []string
	err   error
}

func (f *fakePhaseSetter) SetPhase(_ context.Context, _ uuid.UUID, phase string) error {
	f.calls = append(f.calls, phase)
	return f.err
}

func seedRegionJob(
	t *testing.T,
	docs *docmem.Repository,
	objects *mock.Store,
	content string,
	contentType string,
) (*queue.Job, uuid.UUID) {
	t.Helper()
	userID := uuid.New()
	ctx := auth.WithUserID(context.Background(), userID)
	s3Key := "uploads/" + uuid.New().String()

	doc, err := docs.Create(ctx, &document.Document{
		KBID:        uuid.New(),
		UserID:      userID,
		Filename:    "file",
		S3Key:       s3Key,
		ContentType: contentType,
		Status:      document.StatusPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(ctx, s3Key, strings.NewReader(content), int64(len(content)), contentType); err != nil {
		t.Fatal(err)
	}
	return &queue.Job{
		ID:          uuid.New(),
		Type:        queue.JobTypeRegionClassification,
		DocumentID:  doc.ID,
		UserID:      userID,
		Attempts:    0,
		MaxAttempts: 3,
	}, userID
}

func TestRegionHandler_Image_DegenrateOneRegion(t *testing.T) {
	docs := docmem.New()
	objects := mock.New()
	chunks := chunkmem.New()
	manifestRepo := manifestmem.New()
	pub := qmem.New()

	job, userID := seedRegionJob(t, docs, objects, "fakeimagebytes", "image/png")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, nil, fakeEmbedder{}, pub,
	)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Figure regions have no VLM follow-up (see regionhandler.go's type doc):
	// detected but skipped, not described.
	regions, _ := manifestRepo.ListByDocument(ctx, userID, job.DocumentID)
	if len(regions) != 1 {
		t.Fatalf("expected 1 manifest region, got %d", len(regions))
	}
	r := regions[0]
	if r.RegionType != manifest.RegionTypeFigure {
		t.Errorf("region type: got %q, want %q", r.RegionType, manifest.RegionTypeFigure)
	}
	if r.Status != manifest.StatusSkipped {
		t.Errorf("region status: got %q, want %q", r.Status, manifest.StatusSkipped)
	}

	// No description means no chunk.
	cs, _ := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if len(cs) != 0 {
		t.Errorf("expected no chunks for an undescribed figure, got %d", len(cs))
	}

	// Document should still reach Indexed even with nothing to index.
	doc, _ := docs.Get(ctx, userID, job.DocumentID)
	if doc.Status != document.StatusIndexed {
		t.Errorf("document status: got %q, want indexed", doc.Status)
	}

	// Entity extraction should still be enqueued (it's unconditional on
	// reaching Indexed, independent of whether any chunks were produced).
	if len(pub.EntityExtractionEvents()) != 1 {
		t.Errorf("expected 1 entity-extraction event published, got %d", len(pub.EntityExtractionEvents()))
	}
}

func TestRegionHandler_PDF_MixedRegions(t *testing.T) {
	docs := docmem.New()
	objects := mock.New()
	chunks := chunkmem.New()
	manifestRepo := manifestmem.New()
	pub := qmem.New()

	// Fake layout extractor: 1 native_text, 1 figure, 1 unconfirmed_table
	fakeExtractor := &fakeLayoutExtractor{regions: []*layout.RawRegion{
		{RegionType: "native_text", PageNumber: 1, BoundingBox: [4]float64{0, 0, 1, 0.3}, Text: "Executive summary text.", NeedsVLM: ""},
		{RegionType: "figure", PageNumber: 1, BoundingBox: [4]float64{0, 0.4, 1, 0.7}, NeedsVLM: "describe"},
		{RegionType: "unconfirmed_table", PageNumber: 2, BoundingBox: [4]float64{0, 0, 1, 0.5}, NeedsVLM: "confirm_scan"},
	}}

	job, userID := seedRegionJob(t, docs, objects, "%PDF-fake", "application/pdf")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, fakeExtractor, fakeEmbedder{}, pub,
	)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	regions, _ := manifestRepo.ListByDocument(ctx, userID, job.DocumentID)
	if len(regions) != 3 {
		t.Fatalf("expected 3 manifest regions (all, including skipped), got %d", len(regions))
	}

	var indexed, skipped int
	for _, r := range regions {
		switch r.Status {
		case manifest.StatusIndexed:
			indexed++
		case manifest.StatusSkipped:
			skipped++
		}
	}
	// Only the native_text region resolves without a VLM follow-up; the
	// figure and unconfirmed_table regions are now always skipped.
	if indexed != 1 || skipped != 2 {
		t.Errorf("summary: got indexed=%d skipped=%d, want indexed=1 skipped=2", indexed, skipped)
	}

	cs, _ := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if len(cs) == 0 {
		t.Fatalf("expected at least 1 chunk (native text), got %d", len(cs))
	}

	doc, _ := docs.Get(ctx, userID, job.DocumentID)
	if doc.Status != document.StatusIndexed {
		t.Errorf("document status: got %q, want indexed", doc.Status)
	}
}

func TestRegionHandler_PDF_FullyScanned_StillReachesIndexed(t *testing.T) {
	docs := docmem.New()
	objects := mock.New()
	chunks := chunkmem.New()
	manifestRepo := manifestmem.New()
	pub := qmem.New()

	// All regions are unconfirmed, and unconfirmed regions are always skipped.
	fakeExtractor := &fakeLayoutExtractor{regions: []*layout.RawRegion{
		{RegionType: "unconfirmed_text", PageNumber: 1, BoundingBox: [4]float64{0, 0, 1, 1}, NeedsVLM: "confirm_scan"},
	}}

	job, userID := seedRegionJob(t, docs, objects, "%PDF-fake", "application/pdf")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, fakeExtractor, fakeEmbedder{}, pub,
	)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	doc, _ := docs.Get(ctx, userID, job.DocumentID)
	if doc.Status != document.StatusIndexed {
		t.Errorf("fully-scanned doc should still reach indexed, got %q", doc.Status)
	}

	regions, _ := manifestRepo.ListByDocument(ctx, userID, job.DocumentID)
	if len(regions) != 1 || regions[0].Status != manifest.StatusSkipped {
		t.Errorf("region should be skipped, got %+v", regions)
	}

	cs, _ := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if len(cs) != 0 {
		t.Errorf("no chunks expected for fully-scanned doc, got %d", len(cs))
	}
}

func TestRegionHandler_RegionTaggedOnChunks(t *testing.T) {
	docs := docmem.New()
	objects := mock.New()
	chunks := chunkmem.New()
	manifestRepo := manifestmem.New()
	pub := qmem.New()

	fakeExtractor := &fakeLayoutExtractor{regions: []*layout.RawRegion{
		{RegionType: "native_text", PageNumber: 3, BoundingBox: [4]float64{0.1, 0.2, 0.9, 0.5}, Text: "Some text on page three.", NeedsVLM: ""},
	}}

	job, userID := seedRegionJob(t, docs, objects, "%PDF-fake", "application/pdf")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, fakeExtractor, fakeEmbedder{}, pub,
	)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	cs, _ := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if len(cs) == 0 {
		t.Fatal("expected at least 1 chunk")
	}
	c := cs[0]
	if c.PageNumber == nil || *c.PageNumber != 3 {
		t.Errorf("chunk PageNumber: got %v, want 3", c.PageNumber)
	}
	if c.BoundingBox == nil {
		t.Error("chunk BoundingBox should be set for region-derived chunk")
	} else if c.BoundingBox.X0 != 0.1 {
		t.Errorf("BoundingBox.X0: got %v, want 0.1", c.BoundingBox.X0)
	}
	if c.RegionID == nil {
		t.Error("chunk RegionID should be set for region-derived chunk")
	}
}

func TestRegionHandler_IdempotentRerun(t *testing.T) {
	docs := docmem.New()
	objects := mock.New()
	chunks := chunkmem.New()
	manifestRepo := manifestmem.New()
	pub := qmem.New()

	job, userID := seedRegionJob(t, docs, objects, "fakeimagebytes", "image/png")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, nil, fakeEmbedder{}, pub,
	)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	firstChunks, _ := chunks.ListByDocument(ctx, userID, job.DocumentID)
	firstRegions, _ := manifestRepo.ListByDocument(ctx, userID, job.DocumentID)

	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	secondChunks, _ := chunks.ListByDocument(ctx, userID, job.DocumentID)
	secondRegions, _ := manifestRepo.ListByDocument(ctx, userID, job.DocumentID)

	if len(firstChunks) != len(secondChunks) {
		t.Errorf("chunk count changed on re-run: %d → %d", len(firstChunks), len(secondChunks))
	}
	if len(firstRegions) != len(secondRegions) {
		t.Errorf("region count changed on re-run: %d → %d", len(firstRegions), len(secondRegions))
	}
}

func TestRegionHandler_OnFailed_MarksDocumentFailed(t *testing.T) {
	docs := docmem.New()
	objects := mock.New()
	chunks := chunkmem.New()
	manifestRepo := manifestmem.New()
	pub := qmem.New()

	job, userID := seedRegionJob(t, docs, objects, "bytes", "image/png")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, nil, fakeEmbedder{}, pub,
	)
	h.OnFailed(ctx, job)

	doc, _ := docs.Get(ctx, userID, job.DocumentID)
	if doc.Status != document.StatusFailed {
		t.Errorf("OnFailed should mark document as failed, got %q", doc.Status)
	}
}

func TestRegionHandler_ImplementsHandler(t *testing.T) {
	var _ worker.Handler = (*worker.RegionClassificationHandler)(nil)
}

func TestRegionHandler_Handle_RecordsStatsOnIndexed(t *testing.T) {
	docs := docmem.New()
	objects := mock.New()
	chunks := chunkmem.New()
	manifestRepo := manifestmem.New()
	pub := qmem.New()
	statsRepo := statsmem.New()

	job, userID := seedRegionJob(t, docs, objects, "fakeimagebytes", "image/png")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, nil, fakeEmbedder{}, pub,
	).WithStats(statsRepo)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	snap, err := statsRepo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if snap.DocumentsIndexed != 1 {
		t.Errorf("DocumentsIndexed = %d, want 1", snap.DocumentsIndexed)
	}
}

func TestRegionHandler_Handle_NilStats_NoPanic(t *testing.T) {
	docs := docmem.New()
	objects := mock.New()
	chunks := chunkmem.New()
	manifestRepo := manifestmem.New()
	pub := qmem.New()

	job, userID := seedRegionJob(t, docs, objects, "fakeimagebytes", "image/png")
	ctx := auth.WithUserID(context.Background(), userID)

	// No WithStats call: stats stays nil.
	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, nil, fakeEmbedder{}, pub,
	)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

func TestRegionHandler_Handle_SetsPhaseToEmbedding(t *testing.T) {
	docs := docmem.New()
	objects := mock.New()
	chunks := chunkmem.New()
	manifestRepo := manifestmem.New()
	pub := qmem.New()
	phases := &fakePhaseSetter{}

	fakeExtractor := &fakeLayoutExtractor{regions: []*layout.RawRegion{
		{RegionType: "native_text", PageNumber: 1, BoundingBox: [4]float64{0, 0, 1, 0.3}, Text: "Some text.", NeedsVLM: ""},
	}}
	job, userID := seedRegionJob(t, docs, objects, "%PDF-fake", "application/pdf")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, fakeExtractor, fakeEmbedder{}, pub,
	).WithPhaseTracking(phases)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(phases.calls) != 1 || phases.calls[0] != queue.PhaseEmbedding {
		t.Errorf("SetPhase calls = %v, want exactly one call with %q", phases.calls, queue.PhaseEmbedding)
	}
}

func TestRegionHandler_Handle_NilPhaseTracking_NoPanic(t *testing.T) {
	docs := docmem.New()
	objects := mock.New()
	chunks := chunkmem.New()
	manifestRepo := manifestmem.New()
	pub := qmem.New()

	fakeExtractor := &fakeLayoutExtractor{regions: []*layout.RawRegion{
		{RegionType: "native_text", PageNumber: 1, BoundingBox: [4]float64{0, 0, 1, 0.3}, Text: "Some text.", NeedsVLM: ""},
	}}
	job, userID := seedRegionJob(t, docs, objects, "%PDF-fake", "application/pdf")
	ctx := auth.WithUserID(context.Background(), userID)

	// No WithPhaseTracking call: phases stays nil.
	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, fakeExtractor, fakeEmbedder{}, pub,
	)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

func TestRegionHandler_Handle_SetPhaseError_DoesNotFailJob(t *testing.T) {
	docs := docmem.New()
	objects := mock.New()
	chunks := chunkmem.New()
	manifestRepo := manifestmem.New()
	pub := qmem.New()
	phases := &fakePhaseSetter{err: fmt.Errorf("boom")}

	fakeExtractor := &fakeLayoutExtractor{regions: []*layout.RawRegion{
		{RegionType: "native_text", PageNumber: 1, BoundingBox: [4]float64{0, 0, 1, 0.3}, Text: "Some text.", NeedsVLM: ""},
	}}
	job, userID := seedRegionJob(t, docs, objects, "%PDF-fake", "application/pdf")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, fakeExtractor, fakeEmbedder{}, pub,
	).WithPhaseTracking(phases)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v (a SetPhase failure should not fail the job)", err)
	}
}

// Ensure layout extractor interface is satisfied by the fake.
var _ worker.LayoutExtractor = (*fakeLayoutExtractor)(nil)
