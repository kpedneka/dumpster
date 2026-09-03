// Package pgstore provides a Postgres-backed theme.Repository.
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/theme"
)

// Store is a Postgres-backed implementation of theme.Repository.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) theme.Repository {
	return &Store{runner: runner}
}

// CommunityMembers returns every community's member canonical entities for
// kbID, grouped by community_id. Ordered by mention_count DESC within each
// community so a caller capping how many entities it uses (see
// theme.Summarizer's maxEntitiesPerPrompt) keeps the most-mentioned, most
// representative members first.
func (s *Store) CommunityMembers(ctx context.Context, userID, kbID uuid.UUID) ([]theme.CommunityMembers, error) {
	byCommunity := make(map[int][]theme.EntityRef)
	var order []int
	seen := make(map[int]bool)

	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT community_id, canonical_text, entity_type
			FROM   canonical_entities
			WHERE  kb_id = $1 AND user_id = $2 AND community_id IS NOT NULL
			ORDER  BY community_id, mention_count DESC`,
			kbID, userID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var communityID int
			var ref theme.EntityRef
			if err := rows.Scan(&communityID, &ref.Text, &ref.Type); err != nil {
				return err
			}
			if !seen[communityID] {
				seen[communityID] = true
				order = append(order, communityID)
			}
			byCommunity[communityID] = append(byCommunity[communityID], ref)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("theme: community members: %w", err)
	}

	members := make([]theme.CommunityMembers, 0, len(order))
	for _, id := range order {
		members = append(members, theme.CommunityMembers{CommunityID: id, Entities: byCommunity[id]})
	}
	return members, nil
}

// SaveResult replaces kbID's theme set: deletes every existing row, then
// inserts themes, all stamped with computedAt — a full overwrite, same
// rationale as community.Repository.SaveResult.
func (s *Store) SaveResult(ctx context.Context, userID, kbID uuid.UUID, themes []theme.Theme, computedAt time.Time) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM kb_themes WHERE kb_id = $1 AND user_id = $2`, kbID, userID); err != nil {
			return err
		}
		if len(themes) == 0 {
			return nil
		}
		batch := &pgx.Batch{}
		for _, t := range themes {
			batch.Queue(`
				INSERT INTO kb_themes (kb_id, user_id, community_id, label, summary, entity_count, computed_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				kbID, userID, t.CommunityID, t.Label, t.Summary, t.EntityCount, computedAt,
			)
		}
		results := tx.SendBatch(ctx, batch)
		for range themes {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return err
			}
		}
		return results.Close()
	})
	if err != nil {
		return fmt.Errorf("theme: save result: %w", err)
	}
	return nil
}

// GetResult returns kbID's most recently generated theme set, ordered by
// entity_count descending (largest/most notable themes first).
func (s *Store) GetResult(ctx context.Context, userID, kbID uuid.UUID) (*theme.Result, error) {
	var result theme.Result
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT community_id, label, summary, entity_count, computed_at
			FROM   kb_themes
			WHERE  kb_id = $1 AND user_id = $2
			ORDER  BY entity_count DESC`,
			kbID, userID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t theme.Theme
			var computedAt time.Time
			if err := rows.Scan(&t.CommunityID, &t.Label, &t.Summary, &t.EntityCount, &computedAt); err != nil {
				return err
			}
			result.ComputedAt = computedAt
			result.Themes = append(result.Themes, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("theme: get result: %w", err)
	}
	if len(result.Themes) == 0 {
		return nil, theme.ErrNoResult
	}
	return &result, nil
}

var _ theme.Repository = (*Store)(nil)
