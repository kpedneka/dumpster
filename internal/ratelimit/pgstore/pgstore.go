// Package pgstore provides a Postgres-backed, fixed-window rate limiter --
// the shared-store replacement for internal/ratelimit/memory, which is only
// correct for a single API replica. The counter lives in the
// rate_limit_counters table (migrations/030) rather than per-process memory,
// so every replica enforces the same limit against the same shared state.
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kunalpednekar/dumpster/internal/ratelimit"
)

// Store is a fixed-window rate limiter backed by Postgres.
type Store struct {
	pool   *pgxpool.Pool
	limit  int
	window time.Duration
}

// New returns a Store that allows up to limit requests per window duration,
// shared across every caller of Allow regardless of which process makes the
// call.
func New(pool *pgxpool.Pool, limit int, window time.Duration) *Store {
	return &Store{pool: pool, limit: limit, window: window}
}

// Allow atomically increments key's counter for the current window and
// reports whether it is still within limit. The INSERT ... ON CONFLICT
// statement does the read-check-write in one round trip and one row-level
// lock, so concurrent calls for the same key -- whether from goroutines in
// this process or from other API replicas entirely -- can't race past each
// other the way a separate SELECT-then-UPDATE would.
func (s *Store) Allow(ctx context.Context, key string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		INSERT INTO rate_limit_counters (key, count, window_end)
		VALUES ($1, 1, now() + make_interval(secs => $2))
		ON CONFLICT (key) DO UPDATE SET
			count = CASE
				WHEN rate_limit_counters.window_end < now() THEN 1
				ELSE rate_limit_counters.count + 1
			END,
			window_end = CASE
				WHEN rate_limit_counters.window_end < now() THEN now() + make_interval(secs => $2)
				ELSE rate_limit_counters.window_end
			END
		RETURNING count
	`, key, s.window.Seconds()).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("ratelimit/pgstore: allow %q: %w", key, err)
	}
	return count <= s.limit, nil
}

// DeleteExpired removes counter rows whose window ended more than
// staleFor ago -- a maintenance operation independent of any one Store's
// configured limit/window, meant to be called periodically (e.g. from
// cmd/cleanup's daily run) so the table doesn't grow by one permanent row
// per distinct key ever seen. Returns the number of rows removed.
func DeleteExpired(ctx context.Context, pool *pgxpool.Pool, staleFor time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx, `
		DELETE FROM rate_limit_counters WHERE window_end < now() - make_interval(secs => $1)
	`, staleFor.Seconds())
	if err != nil {
		return 0, fmt.Errorf("ratelimit/pgstore: delete expired: %w", err)
	}
	return tag.RowsAffected(), nil
}

var _ ratelimit.Limiter = (*Store)(nil)
