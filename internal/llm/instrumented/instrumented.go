// Package instrumented wraps an llm.Embedder to record embedding call
// duration, without requiring the retrieval layer that consumes the
// embedder to know anything about telemetry.
package instrumented

import (
	"context"
	"time"

	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

// Embedder wraps an llm.Embedder, recording EmbeddingDuration around every
// Embed call so a slow embedding provider can be distinguished from a slow
// vector query when diagnosing search or ingestion latency.
type Embedder struct {
	inner llm.Embedder
	inst  *telemetry.Instruments
}

// NewEmbedder returns an Embedder that delegates to inner and records
// EmbeddingDuration on every call. inst may be nil, in which case no metric
// is recorded.
func NewEmbedder(inner llm.Embedder, inst *telemetry.Instruments) *Embedder {
	return &Embedder{inner: inner, inst: inst}
}

// Dims delegates to the wrapped Embedder.
func (e *Embedder) Dims() int { return e.inner.Dims() }

// Embed delegates to the wrapped Embedder and records EmbeddingDuration
// regardless of outcome, since call latency is meaningful whether or not the
// call ultimately succeeded.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	started := time.Now()
	vecs, err := e.inner.Embed(ctx, texts)
	if e.inst != nil {
		e.inst.EmbeddingDuration.Record(ctx, float64(time.Since(started).Microseconds())/1000)
	}
	return vecs, err
}

var _ llm.Embedder = (*Embedder)(nil)
