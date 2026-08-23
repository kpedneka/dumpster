package instrumented_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/llm/instrumented"
	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func mustInstruments(t *testing.T) (*telemetry.Instruments, http.Handler) {
	t.Helper()
	inst, handler, err := telemetry.Setup(context.Background())
	if err != nil {
		t.Fatalf("telemetry.Setup: %v", err)
	}
	return inst, handler
}

func scrapeMetrics(t *testing.T, handler http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w.Body.String()
}

// hasHistogramCount reports whether body contains a Prometheus _count sample
// for the given histogram name with the given count, regardless of labels.
// The exporter may insert a unit suffix (e.g. "_milliseconds") between name
// and "_count", so that gap is matched loosely.
func hasHistogramCount(body, name string, count int) bool {
	pattern := fmt.Sprintf(`%s[a-z_]*_count\{[^}]*\}\s+%d`, regexp.QuoteMeta(name), count)
	return regexp.MustCompile(pattern).MatchString(body)
}

func TestEmbedder_RecordsDurationOnSuccess(t *testing.T) {
	inst, metricsHandler := mustInstruments(t)
	inner := llmmock.NewEmbedder(3)
	e := instrumented.NewEmbedder(inner, inst)

	if _, err := e.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatal(err)
	}

	got := scrapeMetrics(t, metricsHandler)
	if !hasHistogramCount(got, "embedding_duration_ms", 1) {
		t.Errorf("expected embedding_duration_ms sample, got:\n%s", got)
	}
}

func TestEmbedder_RecordsDurationOnError(t *testing.T) {
	inst, metricsHandler := mustInstruments(t)
	inner := llmmock.NewEmbedder(3)
	inner.EmbedFn = func(_ context.Context, _ []string) ([][]float32, error) {
		return nil, errors.New("embedding api unavailable")
	}
	e := instrumented.NewEmbedder(inner, inst)

	if _, err := e.Embed(context.Background(), []string{"hello"}); err == nil {
		t.Fatal("expected error")
	}

	got := scrapeMetrics(t, metricsHandler)
	if !hasHistogramCount(got, "embedding_duration_ms", 1) {
		t.Errorf("expected embedding_duration_ms sample even on error, got:\n%s", got)
	}
}

func TestEmbedder_DimsDelegatesToInner(t *testing.T) {
	inst, _ := mustInstruments(t)
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
