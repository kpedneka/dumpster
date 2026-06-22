package account_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/account"
	accountmock "github.com/kunalpednekar/dumpster/internal/account/mock"
	"github.com/kunalpednekar/dumpster/internal/auth"
	authmock "github.com/kunalpednekar/dumpster/internal/auth/mock"
	emailmock "github.com/kunalpednekar/dumpster/internal/email/mock"
)

var fixedNow = time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

func newTestSweep(users auth.LocalUserStore, deleter account.AccountDeleter, emails *emailmock.Sender) *account.Sweep {
	sweep := account.NewSweep(users, deleter, emails)
	sweep.Now = func() time.Time { return fixedNow }
	return sweep
}

func seedAt(t *testing.T, users *authmock.UserStore, age time.Duration, warned bool) *auth.User {
	t.Helper()
	id := uuid.New()
	u := &auth.User{
		ID:          id,
		ClerkUserID: "clerk_" + id.String(),
		Email:       id.String() + "@example.com",
		CreatedAt:   fixedNow.Add(-age),
	}
	if warned {
		warnedAt := fixedNow.Add(-time.Hour)
		u.WarningSentAt = &warnedAt
	}
	users.Seed(u)
	return u
}

func TestSweep_boundaries(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		name        string
		age         time.Duration
		warned      bool
		wantWarned  bool
		wantDeleted bool
	}{
		{"day0", 0, false, false, false},
		{"day5", 5 * day, false, false, false},
		{"day6_exact", 6 * day, false, true, false},
		{"day6_9", time.Duration(6.9 * float64(day)), false, true, false},
		{"day7_exact", 7 * day, false, false, true},
		{"day7_1", time.Duration(7.1 * float64(day)), false, false, true},
		{"day10_neverWarned_selfHeals", 10 * day, false, false, true},
		{"day6_alreadyWarned_idempotent", 6 * day, true, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			users := authmock.NewUserStore()
			u := seedAt(t, users, tc.age, tc.warned)
			deleter := accountmock.NewDeleter()
			emails := emailmock.New()
			sweep := newTestSweep(users, deleter, emails)

			result, err := sweep.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			gotWarned := emails.SentTo(u.Email) > 0
			if gotWarned != tc.wantWarned {
				t.Errorf("warned: got %v, want %v (result=%+v)", gotWarned, tc.wantWarned, result)
			}
			gotDeleted := deleter.Deleted(u.ID)
			if gotDeleted != tc.wantDeleted {
				t.Errorf("deleted: got %v, want %v (result=%+v)", gotDeleted, tc.wantDeleted, result)
			}
		})
	}
}

func TestSweep_warningIdempotency_acrossRuns(t *testing.T) {
	users := authmock.NewUserStore()
	u := seedAt(t, users, 6*24*time.Hour, false)
	deleter := accountmock.NewDeleter()
	emails := emailmock.New()
	sweep := newTestSweep(users, deleter, emails)

	if _, err := sweep.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if emails.SentTo(u.Email) != 1 {
		t.Fatalf("expected 1 warning email after first run, got %d", emails.SentTo(u.Email))
	}

	if _, err := sweep.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if emails.SentTo(u.Email) != 1 {
		t.Errorf("expected no additional warning email on second run, still got %d", emails.SentTo(u.Email))
	}
}

func TestSweep_mixedBatch(t *testing.T) {
	users := authmock.NewUserStore()
	fresh := seedAt(t, users, 1*24*time.Hour, false)
	toWarn := seedAt(t, users, 6*24*time.Hour, false)
	toDelete := seedAt(t, users, 8*24*time.Hour, false)
	deleter := accountmock.NewDeleter()
	emails := emailmock.New()
	sweep := newTestSweep(users, deleter, emails)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Warned != 1 || result.Deleted != 1 {
		t.Errorf("result: got %+v, want Warned=1 Deleted=1", result)
	}
	if emails.SentTo(fresh.Email) != 0 {
		t.Error("fresh account should not be warned")
	}
	if emails.SentTo(toWarn.Email) != 1 {
		t.Error("day-6 account should be warned")
	}
	if !deleter.Deleted(toDelete.ID) {
		t.Error("day-8 account should be deleted")
	}
	if deleter.Deleted(toWarn.ID) || deleter.Deleted(fresh.ID) {
		t.Error("only the day-8 account should be deleted")
	}
}

func TestSweep_deletionErrorIsolatedFromRestOfBatch(t *testing.T) {
	users := authmock.NewUserStore()
	failing := seedAt(t, users, 8*24*time.Hour, false)
	ok := seedAt(t, users, 8*24*time.Hour, false)
	deleter := accountmock.NewDeleter()
	deleter.ErrFor = map[uuid.UUID]error{failing.ID: errors.New("clerk: backend unavailable")}
	emails := emailmock.New()
	sweep := newTestSweep(users, deleter, emails)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run should not return a top-level error for a per-account failure: %v", err)
	}

	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 collected error, got %d: %v", len(result.Errors), result.Errors)
	}
	if result.Deleted != 1 {
		t.Errorf("expected the non-failing account to still be deleted, got Deleted=%d", result.Deleted)
	}
	if !deleter.Deleted(ok.ID) {
		t.Error("non-failing account should still have been deleted")
	}
	if deleter.Deleted(failing.ID) {
		t.Error("failing account should not be recorded as deleted")
	}
}

func TestSweep_markWarningSentCalledOncePerWarnedUser(t *testing.T) {
	users := authmock.NewUserStore()
	u := seedAt(t, users, 6*24*time.Hour, false)
	deleter := accountmock.NewDeleter()
	emails := emailmock.New()
	sweep := newTestSweep(users, deleter, emails)

	if _, err := sweep.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := users.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WarningSentAt == nil {
		t.Error("expected WarningSentAt to be set after a warning is sent")
	}
}
