package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kunalpednekar/dumpster/internal/config"
)

// dsn returns the direct (non-pooled) connection string.
// DATABASE_URL takes precedence over individual DB_* env vars.
func dsn(cfg *config.Config) string {
	if cfg.DatabaseURL != "" {
		return cfg.DatabaseURL
	}
	return fmt.Sprintf(
		"host=%s port=%s dbname=%s user=%s password=%s sslmode=%s",
		cfg.DBHost, cfg.DBPort, cfg.DBName, cfg.DBUser, cfg.DBPassword, cfg.DBSSL,
	)
}

// dsnPooled returns the PgBouncer connection string for the API.
// DATABASE_URL_POOLED takes precedence, then falls back to dsn.
func dsnPooled(cfg *config.Config) string {
	if cfg.DatabaseURLPooled != "" {
		return cfg.DatabaseURLPooled
	}
	return dsn(cfg)
}

// Connect opens a pgx pool using the pooled (PgBouncer) connection string.
// Prepared-statement caching is disabled so the pool is compatible with
// Neon's PgBouncer in transaction mode.
func Connect(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	return connect(ctx, dsnPooled(cfg))
}

// ConnectDirect opens a pgx pool using the direct (non-pooled) connection
// string. Required for the worker's SKIP LOCKED loop and SET LOCAL RLS calls
// which need session-stable connections that pooler transaction mode cannot
// provide.
func ConnectDirect(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	return connect(ctx, dsn(cfg))
}

func connect(ctx context.Context, connStr string) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(connStr)
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
