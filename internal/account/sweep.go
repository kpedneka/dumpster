package account

import (
	"context"
	"fmt"
	"time"

	"github.com/kunalpednekar/dumpster/internal/session"
)

// IdleTimeout is the period of inactivity after which a session is hard-deleted.
// Exported so other packages (e.g. the in-app banner endpoint) can compute the
// same expiry boundary the sweep uses, rather than duplicating the value.
const IdleTimeout = 2 * time.Hour

// HardCap is the maximum session lifetime, anchored at CreatedAt, regardless
// of activity. Exported for the same reason as IdleTimeout.
const HardCap = 24 * time.Hour

// WarningLeadTime is how far before either expiry clock fires that a session
// receives its one pre-deletion warning.
const WarningLeadTime = 15 * time.Minute

// SweepResult summarizes one Sweep.Run call.
type SweepResult struct {
	Warned  int
	Deleted int
	Errors  []error
}

// Sweep finds demo sessions approaching or past their idle-timeout or hard-cap
// expiry and acts on them.
type Sweep struct {
	sessions session.SessionStore
	deleter  AccountDeleter
	// Now returns the current time and defaults to time.Now. Tests override
	// it for deterministic boundary checks.
	Now func() time.Time
}

// NewSweep returns a Sweep wired to the given stores.
func NewSweep(sessions session.SessionStore, deleter AccountDeleter) *Sweep {
	return &Sweep{sessions: sessions, deleter: deleter, Now: time.Now}
}

// Run scans every session once and, per session:
//   - now-CreatedAt >= HardCap OR now-LastActiveAt >= IdleTimeout: hard-deletes it.
//     ">=" makes this self-healing if a run is missed.
//   - Either clock within WarningLeadTime of firing and WarnedAt is unset: stamps WarnedAt once.
//     Delete and warn are mutually exclusive: a session due for deletion is deleted, never warned.
//
// Per-session errors are collected into the result rather than aborting the
// rest of the batch.
func (s *Sweep) Run(ctx context.Context) (SweepResult, error) {
	sessions, err := s.sessions.ListForSweep(ctx)
	if err != nil {
		return SweepResult{}, fmt.Errorf("account: sweep: list sessions: %w", err)
	}

	now := s.Now()
	var result SweepResult
	for _, sess := range sessions {
		hardAge := now.Sub(sess.CreatedAt)
		idleAge := now.Sub(sess.LastActiveAt)

		if hardAge >= HardCap || idleAge >= IdleTimeout {
			if err := s.deleter.Delete(ctx, sess); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("delete session %s: %w", sess.ID, err))
				continue
			}
			result.Deleted++
			continue
		}

		if sess.WarnedAt == nil {
			timeToHardExp := HardCap - hardAge
			timeToIdleExp := IdleTimeout - idleAge
			if timeToHardExp <= WarningLeadTime || timeToIdleExp <= WarningLeadTime {
				if err := s.sessions.MarkWarned(ctx, sess.ID); err != nil {
					result.Errors = append(result.Errors, fmt.Errorf("mark warned for %s: %w", sess.ID, err))
					continue
				}
				result.Warned++
			}
		}
	}
	return result, nil
}
