package instrumented_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/llm/instrumented"
	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/telemetry/telemetrytest"
)

func TestEmbedder_RecordsDurationOnSuccess(t *testing.T) {
	inst, metrics := telemetrytest.New(t)
	inner := llmmock.NewEmbedder(3)
	e := instrumented.NewEmbedder(inner, inst)

	if _, err := e.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatal(err)
	}

	if got := metrics.HistogramCount("embedding_duration_ms"); got != 1 {
		t.Errorf("embedding_duration_ms count: got %d, want 1", got)
	}
}

func TestEmbedder_RecordsDurationOnError(t *testing.T) {
	inst, metrics := telemetrytest.New(t)
	inner := llmmock.NewEmbedder(3)
	inner.EmbedFn = func(_ context.Context, _ []string) ([][]float32, error) {
		return nil, errors.New("embedding api unavailable")
	}
	e := instrumented.NewEmbedder(inner, inst)

	if _, err := e.Embed(context.Background(), []string{"hello"}); err == nil {
		t.Fatal("expected error")
	}

	if got := metrics.HistogramCount("embedding_duration_ms"); got != 1 {
		t.Errorf("embedding_duration_ms count even on error: got %d, want 1", got)
	}
}

func TestEmbedder_DimsDelegatesToInner(t *testing.T) {
	inst, _ := telemetrytest.New(t)
	inner := llmmock.NewEmbedder(7)
	e := instrumented.NewEmbedder(inner, inst)
	if e.Dims() != 7 {
		t.Errorf("Dims: got %d, want 7", e.Dims())
	}
}

func TestEmbedder_NilInstrumentsDoesNotPanic(t *testing.T) {
	inner := llmmock.NewEmbedder(3)
	e := instrumented.NewEmbedder(inner, nil)
	if _, err := e.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatal(err)
	}
}
