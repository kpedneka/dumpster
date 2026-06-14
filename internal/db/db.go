package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kunalpednekar/dumpster/internal/config"
)

func dsn(cfg *config.Config) string {
	return fmt.Sprintf(
		"host=%s port=%s dbname=%s user=%s password=%s sslmode=%s",
		cfg.DBHost, cfg.DBPort, cfg.DBName, cfg.DBUser, cfg.DBPassword, cfg.DBSSL,
	)
}

// Connect opens a pgx pool for application queries.
// Prepared-statement caching is disabled so the pool is compatible with
// Neon's PgBouncer in transaction mode.
func Connect(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(dsn(cfg))
	if err != nil {
		return nil, fmt.Errorf("db: parse config: %w", err)
	}
	pcfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// ConnectDirect opens a single non-pooled connection for migrations and
// long-held worker transactions (SKIP LOCKED). Must be closed by the caller.
func ConnectDirect(ctx context.Context, cfg *config.Config) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, dsn(cfg))
	if err != nil {
		return nil, fmt.Errorf("db: direct connect: %w", err)
	}
	return conn, nil
}

// TxRunner abstracts transaction lifecycle so that the RLS layer can inject
// SET LOCAL app.current_user_id inside each transaction without
// restructuring the repository code.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(pgx.Tx) error) error
}

// NewTxRunner returns the default TxRunner backed by a pgxpool.
func NewTxRunner(pool *pgxpool.Pool) TxRunner {
	return &poolTxRunner{pool: pool}
}

type poolTxRunner struct {
	pool *pgxpool.Pool
}

func (r *poolTxRunner) RunInTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}
