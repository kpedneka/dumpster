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

// seedWith plants a session with explicit CreatedAt, LastActiveAt, and optional WarnedAt.
func seedWith(t *testing.T, store *sessionmock.Store, createdAt, lastActiveAt time.Time, warnedAt *time.Time) *session.Session {
	t.Helper()
	sess := &session.Session{
		ID:           uuid.New(),
		CreatedAt:    createdAt,
		LastActiveAt: lastActiveAt,
		WarnedAt:     warnedAt,
	}
	store.Seed(sess)
	return sess
}

// TestSweep_idleExpiry_survivesIfRecentlyActive: session old by CreatedAt but
// recently touched stays alive because idle clock has not fired.
func TestSweep_idleExpiry_survivesIfRecentlyActive(t *testing.T) {
	sessions := sessionmock.New()
	sess := seedWith(t, sessions,
		fixedNow.Add(-1*time.Hour),    // within HardCap
		fixedNow.Add(-30*time.Minute), // well within IdleTimeout
		nil,
	)
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Deleted != 0 || result.Warned != 0 {
		t.Errorf("active session: got %+v, want no action", result)
	}
	if deleter.Deleted(sess.ID) {
		t.Error("session should not be deleted")
	}
}

// TestSweep_hardCapExpiry_deletesEvenIfRecentlyActive: a session past HardCap
// is deleted regardless of how recently it was touched.
func TestSweep_hardCapExpiry_deletesEvenIfRecentlyActive(t *testing.T) {
	sessions := sessionmock.New()
	sess := seedWith(t, sessions,
		fixedNow.Add(-account.HardCap), // exactly at HardCap boundary
		fixedNow.Add(-1*time.Minute),   // touched very recently
		nil,
	)
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Deleted != 1 {
		t.Errorf("got Deleted=%d, want 1", result.Deleted)
	}
	if !deleter.Deleted(sess.ID) {
		t.Error("hard-cap session should be deleted")
	}
}

// TestSweep_idleExpiry_deletesIfInactive: a session idle for >= IdleTimeout is
// deleted even if it is within HardCap.
func TestSweep_idleExpiry_deletesIfInactive(t *testing.T) {
	sessions := sessionmock.New()
	sess := seedWith(t, sessions,
		fixedNow.Add(-1*time.Hour),          // well within HardCap
		fixedNow.Add(-account.IdleTimeout),  // exactly at idle boundary
		nil,
	)
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Deleted != 1 {
		t.Errorf("got Deleted=%d, want 1", result.Deleted)
	}
	if !deleter.Deleted(sess.ID) {
		t.Error("idle session should be deleted")
	}
}

// TestSweep_touch_resetsIdleClock: a Touch that moves LastActiveAt back inside
// IdleTimeout saves a session from idle expiry.
func TestSweep_touch_resetsIdleClock(t *testing.T) {
	sessions := sessionmock.New()
	sess := seedWith(t, sessions,
		fixedNow.Add(-1*time.Hour),
		fixedNow.Add(-(account.IdleTimeout-time.Minute)), // 1 min before idle expiry
		nil,
	)
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Deleted != 0 {
		t.Errorf("recently-touched session should not be deleted, got Deleted=%d", result.Deleted)
	}
	if deleter.Deleted(sess.ID) {
		t.Error("session should survive after touch reset the idle clock")
	}
}

// TestSweep_warning_nearHardCap: session within WarningLeadTime of HardCap
// receives a warning.
func TestSweep_warning_nearHardCap(t *testing.T) {
	sessions := sessionmock.New()
	sess := seedWith(t, sessions,
		fixedNow.Add(-(account.HardCap-10*time.Minute)), // 10 min from hard cap
		fixedNow.Add(-1*time.Minute),                    // recently active
		nil,
	)
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Warned != 1 {
		t.Errorf("got Warned=%d, want 1", result.Warned)
	}
	if result.Deleted != 0 {
		t.Errorf("got Deleted=%d, want 0", result.Deleted)
	}
	got, _ := sessions.GetByID(context.Background(), sess.ID)
	if got.WarnedAt == nil {
		t.Error("WarnedAt should be set after warning")
	}
}

// TestSweep_warning_nearIdleTimeout: session within WarningLeadTime of
// IdleTimeout receives a warning even if far from HardCap.
func TestSweep_warning_nearIdleTimeout(t *testing.T) {
	sessions := sessionmock.New()
	sess := seedWith(t, sessions,
		fixedNow.Add(-1*time.Hour),                            // well within HardCap
		fixedNow.Add(-(account.IdleTimeout-10*time.Minute)),   // 10 min from idle expiry
		nil,
	)
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Warned != 1 {
		t.Errorf("got Warned=%d, want 1", result.Warned)
	}
	got, _ := sessions.GetByID(context.Background(), sess.ID)
	if got.WarnedAt == nil {
		t.Error("WarnedAt should be set after warning")
	}
}

// TestSweep_warning_idempotency: an already-warned session in the warning
// window is not warned a second time.
func TestSweep_warning_idempotency(t *testing.T) {
	warnedAt := fixedNow.Add(-5 * time.Minute)
	sessions := sessionmock.New()
	sess := seedWith(t, sessions,
		fixedNow.Add(-(account.HardCap-10*time.Minute)),
		fixedNow.Add(-1*time.Minute),
		&warnedAt,
	)
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Warned != 0 {
		t.Errorf("already-warned session should not be warned again, got Warned=%d", result.Warned)
	}
	got, _ := sessions.GetByID(context.Background(), sess.ID)
	if got.WarnedAt != sess.WarnedAt {
		t.Error("WarnedAt should be unchanged after idempotent sweep")
	}
}

// TestSweep_bothClocks_deletesOnce: a session past both HardCap and
// IdleTimeout simultaneously is deleted exactly once, never warned.
func TestSweep_bothClocks_deletesOnce(t *testing.T) {
	sessions := sessionmock.New()
	sess := seedWith(t, sessions,
		fixedNow.Add(-account.HardCap),
		fixedNow.Add(-account.IdleTimeout),
		nil,
	)
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	result, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Deleted != 1 {
		t.Errorf("got Deleted=%d, want 1", result.Deleted)
	}
	if result.Warned != 0 {
		t.Errorf("got Warned=%d, want 0 — delete and warn are mutually exclusive", result.Warned)
	}
	if !deleter.Deleted(sess.ID) {
		t.Error("session should be deleted")
	}
	if deleter.DeletedCount() != 1 {
		t.Errorf("Delete called %d times, want exactly 1", deleter.DeletedCount())
	}
}

// TestSweep_warningIdempotency_acrossRuns: repeated sweeps within the warning
// window stamp WarnedAt exactly once.
func TestSweep_warningIdempotency_acrossRuns(t *testing.T) {
	sessions := sessionmock.New()
	sess := seedWith(t, sessions,
		fixedNow.Add(-(account.HardCap-10*time.Minute)),
		fixedNow.Add(-1*time.Minute),
		nil,
	)
	deleter := accountmock.NewDeleter()
	sweep := newTestSweep(sessions, deleter)

	if _, err := sweep.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
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
	got2, err := sessions.GetByID(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.WarnedAt != got.WarnedAt {
		t.Error("WarnedAt should not change on second run within the same window")
	}
}

// TestSweep_mixedBatch: one fresh, one near-hard-cap, one past hard-cap — only
// warn and delete fire once each.
func TestSweep_mixedBatch(t *testing.T) {
	sessions := sessionmock.New()
	_ = seedWith(t, sessions, fixedNow.Add(-1*time.Hour), fixedNow.Add(-30*time.Minute), nil) // fresh
	_ = seedWith(t, sessions,                                                                   // near hard cap → warn
		fixedNow.Add(-(account.HardCap-10*time.Minute)),
		fixedNow.Add(-1*time.Minute),
		nil,
	)
	toDelete := seedWith(t, sessions, fixedNow.Add(-account.HardCap), fixedNow.Add(-1*time.Minute), nil)
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
		t.Error("hard-cap session should be deleted")
	}
}

// TestSweep_deletionErrorIsolatedFromRestOfBatch: a per-session delete failure
// is collected without aborting the rest of the batch.
func TestSweep_deletionErrorIsolatedFromRestOfBatch(t *testing.T) {
	sessions := sessionmock.New()
	failing := seedWith(t, sessions, fixedNow.Add(-account.HardCap), fixedNow.Add(-1*time.Minute), nil)
	ok := seedWith(t, sessions, fixedNow.Add(-account.HardCap), fixedNow.Add(-1*time.Minute), nil)
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
