// Package pgstore provides a Postgres-backed canonical.Repository.
package pgstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/canonical"
	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/entity"
)

// Store is a Postgres-backed implementation of canonical.Repository.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) canonical.Repository {
	return &Store{runner: runner}
}

// mentionGroup is a set of mentions that share one canonical identity
// (kb_id, user_id, normalized_text, entity_type) within a single
// Canonicalize call.
type mentionGroup struct {
	kbID, userID   uuid.UUID
	normalizedText string
	entityType     string
	canonicalText  string
	mentionIDs     []uuid.UUID
}

// Canonicalize resolves each mention to a canonical entity in one
// transaction: mentions are grouped by identity so each distinct canonical
// entity is upserted exactly once, incrementing mention_count by the
// group's size and document_count by 1 (correct as long as callers only
// ever pass mentions not already resolved — see canonical.ResolveNew,
// which enforces this and is DecrementForDocument's counterpart).
func (s *Store) Canonicalize(ctx context.Context, mentions []*entity.Entity) (map[uuid.UUID]uuid.UUID, error) {
	if len(mentions) == 0 {
		return nil, nil
	}

	var order []*mentionGroup
	index := make(map[string]*mentionGroup)
	for _, m := range mentions {
		normalized := canonical.Normalize(m.Text)
		key := m.KBID.String() + "|" + m.UserID.String() + "|" + normalized + "|" + string(m.Type)
		g, ok := index[key]
		if !ok {
			g = &mentionGroup{
				kbID: m.KBID, userID: m.UserID,
				normalizedText: normalized, entityType: string(m.Type),
				canonicalText: m.Text,
			}
			index[key] = g
			order = append(order, g)
		}
		g.mentionIDs = append(g.mentionIDs, m.ID)
	}

	resolved := make(map[uuid.UUID]uuid.UUID, len(mentions))
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, g := range order {
			batch.Queue(
				`INSERT INTO canonical_entities
				 (kb_id, user_id, canonical_text, normalized_text, entity_type, mention_count, document_count)
				 VALUES ($1, $2, $3, $4, $5, $6, 1)
				 ON CONFLICT (kb_id, user_id, normalized_text, entity_type)
				 DO UPDATE SET mention_count  = canonical_entities.mention_count + $6,
				               document_count = canonical_entities.document_count + 1,
				               updated_at     = now()
				 RETURNING id`,
				g.kbID, g.userID, g.canonicalText, g.normalizedText, g.entityType, len(g.mentionIDs),
			)
		}
		results := tx.SendBatch(ctx, batch)
		for _, g := range order {
			var id uuid.UUID
			if err := results.QueryRow().Scan(&id); err != nil {
				_ = results.Close()
				return err
			}
			for _, mentionID := range g.mentionIDs {
				resolved[mentionID] = id
			}
		}
		return results.Close()
	})
	if err != nil {
		return nil, fmt.Errorf("canonical: canonicalize: %w", err)
	}
	return resolved, nil
}

// DecrementForDocument reverses documentID's current contribution to
// canonical entity stats, deleting any canonical row whose mention_count
// reaches zero as a result.
func (s *Store) DecrementForDocument(ctx context.Context, userID, documentID uuid.UUID) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`WITH doc_mentions AS (
			     SELECT canonical_entity_id, COUNT(*) AS cnt
			     FROM entities
			     WHERE document_id = $1 AND user_id = $2 AND canonical_entity_id IS NOT NULL
			     GROUP BY canonical_entity_id
			 )
			 UPDATE canonical_entities ce
			 SET mention_count  = ce.mention_count - doc_mentions.cnt,
			     document_count = ce.document_count - 1,
			     updated_at     = now()
			 FROM doc_mentions
			 WHERE ce.id = doc_mentions.canonical_entity_id AND ce.user_id = $2`,
			documentID, userID,
		); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`DELETE FROM canonical_entities WHERE user_id = $1 AND mention_count <= 0`,
			userID,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("canonical: decrement for document %v: %w", documentID, err)
	}
	return nil
}

// Get returns the canonical entity with the given id, scoped to userID.
func (s *Store) Get(ctx context.Context, userID, id uuid.UUID) (*canonical.CanonicalEntity, error) {
	var ce canonical.CanonicalEntity
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		var entityType string
		err := tx.QueryRow(ctx,
			`SELECT id, kb_id, user_id, canonical_text, normalized_text, entity_type,
			        mention_count, document_count, created_at, updated_at
			 FROM canonical_entities WHERE id = $1 AND user_id = $2`,
			id, userID,
		).Scan(
			&ce.ID, &ce.KBID, &ce.UserID, &ce.CanonicalText, &ce.NormalizedText, &entityType,
			&ce.MentionCount, &ce.DocumentCount, &ce.CreatedAt, &ce.UpdatedAt,
		)
		if err != nil {
			return err
		}
		ce.Type = entity.Type(entityType)
		return nil
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, canonical.ErrNotFound
		}
		return nil, fmt.Errorf("canonical: get %v: %w", id, err)
	}
	return &ce, nil
}

// ListByKB returns every canonical entity in kbID belonging to userID.
func (s *Store) ListByKB(ctx context.Context, userID, kbID uuid.UUID) ([]*canonical.CanonicalEntity, error) {
	var results []*canonical.CanonicalEntity
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, kb_id, user_id, canonical_text, normalized_text, entity_type,
			        mention_count, document_count, created_at, updated_at
			 FROM canonical_entities WHERE kb_id = $1 AND user_id = $2`,
			kbID, userID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ce canonical.CanonicalEntity
			var entityType string
			if err := rows.Scan(
				&ce.ID, &ce.KBID, &ce.UserID, &ce.CanonicalText, &ce.NormalizedText, &entityType,
				&ce.MentionCount, &ce.DocumentCount, &ce.CreatedAt, &ce.UpdatedAt,
			); err != nil {
				return err
			}
			ce.Type = entity.Type(entityType)
			results = append(results, &ce)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("canonical: list by kb %v: %w", kbID, err)
	}
	return results, nil
}

var _ canonical.Repository = (*Store)(nil)
