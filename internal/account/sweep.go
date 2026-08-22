package account

import (
	"context"
	"fmt"
	"time"

	"github.com/kunalpednekar/dumpster/internal/session"
)

// TTLDays is the demo account TTL, anchored at the session's CreatedAt.
// Exported so other packages (e.g. the in-app banner endpoint) can compute
// the same boundary Sweep uses, rather than duplicating the value.
const TTLDays = 7

// WarningWindowDays is when the day-6 pre-deletion warning starts.
const WarningWindowDays = 6

// SweepResult summarizes one Sweep.Run call.
type SweepResult struct {
	Warned  int
	Deleted int
	Errors  []error
}

// Sweep finds demo sessions at their day-6 (warning) and day-7 (hard-delete)
// TTL boundaries, anchored at CreatedAt, and acts on them.
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
//   - age >= 7 days since CreatedAt: hard-deletes it via the AccountDeleter.
//     ">=" rather than "==" makes this self-healing if a run is missed.
//   - 6 <= age < 7 days and WarnedAt is unset: stamps WarnedAt. The window
//     (not exact equality) means a missed day-6 run still marks warned before
//     the day-7 delete fires; WarnedAt makes repeated runs within the window
//     idempotent. The two conditions are mutually exclusive by construction.
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
		days := int(now.Sub(sess.CreatedAt).Hours() / 24)
		switch {
		case days >= TTLDays:
			if err := s.deleter.Delete(ctx, sess); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("delete session %s: %w", sess.ID, err))
				continue
			}
			result.Deleted++
		case days >= WarningWindowDays && sess.WarnedAt == nil:
			if err := s.sessions.MarkWarned(ctx, sess.ID); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("mark warned for %s: %w", sess.ID, err))
				continue
			}
			result.Warned++
		}
	}
	return result, nil
}
