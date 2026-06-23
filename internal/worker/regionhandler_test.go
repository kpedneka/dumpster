package worker_test

import (
	"context"
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
	"github.com/kunalpednekar/dumpster/internal/vision"
	visionmock "github.com/kunalpednekar/dumpster/internal/vision/mock"
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
	describer := visionmock.New()
	describer.DescribeResponse = "A pie chart showing budget allocation."

	job, userID := seedRegionJob(t, docs, objects, "fakeimagebytes", "image/png")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, nil, describer, fakeEmbedder{}, pub,
	)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	regions, _ := manifestRepo.ListByDocument(ctx, userID, job.DocumentID)
	if len(regions) != 1 {
		t.Fatalf("expected 1 manifest region, got %d", len(regions))
	}
	r := regions[0]
	if r.RegionType != manifest.RegionTypeFigure {
		t.Errorf("region type: got %q, want %q", r.RegionType, manifest.RegionTypeFigure)
	}
	if r.Status != manifest.StatusIndexed {
		t.Errorf("region status: got %q, want %q", r.Status, manifest.StatusIndexed)
	}

	// One chunk should exist with the VLM description as text.
	cs, _ := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if len(cs) == 0 {
		t.Fatal("expected at least 1 chunk from figure description")
	}
	if !strings.Contains(cs[0].Text, "pie chart") {
		t.Errorf("chunk text should contain VLM description, got %q", cs[0].Text)
	}
	if cs[0].PageNumber == nil || *cs[0].PageNumber != 1 {
		t.Errorf("chunk should carry page_number=1 from image upload path")
	}

	// Document should reach Indexed.
	doc, _ := docs.Get(ctx, userID, job.DocumentID)
	if doc.Status != document.StatusIndexed {
		t.Errorf("document status: got %q, want indexed", doc.Status)
	}

	// Entity extraction should be enqueued.
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
	describer := visionmock.New()
	describer.DescribeResponse = "A bar chart showing revenue."

	// Fake layout extractor: 1 native_text, 1 figure, 1 unconfirmed_table
	fakeExtractor := &fakeLayoutExtractor{regions: []*layout.RawRegion{
		{RegionType: "native_text", PageNumber: 1, BoundingBox: [4]float64{0, 0, 1, 0.3}, Text: "Executive summary text.", NeedsVLM: ""},
		{RegionType: "figure", PageNumber: 1, BoundingBox: [4]float64{0, 0.4, 1, 0.7}, ImageBase64: "aW1hZ2U=", NeedsVLM: "describe"},
		{RegionType: "unconfirmed_table", PageNumber: 2, BoundingBox: [4]float64{0, 0, 1, 0.5}, ImageBase64: "dGFibGU=", NeedsVLM: "confirm_scan"},
	}}
	// ConfirmScanned returns true (confirmed scanned) for the table.
	describer.IsScanned = true

	job, userID := seedRegionJob(t, docs, objects, "%PDF-fake", "application/pdf")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, fakeExtractor, describer, fakeEmbedder{}, pub,
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
	if indexed != 2 || skipped != 1 {
		t.Errorf("summary: got indexed=%d skipped=%d, want indexed=2 skipped=1", indexed, skipped)
	}

	cs, _ := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if len(cs) < 2 {
		t.Fatalf("expected at least 2 chunks (native text + figure), got %d", len(cs))
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
	describer := visionmock.New()
	describer.IsScanned = true

	// All regions are scanned/unconfirmed and confirmed scanned.
	fakeExtractor := &fakeLayoutExtractor{regions: []*layout.RawRegion{
		{RegionType: "unconfirmed_text", PageNumber: 1, BoundingBox: [4]float64{0, 0, 1, 1}, ImageBase64: "aW1n", NeedsVLM: "confirm_scan"},
	}}

	job, userID := seedRegionJob(t, docs, objects, "%PDF-fake", "application/pdf")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, fakeExtractor, describer, fakeEmbedder{}, pub,
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
	describer := visionmock.New()

	fakeExtractor := &fakeLayoutExtractor{regions: []*layout.RawRegion{
		{RegionType: "native_text", PageNumber: 3, BoundingBox: [4]float64{0.1, 0.2, 0.9, 0.5}, Text: "Some text on page three.", NeedsVLM: ""},
	}}

	job, userID := seedRegionJob(t, docs, objects, "%PDF-fake", "application/pdf")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, fakeExtractor, describer, fakeEmbedder{}, pub,
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
	describer := visionmock.New()
	describer.DescribeResponse = "A diagram."

	job, userID := seedRegionJob(t, docs, objects, "fakeimagebytes", "image/png")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, nil, describer, fakeEmbedder{}, pub,
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
	describer := visionmock.New()

	job, userID := seedRegionJob(t, docs, objects, "bytes", "image/png")
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewRegionClassificationHandler(
		docs, objects, chunks, manifestRepo, nil, describer, fakeEmbedder{}, pub,
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

// Ensure vision.Describer interface is correctly defined by checking mock implements it.
var _ vision.Describer = (*visionmock.Describer)(nil)

// Ensure layout extractor interface is satisfied by the fake.
var _ worker.LayoutExtractor = (*fakeLayoutExtractor)(nil)

