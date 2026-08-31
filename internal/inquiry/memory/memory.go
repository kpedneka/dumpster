// Package memory provides an in-memory inquiry.Repository for use in tests.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/inquiry"
)

type kbUserKey struct {
	kbID   uuid.UUID
	userID uuid.UUID
}

// Repository is an in-memory, tenant-scoped inquiry.Repository.
type Repository struct {
	mu        sync.Mutex
	inquiries map[uuid.UUID]*inquiry.Inquiry
	byKBUser  map[kbUserKey]uuid.UUID
	messages  map[uuid.UUID][]*inquiry.Message // keyed by inquiry ID
}

// New returns an empty in-memory Repository.
func New() *Repository {
	return &Repository{
		inquiries: make(map[uuid.UUID]*inquiry.Inquiry),
		byKBUser:  make(map[kbUserKey]uuid.UUID),
		messages:  make(map[uuid.UUID][]*inquiry.Message),
	}
}

func (r *Repository) GetOrCreate(_ context.Context, userID, kbID uuid.UUID) (*inquiry.Inquiry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := kbUserKey{kbID: kbID, userID: userID}
	if id, ok := r.byKBUser[key]; ok {
		cp := *r.inquiries[id]
		return &cp, nil
	}
	now := time.Now()
	inq := &inquiry.Inquiry{ID: uuid.New(), KBID: kbID, UserID: userID, CreatedAt: now, UpdatedAt: now}
	r.inquiries[inq.ID] = inq
	r.byKBUser[key] = inq.ID
	cp := *inq
	return &cp, nil
}

func (r *Repository) Get(_ context.Context, userID, kbID uuid.UUID) (*inquiry.Inquiry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byKBUser[kbUserKey{kbID: kbID, userID: userID}]
	if !ok {
		return nil, inquiry.ErrNotFound
	}
	cp := *r.inquiries[id]
	return &cp, nil
}

func (r *Repository) AppendMessage(_ context.Context, userID uuid.UUID, msg *inquiry.Message) (*inquiry.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inq, ok := r.inquiries[msg.InquiryID]
	if !ok || inq.UserID != userID {
		return nil, inquiry.ErrNotFound
	}
	cp := *msg
	cp.ID = uuid.New()
	cp.UserID = userID
	cp.Ordinal = len(r.messages[msg.InquiryID])
	cp.CreatedAt = time.Now()
	r.messages[msg.InquiryID] = append(r.messages[msg.InquiryID], &cp)
	out := cp
	return &out, nil
}

func (r *Repository) ListMessages(_ context.Context, userID, inquiryID uuid.UUID) ([]*inquiry.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inq, ok := r.inquiries[inquiryID]
	if !ok || inq.UserID != userID {
		return nil, inquiry.ErrNotFound
	}
	out := make([]*inquiry.Message, len(r.messages[inquiryID]))
	copy(out, r.messages[inquiryID])
	sort.Slice(out, func(i, j int) bool { return out[i].Ordinal < out[j].Ordinal })
	return out, nil
}

var _ inquiry.Repository = (*Repository)(nil)
