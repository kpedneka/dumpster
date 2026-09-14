package telemetry_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func TestSetup_InstrumentsNonNil(t *testing.T) {
	inst, shutdown, err := telemetry.Setup(context.Background(), "test")
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
	if shutdown == nil {
		t.Error("shutdown func is nil")
	}
}

// TestSetup_InstrumentsRecordWithoutError exercises every instrument the way
// the instrumented call sites do. otlpmetricgrpc.New dials lazily, so this
// must succeed even with no collector sidecar listening on localhost:4317 --
// true in this test environment and also true briefly in production before
// the sidecar container finishes starting.
func TestSetup_InstrumentsRecordWithoutError(t *testing.T) {
	inst, _, err := telemetry.Setup(context.Background(), "test")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	ctx := context.Background()
	inst.SearchLatency.Record(ctx, 42.5, metric.WithAttributes(attribute.String("status", "ok")))
	inst.EmbeddingDuration.Record(ctx, 12.3)
	inst.QueueDepth.Add(ctx, 1)
	inst.JobFailureTotal.Add(ctx, 1)
	inst.DocumentsUploadedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "success")))
	inst.DocumentsDeletedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "failure")))
	inst.JobDuration.Record(ctx, 123.4, metric.WithAttributes(attribute.String("outcome", "success")))
}

// TestSetup_ShutdownReturnsPromptly asserts the real production shape of a
// best-effort push exporter: with no collector reachable, the final flush
// attempt inside Shutdown may fail, but it must respect the caller's context
// deadline rather than hang retrying a connection.
func TestSetup_ShutdownReturnsPromptly(t *testing.T) {
	_, shutdown, err := telemetry.Setup(context.Background(), "test")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = shutdown(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not return within the context deadline")
	}
}
