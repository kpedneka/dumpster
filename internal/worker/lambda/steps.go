// Package lambda adapts worker.Handler to the stateless-jobs Lambda's
// state machine steps -- the seam that keeps internal/worker itself
// Lambda-agnostic. The handlers it runs (EdgeHandler,
// CanonicalizationHandler) know nothing about Step Functions.
//
// Edge extraction and canonicalization each have a state machine
// (terraform/modules/pipeline) with two Lambda steps:
//
//   - run executes the job. Its Retry retries a TransientError; its Catch
//     sends every other failure, and a transient one that ran out of
//     retries, to record_failure.
//   - record_failure stores the caught error in the status store, which is
//     what the UI's retry button acts on.
//
// This replaces SQS queues with event-source mappings, which polled around
// the clock and billed every empty receive.
package lambda

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

// Step names, as each state machine's Task Parameters send them.
const (
	StepRun           = "run"
	StepRecordFailure = "record_failure"
)

// StatusWriter is the part of jobstatus.Writer the steps use.
type StatusWriter interface {
	MarkProcessing(ctx context.Context, key jobstatus.Key, attempt int) error
	MarkSucceeded(ctx context.Context, key jobstatus.Key, attempt int) error
	MarkFailed(ctx context.Context, key jobstatus.Key, attempt int, reason string) error
}

// Event is the payload every Task sends: the step to run and the
// execution's input (plus, for record_failure, the Catch's error).
type Event struct {
	Step string `json:"step"`
	Run  Input  `json:"run"`
}

// Input is a state machine's execution input, as
// internal/queue/stepfunctions starts it.
type Input struct {
	Type       queue.JobType `json:"type"`
	DocumentID uuid.UUID     `json:"document_id"`
	UserID     uuid.UUID     `json:"user_id"`
	Attempt    int           `json:"attempt"`
	// Error is what run's Catch recorded; set only for record_failure.
	Error *pipeline.StepError `json:"error,omitempty"`
}

func (in Input) key() jobstatus.Key {
	return jobstatus.Key{UserID: in.UserID, DocumentID: in.DocumentID, JobType: in.Type}
}

func (in Input) job() *queue.Job {
	return &queue.Job{Type: in.Type, DocumentID: in.DocumentID, UserID: in.UserID}
}

// StepHandler runs the stateless-jobs Lambda's state machine steps.
type StepHandler struct {
	handlers map[queue.JobType]worker.Handler
	status   StatusWriter
}

// NewStepHandler returns a StepHandler that records each job's progress in
// status.
func NewStepHandler(status StatusWriter) *StepHandler {
	return &StepHandler{handlers: make(map[queue.JobType]worker.Handler), status: status}
}

// Register makes handler run jobs of jobType, returning the StepHandler
// for chaining.
func (s *StepHandler) Register(jobType queue.JobType, handler worker.Handler) *StepHandler {
	s.handlers[jobType] = handler
	return s
}

// HandleStep decodes raw and runs its step. It's the Lambda's entrypoint.
// The returned error's outermost type name is what the state machine's
// Retry matches on, so it's prepared with pipeline.ForLambda.
func (s *StepHandler) HandleStep(ctx context.Context, raw json.RawMessage) (any, error) {
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, fmt.Errorf("worker/lambda: decode step event: %w", err)
	}
	handler, ok := s.handlers[ev.Run.Type]
	if !ok {
		return nil, fmt.Errorf("worker/lambda: no handler registered for job type %q", ev.Run.Type)
	}
	ctx = auth.WithUserID(ctx, ev.Run.UserID)
	switch ev.Step {
	case StepRun:
		return nil, pipeline.ForLambda(s.run(ctx, handler, ev.Run))
	case StepRecordFailure:
		return nil, pipeline.ForLambda(s.recordFailure(ctx, handler, ev.Run))
	default:
		return nil, fmt.Errorf("worker/lambda: unknown step %q", ev.Step)
	}
}

// run marks the attempt processing, runs it and marks it succeeded. A
// superseded attempt is skipped, and a job the publisher never recorded
// runs untracked. A failure is returned for the state machine to retry or
// catch; run never records it.
func (s *StepHandler) run(ctx context.Context, handler worker.Handler, in Input) error {
	tracked := true
	err := s.status.MarkProcessing(ctx, in.key(), in.Attempt)
	switch {
	case pipeline.Superseded(err):
		log.Printf("worker/lambda: skipping %s for document %s: attempt %d is no longer current", in.Type, in.DocumentID, in.Attempt)
		return nil
	case errors.Is(err, jobstatus.ErrNotFound):
		tracked = false
	case err != nil:
		return fmt.Errorf("worker/lambda: mark %s processing for document %s: %w", in.Type, in.DocumentID, pipeline.Transient(err))
	}

	if err := handler.Handle(ctx, in.job()); err != nil {
		return fmt.Errorf("worker/lambda: handle %s job for document %s: %w", in.Type, in.DocumentID, err)
	}
	if !tracked {
		return nil
	}
	if err := s.status.MarkSucceeded(ctx, in.key(), in.Attempt); err != nil && !pipeline.Superseded(err) {
		return fmt.Errorf("worker/lambda: mark %s succeeded for document %s: %w", in.Type, in.DocumentID, pipeline.Transient(err))
	}
	return nil
}

// recordFailure stores the caught error as the attempt's failure reason
// and calls the handler's OnFailed. An attempt that's been superseded or
// already finished is left alone, as is a job that was never recorded.
func (s *StepHandler) recordFailure(ctx context.Context, handler worker.Handler, in Input) error {
	err := s.status.MarkFailed(ctx, in.key(), in.Attempt, in.Error.Reason())
	switch {
	case pipeline.Superseded(err):
		return nil
	case errors.Is(err, jobstatus.ErrNotFound):
	case err != nil:
		return fmt.Errorf("worker/lambda: record %s failure for document %s: %w", in.Type, in.DocumentID, pipeline.Transient(err))
	}
	handler.OnFailed(ctx, in.job())
	log.Printf("worker/lambda: %s for document %s failed permanently (attempt %d): %s", in.Type, in.DocumentID, in.Attempt, in.Error.Reason())
	return nil
}

// Definition is the state machine definition (Amazon States Language)
// that edge extraction and canonicalization each deploy, with ${name}
// placeholders for DefinitionVars that Terraform fills with templatefile()
// on this same file (terraform/modules/pipeline).
//
//go:embed statemachine.asl.json
var Definition string

// DefinitionVars are the placeholders in Definition: the stateless-jobs
// Lambda.
var DefinitionVars = []string{"lambda_arn"}
