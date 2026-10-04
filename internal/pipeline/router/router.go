// Package router sends each pipeline Lambda event to its job type's handler.
package router

import (
	"context"
	"encoding/json"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Handler handles one raw event.
type Handler func(ctx context.Context, raw json.RawMessage) (any, error)

// Typed adapts a typed handler.
func Typed[E any](h func(context.Context, E) (any, error)) Handler { return nil }

// Router routes events.
type Router struct{}

// New returns a Router.
func New() *Router { return &Router{} }

// Register adds a handler.
func (r *Router) Register(jobType queue.JobType, h Handler) *Router { return r }

// Route routes raw.
func (r *Router) Route(ctx context.Context, raw json.RawMessage) (any, error) { return nil, nil }
