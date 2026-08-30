// Package pgstore provides a Postgres-backed inquiry.Repository.
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/inquiry"
)

// Store is a Postgres-backed implementation of inquiry.Repository.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) inquiry.Repository {
	return &Store{runner: runner}
}

// GetOrCreate upserts a no-op self-update on conflict rather than DO
// NOTHING, since RETURNING produces no row for a skipped DO NOTHING — this
// is the standard idiom for "insert, or fetch the existing row" in one
// round trip.
func (s *Store) GetOrCreate(ctx context.Context, userID, kbID uuid.UUID) (*inquiry.Inquiry, error) {
	var result inquiry.Inquiry
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO inquiries (kb_id, user_id)
			 VALUES ($1, $2)
			 ON CONFLICT (kb_id, user_id) DO UPDATE SET kb_id = inquiries.kb_id
			 RETURNING id, kb_id, user_id, title, created_at, updated_at`,
			kbID, userID,
		).Scan(&result.ID, &result.KBID, &result.UserID, &result.Title, &result.CreatedAt, &result.UpdatedAt)
	})
	if err != nil {
		return nil, fmt.Errorf("inquiry: get or create (kb=%v): %w", kbID, err)
	}
	return &result, nil
}

func (s *Store) Get(ctx context.Context, userID, kbID uuid.UUID) (*inquiry.Inquiry, error) {
	var result inquiry.Inquiry
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, kb_id, user_id, title, created_at, updated_at
			 FROM inquiries WHERE kb_id = $1 AND user_id = $2`,
			kbID, userID,
		).Scan(&result.ID, &result.KBID, &result.UserID, &result.Title, &result.CreatedAt, &result.UpdatedAt)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, inquiry.ErrNotFound
		}
		return nil, fmt.Errorf("inquiry: get (kb=%v): %w", kbID, err)
	}
	return &result, nil
}

// AppendMessage computes Ordinal as one past the current max for the
// inquiry within the same INSERT...SELECT, rather than a separate
// SELECT MAX then INSERT, to keep the read and write in one statement.
// The inquiry_messages_inquiry_ordinal_key unique index is the safety net
// against a concurrent-append race producing a duplicate position.
func (s *Store) AppendMessage(ctx context.Context, userID uuid.UUID, msg *inquiry.Message) (*inquiry.Message, error) {
	citationsJSON, err := marshalOrNil(msg.Citations)
	if err != nil {
		return nil, fmt.Errorf("inquiry: marshal citations: %w", err)
	}
	retrievedJSON, err := marshalOrNil(msg.RetrievedDocuments)
	if err != nil {
		return nil, fmt.Errorf("inquiry: marshal retrieved_documents: %w", err)
	}

	var result inquiry.Message
	var citationsOut, retrievedOut []byte
	err = s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO inquiry_messages
			 (inquiry_id, kb_id, user_id, role, content, citations, retrieved_documents, ordinal)
			 SELECT $1, $2, $3, $4, $5, $6, $7, COALESCE(MAX(ordinal), -1) + 1
			 FROM inquiry_messages WHERE inquiry_id = $1
			 RETURNING id, inquiry_id, kb_id, user_id, role, content, citations, retrieved_documents, ordinal, created_at`,
			msg.InquiryID, msg.KBID, userID, msg.Role, msg.Content, citationsJSON, retrievedJSON,
		).Scan(
			&result.ID, &result.InquiryID, &result.KBID, &result.UserID, &result.Role, &result.Content,
			&citationsOut, &retrievedOut, &result.Ordinal, &result.CreatedAt,
		)
	})
	if err != nil {
		return nil, fmt.Errorf("inquiry: append message (inquiry=%v): %w", msg.InquiryID, err)
	}
	if err := unmarshalIfPresent(citationsOut, &result.Citations); err != nil {
		return nil, fmt.Errorf("inquiry: unmarshal citations: %w", err)
	}
	if err := unmarshalIfPresent(retrievedOut, &result.RetrievedDocuments); err != nil {
		return nil, fmt.Errorf("inquiry: unmarshal retrieved_documents: %w", err)
	}
	return &result, nil
}

func (s *Store) ListMessages(ctx context.Context, userID, inquiryID uuid.UUID) ([]*inquiry.Message, error) {
	var results []*inquiry.Message
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, inquiry_id, kb_id, user_id, role, content, citations, retrieved_documents, ordinal, created_at
			 FROM inquiry_messages
			 WHERE inquiry_id = $1 AND user_id = $2
			 ORDER BY ordinal`,
			inquiryID, userID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m inquiry.Message
			var citationsRaw, retrievedRaw []byte
			if err := rows.Scan(
				&m.ID, &m.InquiryID, &m.KBID, &m.UserID, &m.Role, &m.Content,
				&citationsRaw, &retrievedRaw, &m.Ordinal, &m.CreatedAt,
			); err != nil {
				return err
			}
			if err := unmarshalIfPresent(citationsRaw, &m.Citations); err != nil {
				return fmt.Errorf("unmarshal citations: %w", err)
			}
			if err := unmarshalIfPresent(retrievedRaw, &m.RetrievedDocuments); err != nil {
				return fmt.Errorf("unmarshal retrieved_documents: %w", err)
			}
			results = append(results, &m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("inquiry: list messages (inquiry=%v): %w", inquiryID, err)
	}
	return results, nil
}

// marshalOrNil returns nil (SQL NULL) for an empty/nil slice, rather than
// the JSON literal "[]" or "null" — mirroring chunk.pgstore's handling of
// the optional bounding_box column.
func marshalOrNil[T any](v []T) ([]byte, error) {
	if len(v) == 0 {
		return nil, nil
	}
	return json.Marshal(v)
}

// unmarshalIfPresent leaves dst untouched (nil slice) when raw is empty,
// mirroring marshalOrNil's NULL-for-empty convention on the write side.
func unmarshalIfPresent[T any](raw []byte, dst *[]T) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

var _ inquiry.Repository = (*Store)(nil)
