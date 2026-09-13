// Package mock provides a test double for ratelimit.Limiter.
package mock

import (
	"context"

	"github.com/kunalpednekar/dumpster/internal/ratelimit"
)

// Limiter is a controllable ratelimit.Limiter for use in tests.
type Limiter struct {
	// AllowFn is called for every Allow invocation. When nil, all requests
	// are allowed.
	AllowFn func(ctx context.Context, key string) (bool, error)
}

// Allow delegates to AllowFn and returns (true, nil) when AllowFn is nil.
func (m *Limiter) Allow(ctx context.Context, key string) (bool, error) {
	if m.AllowFn == nil {
		return true, nil
	}
	return m.AllowFn(ctx, key)
}

// New returns a Limiter that allows all requests.
func New() *Limiter { return &Limiter{} }

// Deny returns a Limiter that blocks all requests.
func Deny() *Limiter {
	return &Limiter{AllowFn: func(_ context.Context, _ string) (bool, error) { return false, nil }}
}

// Failing returns a Limiter whose Allow always returns err, for exercising
// ratelimit.Middleware's fail-open behavior.
func Failing(err error) *Limiter {
	return &Limiter{AllowFn: func(_ context.Context, _ string) (bool, error) { return false, err }}
}

var _ ratelimit.Limiter = (*Limiter)(nil)
