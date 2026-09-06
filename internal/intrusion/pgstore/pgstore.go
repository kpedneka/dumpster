// Package pgstore provides a Postgres-backed intrusion.Repository.
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/intrusion"
)

// Store is a Postgres-backed implementation of intrusion.Repository.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) intrusion.Repository {
	return &Store{runner: runner}
}

// SaveResult replaces kbID's intrusion test result: deletes every existing
// row, then inserts result.Communities, all stamped with result.ComputedAt
// -- a full overwrite, same rationale as theme.Repository.SaveResult.
func (s *Store) SaveResult(ctx context.Context, userID, kbID uuid.UUID, result intrusion.Result) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM kb_intrusion_tests WHERE kb_id = $1 AND user_id = $2`, kbID, userID); err != nil {
			return err
		}
		if len(result.Communities) == 0 {
			return nil
		}
		batch := &pgx.Batch{}
		for _, c := range result.Communities {
			batch.Queue(`
				INSERT INTO kb_intrusion_tests (kb_id, user_id, community_id, members, intruder_text, judge_answer, correct, computed_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				kbID, userID, c.CommunityID, c.Members, c.IntruderText, c.JudgeAnswer, c.Correct, result.ComputedAt,
			)
		}
		results := tx.SendBatch(ctx, batch)
		for range result.Communities {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return err
			}
		}
		return results.Close()
	})
	if err != nil {
		return fmt.Errorf("intrusion: save result: %w", err)
	}
	return nil
}

// GetResult returns kbID's most recently run intrusion test.
func (s *Store) GetResult(ctx context.Context, userID, kbID uuid.UUID) (*intrusion.Result, error) {
	var result intrusion.Result
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT community_id, members, intruder_text, judge_answer, correct, computed_at
			FROM   kb_intrusion_tests
			WHERE  kb_id = $1 AND user_id = $2
			ORDER  BY community_id`,
			kbID, userID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c intrusion.CommunityResult
			var computedAt time.Time
			if err := rows.Scan(&c.CommunityID, &c.Members, &c.IntruderText, &c.JudgeAnswer, &c.Correct, &computedAt); err != nil {
				return err
			}
			result.ComputedAt = computedAt
			result.Communities = append(result.Communities, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("intrusion: get result: %w", err)
	}
	if len(result.Communities) == 0 {
		return nil, intrusion.ErrNoResult
	}

	result.TestedCount = len(result.Communities)
	var correct int
	for _, c := range result.Communities {
		if c.Correct {
			correct++
		}
	}
	result.Score = float64(correct) / float64(result.TestedCount)
	return &result, nil
}

var _ intrusion.Repository = (*Store)(nil)
