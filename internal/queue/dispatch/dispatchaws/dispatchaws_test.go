package dispatchaws_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/config"
	statusmem "github.com/kunalpednekar/dumpster/internal/jobstatus/memory"
	"github.com/kunalpednekar/dumpster/internal/queue/dispatch/dispatchaws"
)

func fullConfig() *config.Config {
	return &config.Config{
		AWSRegion:                           "us-east-1",
		DocumentIndexingStateMachineARN:     "arn:aws:states:us-east-1:1:stateMachine:doc",
		EntityExtractionStateMachineARN:     "arn:aws:states:us-east-1:1:stateMachine:entity",
		RegionClassificationStateMachineARN: "arn:aws:states:us-east-1:1:stateMachine:region",
		EdgeExtractionQueueURL:              "https://sqs.us-east-1.amazonaws.com/1/edge",
		CanonicalizationQueueURL:            "https://sqs.us-east-1.amazonaws.com/1/canon",
	}
}

func TestNew_FullConfig_BuildsAPublisher(t *testing.T) {
	p, err := dispatchaws.New(context.Background(), fullConfig(), statusmem.New())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p == nil {
		t.Fatal("New returned a nil publisher")
	}
}

// A missing target would otherwise only surface on the first upload of
// that job type, so it must fail at startup and name what's missing.
func TestNew_MissingTargets_FailsNamingEachEnvVar(t *testing.T) {
	cfg := fullConfig()
	cfg.EntityExtractionStateMachineARN = ""
	cfg.CanonicalizationQueueURL = ""

	_, err := dispatchaws.New(context.Background(), cfg, statusmem.New())
	if err == nil {
		t.Fatal("New err = nil, want an error for missing targets")
	}
	for _, name := range []string{"ENTITY_EXTRACTION_STATE_MACHINE_ARN", "CANONICALIZATION_QUEUE_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
	for _, name := range []string{"DOCUMENT_INDEXING_STATE_MACHINE_ARN", "EDGE_EXTRACTION_QUEUE_URL"} {
		if strings.Contains(err.Error(), name) {
			t.Errorf("error %q names %s, which is set", err, name)
		}
	}
}
