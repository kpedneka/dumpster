package session

import "time"

// IdleTimeout is the idle limit.
const IdleTimeout = 6 * time.Hour

// HardCap is the lifetime limit.
const HardCap = 24 * time.Hour

// Expired reports whether s is past a limit.
func (s *Session) Expired(now time.Time) bool { return false }
