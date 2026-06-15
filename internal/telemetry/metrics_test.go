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
