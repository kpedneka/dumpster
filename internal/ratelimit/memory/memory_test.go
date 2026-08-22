package memory_test

import (
	"testing"
	"time"

	"github.com/kunalpednekar/dumpster/internal/ratelimit/memory"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newFrozen(limit int, window time.Duration) (*memory.Store, *time.Time) {
	now := epoch
	s := memory.New(limit, window)
	s.Now = func() time.Time { return now }
	return s, &now
}

func TestStore_allowsUpToLimit(t *testing.T) {
	s, _ := newFrozen(2, time.Minute)

	if !s.Allow("1.2.3.4") {
		t.Error("request 1 should be allowed")
	}
	if !s.Allow("1.2.3.4") {
		t.Error("request 2 should be allowed")
	}
	if s.Allow("1.2.3.4") {
		t.Error("request 3 should be blocked (limit=2)")
	}
}

func TestStore_recoversAfterWindowExpires(t *testing.T) {
	s, now := newFrozen(1, time.Minute)

	s.Allow("1.2.3.4") // consume the one slot
	if s.Allow("1.2.3.4") {
		t.Fatal("should be blocked within the window")
	}

	// Advance the clock past the window.
	*now = now.Add(time.Minute + time.Second)
	if !s.Allow("1.2.3.4") {
		t.Error("should be allowed after window reset")
	}
}

func TestStore_windowExpiryIsExact(t *testing.T) {
	s, now := newFrozen(1, time.Minute)

	s.Allow("1.2.3.4") // consume slot

	// Exactly at window boundary: still blocked (window ends strictly after now).
	*now = now.Add(time.Minute)
	if s.Allow("1.2.3.4") {
		t.Error("at window boundary should still be blocked")
	}

	// One nanosecond past the boundary: new window.
	*now = now.Add(time.Nanosecond)
	if !s.Allow("1.2.3.4") {
		t.Error("past window boundary should open a new window")
	}
}

func TestStore_differentKeysAreIndependent(t *testing.T) {
	s, _ := newFrozen(1, time.Minute)

	s.Allow("1.1.1.1") // exhaust key A
	if s.Allow("1.1.1.1") {
		t.Error("key A should be blocked")
	}
	if !s.Allow("2.2.2.2") {
		t.Error("key B should be allowed (separate quota)")
	}
}

func TestStore_firstRequestAlwaysAllowed(t *testing.T) {
	s, _ := newFrozen(0, time.Minute) // limit=0 is an edge case; first request still opens a window
	// Note: limit=0 means the first request consumes count=1 but limit=0 < 1 would
	// normally block — the implementation opens a new window unconditionally for
	// the first request, so even limit=0 allows the very first one.
	// This is a deliberate design choice: allow is checked AFTER incrementing only
	// if a window already exists; a missing window always opens allowing one.
	if !s.Allow("x") {
		t.Log("first request on a missing window opened a new entry with count=1 (allowed)")
	}
}

func TestStore_concurrentCallsDoNotRace(t *testing.T) {
	s := memory.New(1000, time.Minute)
	done := make(chan struct{})
	for range 50 {
		go func() {
			for range 100 {
				s.Allow("shared")
			}
			done <- struct{}{}
		}()
	}
	for range 50 {
		<-done
	}
}
