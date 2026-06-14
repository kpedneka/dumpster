// Package rls provides a TxRunner that sets the per-transaction RLS context
// so Postgres row-level security policies can enforce tenant isolation as a
// structural backstop independent of the app-level WHERE user_id filter.
package rls

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/db"
)

// TxRunner implements db.TxRunner. Before invoking fn it sets
// app.current_user_id transaction-locally so RLS policies see the
// correct identity for the duration of the transaction.
//
// SET LOCAL / set_config(..., true) is used deliberately over session-level SET:
// transaction-mode pooling shares backends across requests, so a session-scoped
// setting would leak one user's identity into another's query.
type TxRunner struct {
	pool *pgxpool.Pool
}

// New returns an RLS-aware TxRunner backed by pool.
func New(pool *pgxpool.Pool) *TxRunner {
	return &TxRunner{pool: pool}
}

// RunInTx begins a transaction, injects the authenticated user's UUID as
// the RLS context, calls fn, then commits or rolls back.
//
// Returns an error immediately if the context carries no authenticated user —
// this is the fail-loud behaviour required by the RLS policy, which uses the
// one-arg form of current_setting() that errors on a missing setting.
func (r *TxRunner) RunInTx(ctx context.Context, fn func(pgx.Tx) error) error {
	userID, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return errors.New("rls: unauthenticated: no user identity in context")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("rls: begin tx: %w", err)
	}

	// set_config(name, value, is_local=true) is equivalent to SET LOCAL and
	// binds the setting only for the current transaction.
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.current_user_id', $1, true)`,
		userID.String(),
	); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("rls: set context: %w", err)
	}

	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

var _ db.TxRunner = (*TxRunner)(nil)
