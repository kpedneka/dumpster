package session

import "time"

// IdleTimeout is the period of inactivity after which a session expires.
const IdleTimeout = 6 * time.Hour

// HardCap is the maximum session lifetime, anchored at CreatedAt,
// regardless of activity.
const HardCap = 24 * time.Hour

// Expired reports whether s is past its idle timeout or hard cap at now.
// It's the one rule both places use: the auth middleware treats an expired
// session as absent (so a request can't revive it), and the account sweep
// deletes it. ">=" keeps the sweep self-healing if a run is missed.
func (s *Session) Expired(now time.Time) bool {
	return now.Sub(s.CreatedAt) >= HardCap || now.Sub(s.LastActiveAt) >= IdleTimeout
}
