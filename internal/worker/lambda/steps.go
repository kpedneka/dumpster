package lambda

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

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

// HandleStep runs one state machine step.
func (s *StepHandler) HandleStep(_ context.Context, _ json.RawMessage) (any, error) {
	return nil, errors.New("worker/lambda: HandleStep is not implemented")
}
