// Package mock provides a test double for router.Router.
package mock

import (
	"context"

	"github.com/kunalpednekar/dumpster/internal/router"
)

// Router is a configurable test double for router.Router.
type Router struct {
	qt  router.QueryType
	Err error
}

// New returns a Router that always returns qt and a nil error.
func New(qt router.QueryType) *Router {
	return &Router{qt: qt}
}

// Route returns the configured QueryType and error.
func (r *Router) Route(_ context.Context, _ string) (router.QueryType, error) {
	return r.qt, r.Err
}

var _ router.Router = (*Router)(nil)
