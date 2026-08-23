package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Setup initialises a Prometheus-backed OTel MeterProvider and returns the
// metric instruments plus an HTTP handler for the Prometheus scrape endpoint.
//
// Application code depends only on the OTel metric API via Instruments;
// swapping the backend to OTLP/gRPC or another exporter is a change here,
// not in the instrumented paths — satisfying the vendor-neutral seam requirement.
//
// Setup does not set the global OTel MeterProvider to keep it self-contained
// and safe to call multiple times in tests.
func Setup(_ context.Context) (*Instruments, http.Handler, error) {
	reg := prometheus.NewRegistry()

	exp, err := promexporter.New(promexporter.WithRegisterer(reg))
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry: prometheus exporter: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp))
	meter := provider.Meter("dumpster")

	inst, err := newInstruments(meter)
	if err != nil {
		return nil, nil, err
	}

	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return inst, handler, nil
}

func newInstruments(meter metric.Meter) (*Instruments, error) {
	searchLatency, err := meter.Float64Histogram(
		"search_latency_ms",
		metric.WithDescription("End-to-end search request latency in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: search_latency_ms: %w", err)
	}

	embeddingDuration, err := meter.Float64Histogram(
		"embedding_duration_ms",
		metric.WithDescription("Embedding API call duration in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: embedding_duration_ms: %w", err)
	}

	queueDepth, err := meter.Int64UpDownCounter(
		"queue_depth",
		metric.WithDescription("Number of jobs currently pending in the queue"),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: queue_depth: %w", err)
	}

	jobFailureTotal, err := meter.Int64Counter(
		"job_failure_total",
		metric.WithDescription("Number of jobs dead-lettered after exhausting retries"),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: job_failure_total: %w", err)
	}

	documentsUploadedTotal, err := meter.Int64Counter(
		"documents_uploaded_total",
		metric.WithDescription("Number of document upload attempts, labeled by outcome"),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: documents_uploaded_total: %w", err)
	}

	documentsDeletedTotal, err := meter.Int64Counter(
		"documents_deleted_total",
		metric.WithDescription("Number of document deletion attempts, labeled by outcome"),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: documents_deleted_total: %w", err)
	}

	jobDuration, err := meter.Float64Histogram(
		"job_duration_ms",
		metric.WithDescription("Wall-clock time from a job being claimed to its resolution, in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: job_duration_ms: %w", err)
	}

	return &Instruments{
		SearchLatency:          searchLatency,
		EmbeddingDuration:      embeddingDuration,
		QueueDepth:             queueDepth,
		JobFailureTotal:        jobFailureTotal,
		DocumentsUploadedTotal: documentsUploadedTotal,
		DocumentsDeletedTotal:  documentsDeletedTotal,
		JobDuration:            jobDuration,
	}, nil
}
