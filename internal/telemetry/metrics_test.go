package telemetry_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

// hasMetricSample reports whether body contains a Prometheus exposition line
// for name with the given outcome label and value, regardless of what other
// labels (e.g. OTel's otel_scope_* resource attributes) are also present.
func hasMetricSample(body, name, outcome string, value int) bool {
	pattern := fmt.Sprintf(`%s\{[^}]*outcome="%s"[^}]*\}\s+%d`, regexp.QuoteMeta(name), regexp.QuoteMeta(outcome), value)
	return regexp.MustCompile(pattern).MatchString(body)
}

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
	if inst.JobDuration == nil {
		t.Error("JobDuration is nil")
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
	if !hasMetricSample(body, "documents_uploaded_total", "success", 1) {
		t.Errorf("metrics body does not contain expected documents_uploaded_total sample:\n%s", body)
	}
	if !hasMetricSample(body, "documents_deleted_total", "failure", 1) {
		t.Errorf("metrics body does not contain expected documents_deleted_total sample:\n%s", body)
	}
}

func TestSetup_JobDurationScrapeable(t *testing.T) {
	inst, handler, err := telemetry.Setup(context.Background())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	inst.JobDuration.Record(context.Background(), 123.4,
		metric.WithAttributes(attribute.String("outcome", "success")),
	)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("metrics handler: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	// The Prometheus exporter inserts a unit suffix (e.g. "_milliseconds",
	// from WithUnit("ms")) between the metric name and "_count".
	pattern := regexp.MustCompile(`job_duration_ms[a-z_]*_count\{[^}]*outcome="success"[^}]*\}\s+1`)
	if !pattern.MatchString(body) {
		t.Errorf("metrics body does not contain expected job_duration_ms sample:\n%s", body)
	}
}
