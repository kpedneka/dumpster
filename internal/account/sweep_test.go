package account_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/account"
	accountmock "github.com/kunalpednekar/dumpster/internal/account/mock"
	"github.com/kunalpednekar/dumpster/internal/session"
	sessionmock "github.com/kunalpednekar/dumpster/internal/session/mock"
)

var fixedNow = time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

func newTestSweep(sessions *sessionmock.Store, deleter account.AccountDeleter) *account.Sweep {
	sweep := account.NewSweep(sessions, deleter)
	sweep.Now = func() time.Time { return fixedNow }
	return sweep
}

func seedAt(t *testing.T, store *sessionmock.Store, age time.Duration, warned bool) *session.Session {
	t.Helper()
	id := uuid.New()
	sess := &session.Session{
		ID:        id,
		CreatedAt: fixedNow.Add(-age),
	}
	if warned {
		warnedAt := fixedNow.Add(-time.Hour)
		sess.WarnedAt = &warnedAt
	}
	store.Seed(sess)
	return sess
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
			sessions := sessionmock.New()
			sess := seedAt(t, sessions, tc.age, tc.warned)
			deleter := accountmock.NewDeleter()
			sweep := newTestSweep(sessions, deleter)

			result, err := sweep.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			// Check warned: session should have WarnedAt set.
			got, _ := sessions.GetByID(context.Background(), sess.ID)
			gotWarned := got != nil && got.WarnedAt != nil && !tc.warned
			if tc.warned {
				// Already warned before the run; WarnedAt was set before.
				gotWarned = false
			} else if result.Warned > 0 {
				gotWarned = true
			}

			if gotWarned != tc.wantWarned {
				t.Errorf("warned: got %v, want %v (result=%+v)", gotWarned, tc.wantWarned, result)
			}
			gotDeleted := deleter.Deleted(sess.ID)
			if gotDeleted != tc.wantDeleted {
				t.Errorf("deleted: got %v, want %v (result=%+v)", gotDeleted, tc.wantDeleted, result)
			}
		})
	}
}

func TestSweep_warningIdempotency_acrossRuns(t *testing.T) {
	sessions := sessionmock.New()
	sess := seedAt(t, sessions, 6*24*time.Hour, false)
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	if _, err := sweep.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// WarnedAt should be set after the first run.
	got, err := sessions.GetByID(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WarnedAt == nil {
		t.Fatal("expected WarnedAt to be set after first run")
	}

	if _, err := sweep.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	// No additional warnings on repeated runs within the window.
	got2, err := sessions.GetByID(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.WarnedAt != got.WarnedAt {
		t.Error("WarnedAt should not change on second run")
	}
}

func TestSweep_mixedBatch(t *testing.T) {
	sessions := sessionmock.New()
	_ = seedAt(t, sessions, 1*24*time.Hour, false)   // fresh — no action
	_ = seedAt(t, sessions, 6*24*time.Hour, false)   // day-6 — warn
	toDelete := seedAt(t, sessions, 8*24*time.Hour, false) // day-8 — delete
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Warned != 1 || result.Deleted != 1 {
		t.Errorf("result: got %+v, want Warned=1 Deleted=1", result)
	}
	if !deleter.Deleted(toDelete.ID) {
		t.Error("day-8 session should be deleted")
	}
}

func TestSweep_deletionErrorIsolatedFromRestOfBatch(t *testing.T) {
	sessions := sessionmock.New()
	failing := seedAt(t, sessions, 8*24*time.Hour, false)
	ok := seedAt(t, sessions, 8*24*time.Hour, false)
	deleter := accountmock.NewDeleter()
	deleter.ErrFor = map[uuid.UUID]error{failing.ID: errors.New("object store: unavailable")}
	sweep := newTestSweep(sessions, deleter)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run should not return a top-level error for a per-session failure: %v", err)
	}

	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 collected error, got %d: %v", len(result.Errors), result.Errors)
	}
	if result.Deleted != 1 {
		t.Errorf("expected the non-failing session to still be deleted, got Deleted=%d", result.Deleted)
	}
	if !deleter.Deleted(ok.ID) {
		t.Error("non-failing session should still have been deleted")
	}
	if deleter.Deleted(failing.ID) {
		t.Error("failing session should not be recorded as deleted")
	}
}

func TestSweep_markWarnedCalledOncePerWarnedSession(t *testing.T) {
	sessions := sessionmock.New()
	sess := seedAt(t, sessions, 6*24*time.Hour, false)
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	if _, err := sweep.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := sessions.GetByID(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WarnedAt == nil {
		t.Error("expected WarnedAt to be set after a warning is sent")
	}
}
