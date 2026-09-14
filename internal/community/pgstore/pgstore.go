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
//
// The raw summed co-occurrence count is then replaced with each edge's PPMI
// score (community.ApplyPMIWeighting) before Louvain ever sees it. Raw
// counts alone reward ubiquity, not topical significance: an entity that
// appears in nearly every chunk of a document (a generic technical term, a
// common pronoun) racks up high co-occurrence with everything else it's
// ubiquitous alongside, which Louvain then reads as a real, dense
// community — a "topic" made entirely of noise. PMI corrects for this by
// weighting a pair against how often chance alone would predict it,
// given each entity's own overall frequency (mention_count).
//
// After PMI, community.ApplyRelationBoost applies a further, independent
// adjustment from whatever internal/relation has found for each pair so
// far: boosted if it confirmed a real relationship, damped if it reviewed
// the pair and found none, unchanged if not yet reviewed (still the
// common case, given relation extraction's incremental design) -- see
// ApplyRelationBoost's own comment for why this is a multiplier on top of
// PMI rather than a replacement for it.
func (s *Store) KBGraph(ctx context.Context, userID, kbID uuid.UUID) (*community.Graph, error) {
	g := &community.Graph{}
	freq := make(map[uuid.UUID]int)
	confirmed := make(map[[2]uuid.UUID]bool)
	noneFound := make(map[[2]uuid.UUID]bool)
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		nodeRows, err := tx.Query(ctx,
			`SELECT id, mention_count FROM canonical_entities WHERE kb_id = $1 AND user_id = $2`,
			kbID, userID,
		)
		if err != nil {
			return err
		}
		defer nodeRows.Close()
		for nodeRows.Next() {
			var id uuid.UUID
			var mentionCount int
			if err := nodeRows.Scan(&id, &mentionCount); err != nil {
				return err
			}
			g.Nodes = append(g.Nodes, id)
			freq[id] = mentionCount
		}
		if err := nodeRows.Err(); err != nil {
			return err
		}

		edgeRows, err := tx.Query(ctx, `
			SELECT LEAST(ea.canonical_entity_id, eb.canonical_entity_id)    AS a,
			       GREATEST(ea.canonical_entity_id, eb.canonical_entity_id) AS b,
			       SUM(ee.co_occurrence_count)                              AS weight,
			       BOOL_OR(ee.relation_type IS NOT NULL AND ee.relation_type <> 'none') AS confirmed,
			       COALESCE(BOOL_OR(ee.relation_type = 'none'), false)      AS none_found
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
			// Both flags scan as plain bool, so none_found is COALESCEd to
			// false in SQL rather than left as BOOL_OR's raw result:
			// BOOL_OR returns NULL, not false, when every relation_type in
			// the group is NULL -- the normal case whenever relation
			// extraction hasn't run (or is disabled, as it currently is),
			// which previously made every edge in every KB fail this scan
			// with "cannot scan NULL into *bool" and broke community
			// detection entirely.
			var isConfirmed, isNoneFound bool
			if err := edgeRows.Scan(&e.A, &e.B, &weight, &isConfirmed, &isNoneFound); err != nil {
				return err
			}
			e.Weight = float64(weight)
			g.Edges = append(g.Edges, e)
			key := community.PairKey(e.A, e.B)
			confirmed[key] = isConfirmed
			noneFound[key] = isNoneFound
		}
		return edgeRows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: kb graph: %w", err)
	}
	weighted := community.ApplyPMIWeighting(g, freq)
	return community.ApplyRelationBoost(weighted, g, confirmed, noneFound), nil
}

// GraphView returns kbID's canonical-entity graph shaped for display.
// Nodes come from canonical_entities directly (label, type, and whatever
// community_id the most recent recompute assigned); edges use the same
// collapse-onto-canonical-pairs query KBGraph uses, then get the same PPMI
// reweighting KBGraph applies (see its comment) so the graph rendered here
// never disagrees with the graph Louvain actually saw when it produced the
// community_id values also returned here. Degree is computed in Go from
// the PMI-filtered edge list rather than a second query, so it reflects how
// many *meaningfully* associated entities a node has, not how many
// generically-common ones it happens to share chunks with — the earlier,
// raw-co-occurrence version of Degree actively rewarded ubiquitous noise
// entities, which filterGraphView's percentile cutoff then preserved
// instead of trimming.
func (s *Store) GraphView(ctx context.Context, userID, kbID uuid.UUID) (*community.GraphView, error) {
	view := &community.GraphView{}
	freq := make(map[uuid.UUID]int)
	// edgeDocCount is keyed by the same LEAST/GREATEST-ordered pair the edge
	// query groups by, so it can be looked up again after PMI weighting has
	// decided which edges survive — ApplyPMIWeighting round-trips through
	// community.WeightedEdge (A/B/Weight only, a pure math type with no room
	// for display-only fields), so DocumentCount has to be carried
	// separately and re-attached rather than passed through it.
	type pair struct{ a, b uuid.UUID }
	edgeDocCount := make(map[pair]int)
	// edgeRelationType mirrors edgeDocCount's reason for existing --
	// PMI weighting round-trips through community.WeightedEdge, which has
	// no room for it either.
	edgeRelationType := make(map[pair]*string)
	// confirmed/noneFound feed community.ApplyRelationBoost, same as
	// KBGraph -- so the graph rendered here (including any edge PMI
	// dropped but a confirmed relation rescued) never disagrees with the
	// one Louvain actually saw.
	confirmed := make(map[[2]uuid.UUID]bool)
	noneFound := make(map[[2]uuid.UUID]bool)
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		nodeRows, err := tx.Query(ctx,
			`SELECT id, canonical_text, entity_type, community_id, mention_count, document_count
			 FROM   canonical_entities
			 WHERE  kb_id = $1 AND user_id = $2`,
			kbID, userID,
		)
		if err != nil {
			return err
		}
		defer nodeRows.Close()
		for nodeRows.Next() {
			var n community.GraphNode
			var mentionCount int
			if err := nodeRows.Scan(&n.ID, &n.Label, &n.Type, &n.CommunityID, &mentionCount, &n.DocumentCount); err != nil {
				return err
			}
			view.Nodes = append(view.Nodes, n)
			freq[n.ID] = mentionCount
		}
		if err := nodeRows.Err(); err != nil {
			return err
		}

		edgeRows, err := tx.Query(ctx, `
			SELECT LEAST(ea.canonical_entity_id, eb.canonical_entity_id)    AS a,
			       GREATEST(ea.canonical_entity_id, eb.canonical_entity_id) AS b,
			       SUM(ee.co_occurrence_count)                              AS weight,
			       COUNT(DISTINCT ee.document_id)                           AS doc_count,
			       MAX(ee.relation_type) FILTER (
			           WHERE ee.relation_type IS NOT NULL AND ee.relation_type <> 'none'
			       )                                                        AS relation_type,
			       COALESCE(BOOL_OR(ee.relation_type = 'none'), false)      AS none_found
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
			var source, target uuid.UUID
			var weight int64
			var docCount int
			var relationType *string
			var isNoneFound bool
			if err := edgeRows.Scan(&source, &target, &weight, &docCount, &relationType, &isNoneFound); err != nil {
				return err
			}
			view.Edges = append(view.Edges, community.GraphEdge{Source: source, Target: target, Weight: float64(weight)})
			edgeDocCount[pair{source, target}] = docCount
			edgeRelationType[pair{source, target}] = relationType
			key := community.PairKey(source, target)
			confirmed[key] = relationType != nil
			noneFound[key] = isNoneFound
		}
		return edgeRows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: graph view: %w", err)
	}

	raw := &community.Graph{Nodes: nodesOf(view.Nodes), Edges: toWeightedEdges(view.Edges)}
	weighted := community.ApplyPMIWeighting(raw, freq)
	boosted := community.ApplyRelationBoost(weighted, raw, confirmed, noneFound)
	view.Edges = fromWeightedEdges(boosted.Edges)
	for i := range view.Edges {
		p := pair{view.Edges[i].Source, view.Edges[i].Target}
		view.Edges[i].DocumentCount = edgeDocCount[p]
		view.Edges[i].RelationType = edgeRelationType[p]
	}

	degree := make(map[uuid.UUID]int, len(view.Nodes))
	for _, e := range view.Edges {
		degree[e.Source]++
		degree[e.Target]++
	}
	for i := range view.Nodes {
		view.Nodes[i].Degree = degree[view.Nodes[i].ID]
	}
	return view, nil
}

// nodesOf, toWeightedEdges, and fromWeightedEdges adapt between
// community.GraphView's display-shaped edges (Source/Target) and
// community.Graph's math-shaped edges (A/B) so GraphView can reuse
// ApplyPMIWeighting without either type depending on the other.
func nodesOf(nodes []community.GraphNode) []uuid.UUID {
	ids := make([]uuid.UUID, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	return ids
}

func toWeightedEdges(edges []community.GraphEdge) []community.WeightedEdge {
	out := make([]community.WeightedEdge, len(edges))
	for i, e := range edges {
		out[i] = community.WeightedEdge{A: e.Source, B: e.Target, Weight: e.Weight}
	}
	return out
}

func fromWeightedEdges(edges []community.WeightedEdge) []community.GraphEdge {
	out := make([]community.GraphEdge, len(edges))
	for i, e := range edges {
		out[i] = community.GraphEdge{Source: e.A, Target: e.B, Weight: e.Weight}
	}
	return out
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
