package stepfunctions_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/queue/stepfunctions"
	"github.com/kunalpednekar/dumpster/internal/queue/stepfunctions/mock"
)

func testConfig() stepfunctions.Config {
	return stepfunctions.Config{
		DocumentIndexingStateMachineARN:     "arn:aws:states:us-east-1:123456789012:stateMachine:document-indexing",
		EntityExtractionStateMachineARN:     "arn:aws:states:us-east-1:123456789012:stateMachine:entity-extraction",
		RegionClassificationStateMachineARN: "arn:aws:states:us-east-1:123456789012:stateMachine:region-classification",
	}
}

func TestDispatch_StartsNamedExecutionOnTheJobTypesStateMachine(t *testing.T) {
	cfg := testConfig()
	cases := map[queue.JobType]string{
		queue.JobTypeDocumentIndexing:     cfg.DocumentIndexingStateMachineARN,
		queue.JobTypeEntityExtraction:     cfg.EntityExtractionStateMachineARN,
		queue.JobTypeRegionClassification: cfg.RegionClassificationStateMachineARN,
	}
	for jobType, wantARN := range cases {
		t.Run(string(jobType), func(t *testing.T) {
			client := mock.New()
			starter := stepfunctions.New(client, cfg)
			run := queue.JobRun{Type: jobType, DocumentID: uuid.New(), UserID: uuid.New(), Attempt: 3}

			if err := starter.Dispatch(context.Background(), run); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}

			started := client.Started()
			if len(started) != 1 {
				t.Fatalf("StartExecution calls = %d, want 1", len(started))
			}
			if started[0].StateMachineARN != wantARN {
				t.Errorf("state machine = %q, want %q", started[0].StateMachineARN, wantARN)
			}
			wantName := fmt.Sprintf("%s-%s-3", jobType, run.DocumentID)
			if started[0].Name != wantName {
				t.Errorf("execution name = %q, want %q", started[0].Name, wantName)
			}

			var input map[string]any
			if err := json.Unmarshal([]byte(started[0].Input), &input); err != nil {
				t.Fatalf("unmarshal input: %v", err)
			}
			want := map[string]any{
				"type":        string(jobType),
				"document_id": run.DocumentID.String(),
				"user_id":     run.UserID.String(),
				"attempt":     float64(3),
			}
			for k, v := range want {
				if input[k] != v {
					t.Errorf("input[%q] = %v, want %v", k, input[k], v)
				}
			}
		})
	}
}

// Execution names are at most 80 characters. The longest job type plus a
// UUID and a multi-digit attempt has to fit.
func TestExecutionName_FitsTheStepFunctionsLimit(t *testing.T) {
	name := stepfunctions.ExecutionName(queue.JobRun{
		Type: queue.JobTypeRegionClassification, DocumentID: uuid.New(), Attempt: 9999,
	})
	if len(name) > 80 {
		t.Errorf("len(%q) = %d, want <= 80", name, len(name))
	}
}

func TestDispatch_ExecutionAlreadyExists_IsSuccess(t *testing.T) {
	client := mock.New()
	client.StartErr = fmt.Errorf("wrapped: %w", stepfunctions.ErrExecutionAlreadyExists)
	starter := stepfunctions.New(client, testConfig())

	err := starter.Dispatch(context.Background(), queue.JobRun{
		Type: queue.JobTypeDocumentIndexing, DocumentID: uuid.New(), UserID: uuid.New(), Attempt: 1,
	})
	if err != nil {
		t.Errorf("Dispatch err = %v, want nil for an execution that already exists", err)
	}
}

func TestDispatch_StartError_IsReturned(t *testing.T) {
	boom := errors.New("throttled")
	client := mock.New()
	client.StartErr = boom
	starter := stepfunctions.New(client, testConfig())

	err := starter.Dispatch(context.Background(), queue.JobRun{
		Type: queue.JobTypeEntityExtraction, DocumentID: uuid.New(), UserID: uuid.New(), Attempt: 1,
	})
	if !errors.Is(err, boom) {
		t.Errorf("Dispatch err = %v, want it to wrap %v", err, boom)
	}
}

func TestDispatch_UnsupportedJobType_StartsNothing(t *testing.T) {
	client := mock.New()
	starter := stepfunctions.New(client, testConfig())

	err := starter.Dispatch(context.Background(), queue.JobRun{
		Type: queue.JobTypeEdgeExtraction, DocumentID: uuid.New(), UserID: uuid.New(), Attempt: 1,
	})
	if err == nil {
		t.Error("Dispatch err = nil, want an error for a job type with no state machine")
	}
	if n := len(client.Started()); n != 0 {
		t.Errorf("StartExecution calls = %d, want 0", n)
	}
}

func TestDispatch_UnconfiguredStateMachine_StartsNothing(t *testing.T) {
	client := mock.New()
	cfg := testConfig()
	cfg.RegionClassificationStateMachineARN = ""
	starter := stepfunctions.New(client, cfg)

	err := starter.Dispatch(context.Background(), queue.JobRun{
		Type: queue.JobTypeRegionClassification, DocumentID: uuid.New(), UserID: uuid.New(), Attempt: 1,
	})
	if err == nil {
		t.Error("Dispatch err = nil, want an error for an unconfigured state machine ARN")
	}
	if n := len(client.Started()); n != 0 {
		t.Errorf("StartExecution calls = %d, want 0", n)
	}
}
