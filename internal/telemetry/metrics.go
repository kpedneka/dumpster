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
}
