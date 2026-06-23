package manifest_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/manifest"
	"github.com/kunalpednekar/dumpster/internal/manifest/memory"
)

var _ manifest.Repository = (*memory.Repository)(nil)

func makeRegion(userID, kbID, documentID uuid.UUID, rt manifest.RegionType, page int, status manifest.Status) *manifest.Region {
	return &manifest.Region{
		DocumentID:       documentID,
		KBID:             kbID,
		UserID:           userID,
		RegionType:       rt,
		PageNumber:       page,
		BoundingBox:      manifest.BoundingBox{X0: 0, Y0: float64(page) * 0.1, X1: 1, Y1: float64(page)*0.1 + 0.5},
		Status:           status,
		ExtractorVersion: "v1",
	}
}

func TestManifest_BulkCreateAndList(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID, kbID, docID := uuid.New(), uuid.New(), uuid.New()

	regions := []*manifest.Region{
		makeRegion(userID, kbID, docID, manifest.RegionTypeNativeText, 1, manifest.StatusIndexed),
		makeRegion(userID, kbID, docID, manifest.RegionTypeFigure, 1, manifest.StatusIndexed),
		makeRegion(userID, kbID, docID, manifest.RegionTypeScannedText, 2, manifest.StatusSkipped),
	}
	if err := repo.BulkCreate(ctx, regions); err != nil {
		t.Fatal(err)
	}

	list, err := repo.ListByDocument(ctx, userID, docID)
	if err != nil || len(list) != 3 {
		t.Fatalf("ListByDocument: got %d, want 3 (err: %v)", len(list), err)
	}
	for _, r := range list {
		if r.ID == uuid.Nil {
			t.Error("expected BulkCreate to assign a non-nil ID")
		}
	}
}

func TestManifest_ManifestSummary(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID, kbID, docID := uuid.New(), uuid.New(), uuid.New()

	_ = repo.BulkCreate(ctx, []*manifest.Region{
		makeRegion(userID, kbID, docID, manifest.RegionTypeNativeText, 1, manifest.StatusIndexed),
		makeRegion(userID, kbID, docID, manifest.RegionTypeNativeText, 2, manifest.StatusIndexed),
		makeRegion(userID, kbID, docID, manifest.RegionTypeScannedTable, 3, manifest.StatusSkipped),
	})

	list, _ := repo.ListByDocument(ctx, userID, docID)
	var indexed, skipped int
	for _, r := range list {
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
}

func TestManifest_DeleteByDocument(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID, kbID := uuid.New(), uuid.New()
	docA, docB := uuid.New(), uuid.New()

	_ = repo.BulkCreate(ctx, []*manifest.Region{
		makeRegion(userID, kbID, docA, manifest.RegionTypeNativeText, 1, manifest.StatusIndexed),
	})
	_ = repo.BulkCreate(ctx, []*manifest.Region{
		makeRegion(userID, kbID, docB, manifest.RegionTypeFigure, 1, manifest.StatusIndexed),
	})

	if err := repo.DeleteByDocument(ctx, userID, docA); err != nil {
		t.Fatal(err)
	}

	remaining, _ := repo.ListByDocument(ctx, userID, docA)
	if len(remaining) != 0 {
		t.Fatalf("expected 0 regions for docA, got %d", len(remaining))
	}
	untouched, _ := repo.ListByDocument(ctx, userID, docB)
	if len(untouched) != 1 {
		t.Fatalf("expected 1 region for docB, got %d", len(untouched))
	}
}

func TestManifest_TenantIsolation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	user1, user2, kbID, docID := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	_ = repo.BulkCreate(ctx, []*manifest.Region{
		makeRegion(user1, kbID, docID, manifest.RegionTypeNativeText, 1, manifest.StatusIndexed),
	})

	list, _ := repo.ListByDocument(ctx, user2, docID)
	if len(list) != 0 {
		t.Fatalf("user2 ListByDocument should be empty, got %d", len(list))
	}

	_ = repo.DeleteByDocument(ctx, user2, docID)
	remaining, _ := repo.ListByDocument(ctx, user1, docID)
	if len(remaining) != 1 {
		t.Fatalf("user1 regions should be untouched, got %d", len(remaining))
	}
}

func TestManifest_BulkCreate_Empty(t *testing.T) {
	repo := memory.New()
	if err := repo.BulkCreate(context.Background(), nil); err != nil {
		t.Fatalf("BulkCreate(nil) should be a no-op, got error: %v", err)
	}
}

func TestManifest_FullPage(t *testing.T) {
	bb := manifest.FullPage()
	if bb.X0 != 0 || bb.Y0 != 0 || bb.X1 != 1 || bb.Y1 != 1 {
		t.Errorf("FullPage: got %+v, want {0,0,1,1}", bb)
	}
}
