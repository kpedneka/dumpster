// Package stepfunctions starts Step Functions executions for the job types
// that run on AWS Batch.
package stepfunctions

import (
	"context"
	"errors"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// ErrExecutionAlreadyExists is returned by Client.StartExecution when an
// execution with the same name already exists.
var ErrExecutionAlreadyExists = errors.New("stepfunctions: execution already exists")

// Client is the narrow Step Functions capability Starter needs.
type Client interface {
	StartExecution(ctx context.Context, stateMachineARN, name, input string) error
}

// Config maps each job type to its state machine ARN.
type Config struct {
	DocumentIndexingStateMachineARN     string
	EntityExtractionStateMachineARN     string
	RegionClassificationStateMachineARN string
}

// Starter starts executions.
type Starter struct {
	client Client
	cfg    Config
}

// New returns a Starter.
func New(client Client, cfg Config) *Starter { return &Starter{client: client, cfg: cfg} }

// ExecutionName returns the execution name for run.
func ExecutionName(run queue.JobRun) string { return "" }

// Dispatch starts an execution for run.
func (s *Starter) Dispatch(ctx context.Context, run queue.JobRun) error { return nil }
