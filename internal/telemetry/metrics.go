package telemetry

import "go.opentelemetry.io/otel/metric"

// Instruments holds the OpenTelemetry metric instruments shared across the
// application. All fields target the OTel metric API; swapping the backend
// (e.g. from Prometheus to OTLP) is a change in Setup(), not in the
// instrumented paths.
type Instruments struct {
	// SearchLatency tracks end-to-end search request latency in milliseconds.
	// Expose p50/p95 via the histogram's bucket configuration.
	SearchLatency metric.Float64Histogram
	// EmbeddingDuration tracks the time spent calling the embedding API in ms.
	EmbeddingDuration metric.Float64Histogram
	// QueueDepth tracks the number of pending jobs in the queue. Increment on
	// enqueue, decrement on dequeue or dead-letter.
	QueueDepth metric.Int64UpDownCounter
	// JobFailureTotal counts jobs that have been dead-lettered after exhausting
	// retries. A rising rate here signals a systemic processing problem.
	JobFailureTotal metric.Int64Counter
	// DocumentsUploadedTotal counts document upload attempts, labeled by
	// outcome ("success"/"failure"). Only recorded once an upload has begun
	// persisting (object storage, DB row, or enqueue) — request validation
	// and auth rejections are not attempts and are not counted.
	DocumentsUploadedTotal metric.Int64Counter
	// DocumentsDeletedTotal counts document deletion attempts, labeled by
	// outcome ("success"/"failure"), with the same validation-vs-attempt
	// distinction as DocumentsUploadedTotal.
	DocumentsDeletedTotal metric.Int64Counter
	// JobDuration tracks wall-clock time from a job being claimed (Dequeue)
	// to its resolution (Ack or Nack), in milliseconds, labeled by outcome
	// ("success"/"failure"). The direct clearance-rate signal for deciding
	// whether to move off the Postgres-backed queue.
	JobDuration metric.Float64Histogram
}

// NewInstruments builds Instruments from an already-configured OTel Meter.
// Setup is the production entry point (OTLP/gRPC via the ADOT Collector
// sidecar); NewInstruments exists for tests in other packages that need real
// instruments backed by their own reader — e.g. telemetrytest's in-memory
// manual reader — instead of a live network exporter.
func NewInstruments(meter metric.Meter) (*Instruments, error) {
	return newInstruments(meter)
}
