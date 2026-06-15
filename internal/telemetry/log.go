// Package telemetry provides structured logging and OpenTelemetry metrics for
// the application. All instrumentation targets the OTel API; the Prometheus
// backend is a wiring choice in Setup(), keeping application code vendor-neutral.
package telemetry

import (
	"io"
	"log/slog"
)

// NewLogger returns a JSON-format slog.Logger that includes a "service" field
// in every record. Callers should add per-request fields (user_id, document_id,
// duration_ms) via the returned logger's With or Info/Error methods so that
// log events are queryable by field.
func NewLogger(w io.Writer, service string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})).With("service", service)
}
