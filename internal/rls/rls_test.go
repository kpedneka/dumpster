package rls_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/rls"
)

// Compile-time check: TxRunner satisfies the db.TxRunner interface.
var _ db.TxRunner = (*rls.TxRunner)(nil)

// TestRunInTx_rejectsUnauthenticated verifies the fail-loud behaviour:
// a context with no user identity must error before any DB interaction,
// not silently return zero rows (which the two-arg current_setting form would do).
func TestRunInTx_rejectsUnauthenticated(t *testing.T) {
	runner := rls.New(nil) // pool is nil; error must fire before it is used

	err := runner.RunInTx(context.Background(), func(_ pgx.Tx) error {
		t.Error("fn should never be called when context has no user")
		return nil
	})
	if err == nil {
		t.Fatal("expected error for unauthenticated context, got nil")
	}
}

// TestRunInTx_contextCheck confirms the context plumbing RunInTx depends on.
// Full SET LOCAL behaviour and RLS enforcement are verified by integration
// tests against Postgres.
func TestRunInTx_contextCheck(t *testing.T) {
	id := uuid.New()
	ctx := auth.WithUserID(context.Background(), id)

	got, ok := auth.UserIDFromContext(ctx)
	if !ok {
		t.Fatal("expected userID in context")
	}
	if got != id {
		t.Fatalf("context userID: got %v, want %v", got, id)
	}
}
