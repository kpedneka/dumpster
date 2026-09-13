// Package memory provides a fixed-window in-memory rate limiter. The window
// boundary and request count are reset atomically when the window expires,
// so an IP that hit its limit in window N can make requests again in window N+1.
//
// Correct only for a single process: the counter lives in this process's own
// memory, so N horizontally-scaled replicas each enforce the configured
// limit independently, making the effective limit N times more permissive
// and letting a client reset their own budget just by landing on a
// different replica behind the load balancer. Use
// internal/ratelimit/pgstore instead once more than one API replica is
// running.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/kunalpednekar/dumpster/internal/ratelimit"
)

// Store is a thread-safe, fixed-window rate limiter keyed by arbitrary string
// identifiers (typically client IPs).
type Store struct {
	mu       sync.Mutex
	counters map[string]*entry
	limit    int
	window   time.Duration
	// Now returns the current time. Overridable in tests for deterministic
	// window boundary checks without real sleeps.
	Now func() time.Time
}

type entry struct {
	count     int
	windowEnd time.Time
}

// New returns a Store that allows up to limit requests per window duration.
func New(limit int, window time.Duration) *Store {
	return &Store{
		counters: make(map[string]*entry),
		limit:    limit,
		window:   window,
		Now:      time.Now,
	}
}

// Allow returns true if key is within the configured limit for the current
// window, consuming one slot. Returns false when the limit is reached. A new
// window starts automatically once the previous one has elapsed. ctx is
// accepted only to satisfy ratelimit.Limiter -- this implementation is pure
// in-memory state and never blocks or fails.
func (s *Store) Allow(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.Now()
	e, ok := s.counters[key]
	if !ok || now.After(e.windowEnd) {
		s.counters[key] = &entry{count: 1, windowEnd: now.Add(s.window)}
		return true, nil
	}
	if e.count >= s.limit {
		return false, nil
	}
	e.count++
	return true, nil
}

var _ ratelimit.Limiter = (*Store)(nil)
