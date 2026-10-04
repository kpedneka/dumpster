// Package memory provides an in-memory jobstatus.Store for use in tests.
package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// rowKey mirrors the Postgres table's primary key (document_id, job_type).
// The user ID is checked separately, the way row-level security hides
// another tenant's row rather than treating it as a different row.
type rowKey struct {
	documentID uuid.UUID
	jobType    queue.JobType
}

type row struct {
	rec jobstatus.Record
	// seq orders rows by when their current attempt was enqueued, like
	// the Postgres store's enqueued_at.
	seq uint64
}

// Store is an in-memory, tenant-scoped jobstatus.Store.
type Store struct {
	mu   sync.Mutex
	rows map[rowKey]*row
	seq  uint64
}

// New returns an empty Store.
func New() *Store {
	return &Store{rows: make(map[rowKey]*row)}
}

func keyOf(k jobstatus.Key) rowKey {
	return rowKey{documentID: k.DocumentID, jobType: k.JobType}
}

// Enqueue implements jobstatus.Writer.
func (s *Store) Enqueue(_ context.Context, key jobstatus.Key) (jobstatus.Enqueued, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var cur *jobstatus.Record
	r, ok := s.rows[keyOf(key)]
	if ok {
		if r.rec.UserID != key.UserID {
			return jobstatus.Enqueued{}, jobstatus.ErrNotFound
		}
		cur = &r.rec
	}
	next, res := jobstatus.Enqueue(cur, key)
	if res.Started {
		s.seq++
		s.rows[keyOf(key)] = &row{rec: next, seq: s.seq}
	}
	return res, nil
}

// MarkProcessing implements jobstatus.Writer.
func (s *Store) MarkProcessing(_ context.Context, key jobstatus.Key, attempt int) error {
	return s.transition(key, attempt, jobstatus.StatusProcessing, "", "")
}

// SetPhase implements jobstatus.Writer.
func (s *Store) SetPhase(_ context.Context, key jobstatus.Key, attempt int, phase string) error {
	return s.transition(key, attempt, jobstatus.StatusProcessing, phase, "")
}

// MarkSucceeded implements jobstatus.Writer.
func (s *Store) MarkSucceeded(_ context.Context, key jobstatus.Key, attempt int) error {
	return s.transition(key, attempt, jobstatus.StatusSucceeded, "", "")
}

// MarkFailed implements jobstatus.Writer.
func (s *Store) MarkFailed(_ context.Context, key jobstatus.Key, attempt int, reason string) error {
	return s.transition(key, attempt, jobstatus.StatusFailed, "", reason)
}

func (s *Store) transition(key jobstatus.Key, attempt int, to jobstatus.Status, phase, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rows[keyOf(key)]
	if !ok || r.rec.UserID != key.UserID {
		return jobstatus.ErrNotFound
	}
	next, _, err := jobstatus.Transition(r.rec, attempt, to, phase, reason)
	if err != nil {
		return err
	}
	r.rec = next
	return nil
}

// ActiveJobsForDocuments implements queue.JobStatusReader: every pending
// or processing job for userID's documents in documentIDs, in the order
// their current attempts were enqueued.
func (s *Store) ActiveJobsForDocuments(_ context.Context, userID uuid.UUID, documentIDs []uuid.UUID) (map[uuid.UUID][]queue.JobStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	wanted := make(map[uuid.UUID]bool, len(documentIDs))
	for _, id := range documentIDs {
		wanted[id] = true
	}
	var matches []*row
	for _, r := range s.rows {
		if r.rec.UserID == userID && wanted[r.rec.DocumentID] && r.rec.Status.Active() {
			matches = append(matches, r)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].seq < matches[j].seq })

	out := make(map[uuid.UUID][]queue.JobStatus, len(documentIDs))
	for _, r := range matches {
		out[r.rec.DocumentID] = append(out[r.rec.DocumentID], queue.JobStatus{
			Type:      r.rec.JobType,
			Phase:     r.rec.Phase,
			Status:    string(r.rec.Status),
			LastError: r.rec.LastError,
		})
	}
	return out, nil
}

var _ jobstatus.Store = (*Store)(nil)
