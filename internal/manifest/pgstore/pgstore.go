// Package pgstore provides a Postgres-backed manifest.Repository.
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/manifest"
)

// Store is a Postgres-backed implementation of manifest.Repository.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) manifest.Repository {
	return &Store{runner: runner}
}

// BulkCreate persists regions in a single transaction. The bounding_box
// column stores the BoundingBox as JSONB.
func (s *Store) BulkCreate(ctx context.Context, regions []*manifest.Region) error {
	if len(regions) == 0 {
		return nil
	}
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		for _, r := range regions {
			bbox, err := json.Marshal(r.BoundingBox)
			if err != nil {
				return fmt.Errorf("marshal bounding_box: %w", err)
			}
			_, err = tx.Exec(ctx,
				`INSERT INTO ingestion_manifest
				 (document_id, kb_id, user_id, region_type, page_number, bounding_box, status, extractor_version)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				r.DocumentID, r.KBID, r.UserID,
				string(r.RegionType), r.PageNumber, bbox,
				string(r.Status), r.ExtractorVersion,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("manifest: bulk create: %w", err)
	}
	return nil
}

// ListByDocument returns all regions for documentID, ordered by page then
// top-to-bottom position.
func (s *Store) ListByDocument(ctx context.Context, userID, documentID uuid.UUID) ([]*manifest.Region, error) {
	var results []*manifest.Region
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, document_id, kb_id, user_id, region_type, page_number,
			        bounding_box, status, extractor_version, created_at
			 FROM ingestion_manifest
			 WHERE document_id = $1 AND user_id = $2
			 ORDER BY page_number, (bounding_box->>'y0')::float`,
			documentID, userID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r manifest.Region
			var regionType, status string
			var bboxJSON []byte
			if err := rows.Scan(
				&r.ID, &r.DocumentID, &r.KBID, &r.UserID,
				&regionType, &r.PageNumber, &bboxJSON,
				&status, &r.ExtractorVersion, &r.CreatedAt,
			); err != nil {
				return err
			}
			r.RegionType = manifest.RegionType(regionType)
			r.Status = manifest.Status(status)
			if err := json.Unmarshal(bboxJSON, &r.BoundingBox); err != nil {
				return fmt.Errorf("unmarshal bounding_box: %w", err)
			}
			results = append(results, &r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("manifest: list by document %v: %w", documentID, err)
	}
	return results, nil
}

// DeleteByDocument removes all manifest rows for documentID. Re-running
// region classification calls this first so a re-run is idempotent.
func (s *Store) DeleteByDocument(ctx context.Context, userID, documentID uuid.UUID) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM ingestion_manifest WHERE document_id = $1 AND user_id = $2`,
			documentID, userID,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("manifest: delete by document %v: %w", documentID, err)
	}
	return nil
}

var _ manifest.Repository = (*Store)(nil)
