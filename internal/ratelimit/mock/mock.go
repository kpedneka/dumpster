// Package mock provides a test double for ratelimit.Limiter.
package mock

import "github.com/kunalpednekar/dumpster/internal/ratelimit"

// Limiter is a controllable ratelimit.Limiter for use in tests.
type Limiter struct {
	// AllowFn is called for every Allow invocation. When nil, all requests
	// are allowed.
	AllowFn func(key string) bool
}

// Allow delegates to AllowFn and returns true when AllowFn is nil.
func (m *Limiter) Allow(key string) bool {
	if m.AllowFn == nil {
		return true
	}
	return m.AllowFn(key)
}

// New returns a Limiter that allows all requests.
func New() *Limiter { return &Limiter{} }

// Deny returns a Limiter that blocks all requests.
func Deny() *Limiter {
	return &Limiter{AllowFn: func(_ string) bool { return false }}
}

var _ ratelimit.Limiter = (*Limiter)(nil)
