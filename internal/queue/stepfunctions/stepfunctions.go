// Package stepfunctions starts Step Functions executions for the job types
// that run on AWS Batch: document indexing, entity extraction and region
// classification. The publisher starts each execution directly, with no
// SQS queue or starter Lambda in front. A Standard execution is already a
// durable unit of work, and the Batch job queue already buffers compute.
//
// Each execution is named {job_type}-{document_id}-{attempt}. Step
// Functions rejects a second execution with a name that's already in use
// (for 90 days), so a duplicate start of the same attempt is harmless and
// treated as success. The attempt number, from internal/jobstatus, makes
// a user retry a new name.
package stepfunctions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// ErrExecutionAlreadyExists is returned by Client.StartExecution when an
// execution with the same name already exists on the state machine.
var ErrExecutionAlreadyExists = errors.New("stepfunctions: execution already exists")

// Client is the narrow Step Functions capability Starter needs, satisfied
// by the real AWS SDK client (see NewClient) or a test double
// (internal/queue/stepfunctions/mock).
type Client interface {
	// StartExecution starts a Standard execution named name on the state
	// machine stateMachineARN, with input as its JSON input. It returns an
	// error wrapping ErrExecutionAlreadyExists if the name is taken.
	StartExecution(ctx context.Context, stateMachineARN, name, input string) error
}

// Config maps each Batch-backed job type to its state machine. Named
// fields rather than a map, so a missing one is visible where Config is
// built.
type Config struct {
	DocumentIndexingStateMachineARN     string
	EntityExtractionStateMachineARN     string
	RegionClassificationStateMachineARN string
}

// Starter starts one execution per job run.
type Starter struct {
	client Client
	cfg    Config
}

// New returns a Starter that starts executions through client on the state
// machines cfg names.
func New(client Client, cfg Config) *Starter {
	return &Starter{client: client, cfg: cfg}
}

// input is the JSON every state machine receives as its execution input.
type input struct {
	Type       queue.JobType `json:"type"`
	DocumentID uuid.UUID     `json:"document_id"`
	UserID     uuid.UUID     `json:"user_id"`
	Attempt    int           `json:"attempt"`
}

// ExecutionName returns run's execution name, {job_type}-{document_id}-
// {attempt}. The longest job type plus a UUID and a four-digit attempt is
// well under Step Functions' 80-character limit.
func ExecutionName(run queue.JobRun) string {
	return fmt.Sprintf("%s-%s-%d", run.Type, run.DocumentID, run.Attempt)
}

// Dispatch starts run's execution on its job type's state machine. An
// execution that already exists under the same name counts as started.
func (s *Starter) Dispatch(ctx context.Context, run queue.JobRun) error {
	arn, err := s.stateMachineFor(run.Type)
	if err != nil {
		return err
	}
	body, err := json.Marshal(input{Type: run.Type, DocumentID: run.DocumentID, UserID: run.UserID, Attempt: run.Attempt})
	if err != nil {
		return fmt.Errorf("stepfunctions: marshal input for %s: %w", ExecutionName(run), err)
	}
	err = s.client.StartExecution(ctx, arn, ExecutionName(run), string(body))
	if err != nil && !errors.Is(err, ErrExecutionAlreadyExists) {
		return fmt.Errorf("stepfunctions: start %s: %w", ExecutionName(run), err)
	}
	return nil
}

func (s *Starter) stateMachineFor(jobType queue.JobType) (string, error) {
	var arn string
	switch jobType {
	case queue.JobTypeDocumentIndexing:
		arn = s.cfg.DocumentIndexingStateMachineARN
	case queue.JobTypeEntityExtraction:
		arn = s.cfg.EntityExtractionStateMachineARN
	case queue.JobTypeRegionClassification:
		arn = s.cfg.RegionClassificationStateMachineARN
	default:
		return "", fmt.Errorf("stepfunctions: no state machine for job type %q", jobType)
	}
	if arn == "" {
		return "", fmt.Errorf("stepfunctions: state machine ARN for %q is not configured", jobType)
	}
	return arn, nil
}
