package memory_test

import (
	"context"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/stats"
	"github.com/kunalpednekar/dumpster/internal/stats/memory"
)

func TestGet_ZeroCountsProduceZeroAverages(t *testing.T) {
	repo := memory.New()
	got, err := repo.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := stats.Snapshot{}
	if got != want {
		t.Errorf("Get() on empty repo = %+v, want %+v", got, want)
	}
}

func TestRecordDocumentIndexed_TracksCountAndAverage(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()

	if err := repo.RecordDocumentIndexed(ctx, 100); err != nil {
		t.Fatalf("RecordDocumentIndexed: %v", err)
	}
	if err := repo.RecordDocumentIndexed(ctx, 300); err != nil {
		t.Fatalf("RecordDocumentIndexed: %v", err)
	}

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DocumentsIndexed != 2 {
		t.Errorf("DocumentsIndexed = %d, want 2", got.DocumentsIndexed)
	}
	if got.AvgDocumentSizeBytes != 200 {
		t.Errorf("AvgDocumentSizeBytes = %v, want 200", got.AvgDocumentSizeBytes)
	}
}

func TestRecordQueryExecuted_TracksCountAndAverage(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()

	if err := repo.RecordQueryExecuted(ctx, 50); err != nil {
		t.Fatalf("RecordQueryExecuted: %v", err)
	}
	if err := repo.RecordQueryExecuted(ctx, 150); err != nil {
		t.Fatalf("RecordQueryExecuted: %v", err)
	}
	if err := repo.RecordQueryExecuted(ctx, 100); err != nil {
		t.Fatalf("RecordQueryExecuted: %v", err)
	}

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.QueriesExecuted != 3 {
		t.Errorf("QueriesExecuted = %d, want 3", got.QueriesExecuted)
	}
	if got.AvgQueryDurationMs != 100 {
		t.Errorf("AvgQueryDurationMs = %v, want 100", got.AvgQueryDurationMs)
	}
}

func TestDocumentAndQueryCounters_AreIndependent(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()

	if err := repo.RecordDocumentIndexed(ctx, 500); err != nil {
		t.Fatalf("RecordDocumentIndexed: %v", err)
	}

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.QueriesExecuted != 0 || got.AvgQueryDurationMs != 0 {
		t.Errorf("query counters should be untouched by a document event, got %+v", got)
	}
}
