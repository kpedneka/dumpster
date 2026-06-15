package telemetry_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func TestNewLogger_JSONOutput(t *testing.T) {
	var buf bytes.Buffer
	logger := telemetry.NewLogger(&buf, "test-service")

	logger.Info("test event", "user_id", "abc-123", "duration_ms", 42)

	var record map[string]any
	if err := json.NewDecoder(&buf).Decode(&record); err != nil {
		t.Fatalf("output is not valid JSON: %v — output: %q", err, buf.String())
	}

	if record["service"] != "test-service" {
		t.Errorf("service field: got %q, want %q", record["service"], "test-service")
	}
	if record["user_id"] != "abc-123" {
		t.Errorf("user_id field: got %v, want %q", record["user_id"], "abc-123")
	}
	if record["msg"] != "test event" {
		t.Errorf("msg field: got %v, want %q", record["msg"], "test event")
	}
}
