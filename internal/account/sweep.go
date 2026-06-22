package account

import (
	"context"
	"fmt"
	"time"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/email"
)

// TTLDays is the demo account TTL, anchored at signup (CreatedAt). Exported
// so other packages (e.g. the in-app banner endpoint) can compute the same
// boundary Sweep uses, rather than duplicating the value.
const TTLDays = 7

// WarningWindowDays is when the day-6 pre-deletion warning starts.
const WarningWindowDays = 6

// AccountDeleter hard-deletes a user. Implemented by Deleter; Sweep depends
// on the interface rather than the concrete type so it can be tested
// without driving a real Clerk/R2/DB chain.
type AccountDeleter interface {
	Delete(ctx context.Context, u *auth.User) error
}

// SweepResult summarizes one Sweep.Run call.
type SweepResult struct {
	Warned  int
	Deleted int
	Errors  []error
}

// Sweep finds demo accounts at their day-6 (warning) and day-7 (hard-delete)
// TTL boundaries, anchored at signup (CreatedAt), and acts on them.
type Sweep struct {
	users   auth.LocalUserStore
	deleter AccountDeleter
	emails  email.Sender
	// Now returns the current time and defaults to time.Now. Tests override
	// it for deterministic boundary checks.
	Now func() time.Time
}

// NewSweep returns a Sweep wired to the given stores.
func NewSweep(users auth.LocalUserStore, deleter AccountDeleter, emails email.Sender) *Sweep {
	return &Sweep{users: users, deleter: deleter, emails: emails, Now: time.Now}
}

// Run scans every local user once and, per account:
//   - age >= 7 days since CreatedAt: hard-deletes it via the AccountDeleter.
//     ">=" rather than "==" makes this self-healing if a run is missed — an
//     account doesn't get permanently skipped just because the exact day-7
//     run didn't happen.
//   - 6 <= age < 7 days and WarningSentAt is unset: sends the day-6 warning
//     email and stamps WarningSentAt. The window (not exact equality) means
//     a missed day-6 run still warns before the day-7 delete fires on a
//     later run; WarningSentAt makes repeated runs within the window
//     idempotent. The two conditions are mutually exclusive by construction.
//
// Per-account errors are collected into the result rather than aborting the
// rest of the batch.
func (s *Sweep) Run(ctx context.Context) (SweepResult, error) {
	users, err := s.users.ListForSweep(ctx)
	if err != nil {
		return SweepResult{}, fmt.Errorf("account: sweep: list users: %w", err)
	}

	now := s.Now()
	var result SweepResult
	for _, u := range users {
		days := int(now.Sub(u.CreatedAt).Hours() / 24)
		switch {
		case days >= TTLDays:
			if err := s.deleter.Delete(ctx, u); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("delete user %s: %w", u.ID, err))
				continue
			}
			result.Deleted++
		case days >= WarningWindowDays && u.WarningSentAt == nil:
			deletesAt := u.CreatedAt.AddDate(0, 0, TTLDays)
			if err := s.emails.Send(ctx, email.WarningMessage(u.Email, deletesAt)); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("warn user %s: %w", u.ID, err))
				continue
			}
			if err := s.users.MarkWarningSent(ctx, u.ID); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("mark warning sent for %s: %w", u.ID, err))
				continue
			}
			result.Warned++
		}
	}
	return result, nil
}
