// Package router lets one Lambda function serve every pipeline state
// machine (document indexing, entity extraction, region classification).
// Every Lambda payload those state machines send carries the job type at
// run.type, so the router decodes just that field and hands the raw event
// to the handler registered for it.
package router

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Handler handles one raw Lambda event for a job type.
type Handler func(ctx context.Context, raw json.RawMessage) (any, error)

// Typed adapts a pipeline package's Handle method, which takes its own
// Event type, into a Handler that decodes the raw event first.
func Typed[E any](h func(context.Context, E) (any, error)) Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var ev E
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("router: decode event: %w", err)
		}
		return h(ctx, ev)
	}
}

// Router sends events to the Handler for their job type.
type Router struct {
	handlers map[queue.JobType]Handler
}

// New returns a Router with no handlers registered.
func New() *Router {
	return &Router{handlers: make(map[queue.JobType]Handler)}
}

// Register makes h handle events for jobType, returning the Router for
// chaining.
func (r *Router) Register(jobType queue.JobType, h Handler) *Router {
	r.handlers[jobType] = h
	return r
}

// envelope is the part of every pipeline event the router reads.
type envelope struct {
	Run struct {
		Type queue.JobType `json:"type"`
	} `json:"run"`
}

// Route decodes raw's job type and calls its handler. The handler's
// result and error are returned unchanged: the error's type name is what
// a state machine's Retry matches on, so it must not be wrapped here.
func (r *Router) Route(ctx context.Context, raw json.RawMessage) (any, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("router: decode job type: %w", err)
	}
	h, ok := r.handlers[env.Run.Type]
	if !ok {
		return nil, fmt.Errorf("router: no handler for job type %q", env.Run.Type)
	}
	return h(ctx, raw)
}
