// Package pgstore provides a Postgres-backed community.Repository.
package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/community"
	"github.com/kunalpednekar/dumpster/internal/db"
)

// Store is a Postgres-backed implementation of community.Repository.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) community.Repository {
	return &Store{runner: runner}
}

// CountCanonicalEntities returns the number of canonical entities in kbID.
func (s *Store) CountCanonicalEntities(ctx context.Context, userID, kbID uuid.UUID) (int, error) {
	var count int
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM canonical_entities WHERE kb_id = $1 AND user_id = $2`,
			kbID, userID,
		).Scan(&count)
	})
	if err != nil {
		return 0, fmt.Errorf("community: count canonical entities: %w", err)
	}
	return count, nil
}

// KBGraph builds the canonical-entity graph for kbID. Nodes are every
// canonical entity in the KB, including ones with no edges — SaveResult
// needs a full node list to fully overwrite the KB's community assignment
// on every run. Edges collapse entity_edges (chunk-scoped, per-mention)
// onto canonical entity pairs by joining both sides to canonical_entity_id
// and summing co_occurrence_count across every contributing mention pair;
// mentions not yet canonicalized, and any pair that collapses onto the same
// canonical entity on both sides (a self-loop), are excluded.
func (s *Store) KBGraph(ctx context.Context, userID, kbID uuid.UUID) (*community.Graph, error) {
	g := &community.Graph{}
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		nodeRows, err := tx.Query(ctx,
			`SELECT id FROM canonical_entities WHERE kb_id = $1 AND user_id = $2`,
			kbID, userID,
		)
		if err != nil {
			return err
		}
		defer nodeRows.Close()
		for nodeRows.Next() {
			var id uuid.UUID
			if err := nodeRows.Scan(&id); err != nil {
				return err
			}
			g.Nodes = append(g.Nodes, id)
		}
		if err := nodeRows.Err(); err != nil {
			return err
		}

		edgeRows, err := tx.Query(ctx, `
			SELECT LEAST(ea.canonical_entity_id, eb.canonical_entity_id)    AS a,
			       GREATEST(ea.canonical_entity_id, eb.canonical_entity_id) AS b,
			       SUM(ee.co_occurrence_count)                              AS weight
			FROM   entity_edges ee
			JOIN   entities ea ON ea.id = ee.entity_a_id
			JOIN   entities eb ON eb.id = ee.entity_b_id
			WHERE  ee.kb_id = $1 AND ee.user_id = $2
			  AND  ea.canonical_entity_id IS NOT NULL
			  AND  eb.canonical_entity_id IS NOT NULL
			  AND  ea.canonical_entity_id <> eb.canonical_entity_id
			GROUP  BY 1, 2`,
			kbID, userID,
		)
		if err != nil {
			return err
		}
		defer edgeRows.Close()
		for edgeRows.Next() {
			var e community.WeightedEdge
			var weight int64
			if err := edgeRows.Scan(&e.A, &e.B, &weight); err != nil {
				return err
			}
			e.Weight = float64(weight)
			g.Edges = append(g.Edges, e)
		}
		return edgeRows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: kb graph: %w", err)
	}
	return g, nil
}

// SaveResult persists assignments onto canonical_entities.community_id and
// upserts kbID's kb_community_runs row, in one transaction.
func (s *Store) SaveResult(ctx context.Context, userID, kbID uuid.UUID, assignments map[uuid.UUID]int, run community.Result) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		if len(assignments) > 0 {
			batch := &pgx.Batch{}
			for entityID, communityID := range assignments {
				batch.Queue(
					`UPDATE canonical_entities
					 SET    community_id = $1, updated_at = NOW()
					 WHERE  id = $2 AND kb_id = $3 AND user_id = $4`,
					communityID, entityID, kbID, userID,
				)
			}
			results := tx.SendBatch(ctx, batch)
			for range assignments {
				if _, err := results.Exec(); err != nil {
					_ = results.Close()
					return err
				}
			}
			if err := results.Close(); err != nil {
				return err
			}
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO kb_community_runs (kb_id, user_id, computed_at, modularity, community_count, node_count, edge_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (kb_id) DO UPDATE SET
				user_id         = EXCLUDED.user_id,
				computed_at     = EXCLUDED.computed_at,
				modularity      = EXCLUDED.modularity,
				community_count = EXCLUDED.community_count,
				node_count      = EXCLUDED.node_count,
				edge_count      = EXCLUDED.edge_count`,
			kbID, userID, run.ComputedAt, run.Modularity, run.CommunityCount, run.NodeCount, run.EdgeCount,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("community: save result: %w", err)
	}
	return nil
}

// GetResult returns kbID's most recent community-detection summary.
func (s *Store) GetResult(ctx context.Context, userID, kbID uuid.UUID) (*community.Result, error) {
	r := &community.Result{KBID: kbID, UserID: userID}
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT computed_at, modularity, community_count, node_count, edge_count
			FROM   kb_community_runs
			WHERE  kb_id = $1 AND user_id = $2`,
			kbID, userID,
		).Scan(&r.ComputedAt, &r.Modularity, &r.CommunityCount, &r.NodeCount, &r.EdgeCount)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, community.ErrNoResult
		}
		return nil, fmt.Errorf("community: get result: %w", err)
	}
	return r, nil
}

var _ community.Repository = (*Store)(nil)
