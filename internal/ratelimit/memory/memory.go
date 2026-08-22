// Package memory provides a fixed-window in-memory rate limiter. The window
// boundary and request count are reset atomically when the window expires,
// so an IP that hit its limit in window N can make requests again in window N+1.
package memory

import (
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
// window, consuming one slot. Returns false when the limit is reached.
// A new window starts automatically once the previous one has elapsed.
func (s *Store) Allow(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.Now()
	e, ok := s.counters[key]
	if !ok || now.After(e.windowEnd) {
		s.counters[key] = &entry{count: 1, windowEnd: now.Add(s.window)}
		return true
	}
	if e.count >= s.limit {
		return false
	}
	e.count++
	return true
}

var _ ratelimit.Limiter = (*Store)(nil)
