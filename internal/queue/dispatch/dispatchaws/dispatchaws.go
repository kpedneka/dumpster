// Package dispatchaws builds the production queue.Publisher: the
// internal/queue/dispatch publisher wired to real AWS clients for Step
// Functions and SQS, with the targets from config. cmd/api,
// cmd/evalbaseline and cmd/lambda-pipeline all publish through it, so they
// can't drift apart.
package dispatchaws

import (
	"context"
	"fmt"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/queue/dispatch"
	"github.com/kunalpednekar/dumpster/internal/queue/sqs"
	"github.com/kunalpednekar/dumpster/internal/queue/stepfunctions"
)

// New returns a dispatch.Publisher that records jobs in status and starts
// them on the state machines and queues cfg names. It fails if any target
// is missing, naming the environment variables to set, so a misconfigured
// deployment stops at startup instead of on the first publish of that job
// type.
func New(ctx context.Context, cfg *config.Config, status dispatch.StatusWriter) (*dispatch.Publisher, error) {
	targets := []struct{ env, value string }{
		{"DOCUMENT_INDEXING_STATE_MACHINE_ARN", cfg.DocumentIndexingStateMachineARN},
		{"ENTITY_EXTRACTION_STATE_MACHINE_ARN", cfg.EntityExtractionStateMachineARN},
		{"REGION_CLASSIFICATION_STATE_MACHINE_ARN", cfg.RegionClassificationStateMachineARN},
		{"EDGE_EXTRACTION_QUEUE_URL", cfg.EdgeExtractionQueueURL},
		{"CANONICALIZATION_QUEUE_URL", cfg.CanonicalizationQueueURL},
	}
	var missing []string
	for _, t := range targets {
		if t.value == "" {
			missing = append(missing, t.env)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("dispatchaws: pipeline targets not configured: %s", strings.Join(missing, ", "))
	}

	sqsClient, err := sqs.NewClient(ctx, cfg.AWSRegion)
	if err != nil {
		return nil, fmt.Errorf("dispatchaws: %w", err)
	}
	sfnClient, err := stepfunctions.NewClient(ctx, cfg.AWSRegion)
	if err != nil {
		return nil, fmt.Errorf("dispatchaws: %w", err)
	}
	return dispatch.New(status,
		sqs.New(sqsClient, sqs.Config{
			EdgeExtractionQueueURL:   cfg.EdgeExtractionQueueURL,
			CanonicalizationQueueURL: cfg.CanonicalizationQueueURL,
		}),
		stepfunctions.New(sfnClient, stepfunctions.Config{
			DocumentIndexingStateMachineARN:     cfg.DocumentIndexingStateMachineARN,
			EntityExtractionStateMachineARN:     cfg.EntityExtractionStateMachineARN,
			RegionClassificationStateMachineARN: cfg.RegionClassificationStateMachineARN,
		}),
	), nil
}
