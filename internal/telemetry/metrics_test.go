package telemetry_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func TestSetup_InstrumentsNonNil(t *testing.T) {
	inst, handler, err := telemetry.Setup(context.Background())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if inst == nil {
		t.Fatal("Instruments is nil")
	}
	if inst.SearchLatency == nil {
		t.Error("SearchLatency is nil")
	}
	if inst.EmbeddingDuration == nil {
		t.Error("EmbeddingDuration is nil")
	}
	if inst.QueueDepth == nil {
		t.Error("QueueDepth is nil")
	}
	if inst.JobFailureTotal == nil {
		t.Error("JobFailureTotal is nil")
	}
	if inst.DocumentsUploadedTotal == nil {
		t.Error("DocumentsUploadedTotal is nil")
	}
	if inst.DocumentsDeletedTotal == nil {
		t.Error("DocumentsDeletedTotal is nil")
	}
	if handler == nil {
		t.Error("metrics HTTP handler is nil")
	}
}

func TestSetup_MetricsScrapeable(t *testing.T) {
	inst, handler, err := telemetry.Setup(context.Background())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	inst.SearchLatency.Record(context.Background(), 42.5,
		metric.WithAttributes(attribute.String("status", "ok")),
	)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("metrics handler: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "search_latency_ms") {
		t.Errorf("metrics body does not contain search_latency_ms:\n%s", body)
	}
}

func TestSetup_DocumentMetricsScrapeable(t *testing.T) {
	inst, handler, err := telemetry.Setup(context.Background())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	inst.DocumentsUploadedTotal.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String("outcome", "success")),
	)
	inst.DocumentsDeletedTotal.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String("outcome", "failure")),
	)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("metrics handler: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `documents_uploaded_total{outcome="success"} 1`) {
		t.Errorf("metrics body does not contain expected documents_uploaded_total sample:\n%s", body)
	}
	if !strings.Contains(body, `documents_deleted_total{outcome="failure"} 1`) {
		t.Errorf("metrics body does not contain expected documents_deleted_total sample:\n%s", body)
	}
}
