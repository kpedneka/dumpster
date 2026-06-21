// Package mock provides test doubles for the interfaces defined in
// internal/auth, following the function-pointer-field pattern used
// throughout the codebase (see internal/llm/mock).
package mock

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
)

// Verifier is a test double for auth.SessionVerifier.
type Verifier struct {
	VerifyFn func(ctx context.Context, token string) (auth.Identity, error)
}

// NewVerifier returns a Verifier that accepts any token and always returns identity.
func NewVerifier(identity auth.Identity) *Verifier {
	return &Verifier{
		VerifyFn: func(_ context.Context, _ string) (auth.Identity, error) {
			return identity, nil
		},
	}
}

// NewErrorVerifier returns a Verifier whose Verify call always fails with err.
func NewErrorVerifier(err error) *Verifier {
	return &Verifier{
		VerifyFn: func(_ context.Context, _ string) (auth.Identity, error) {
			return auth.Identity{}, err
		},
	}
}

func (v *Verifier) Verify(ctx context.Context, token string) (auth.Identity, error) {
	return v.VerifyFn(ctx, token)
}

// UserStore is an in-memory test double for auth.LocalUserStore.
type UserStore struct {
	mu      sync.Mutex
	byID    map[uuid.UUID]*auth.User
	byClerk map[string]*auth.User
	// GetOrCreateErr, when set, is returned by every GetOrCreateByClerkID call.
	GetOrCreateErr error
}

// NewUserStore returns an empty in-memory LocalUserStore.
func NewUserStore() *UserStore {
	return &UserStore{
		byID:    make(map[uuid.UUID]*auth.User),
		byClerk: make(map[string]*auth.User),
	}
}

// Seed registers a User under clerkUserID directly, bypassing the usual
// uuid.New() assignment. Useful in tests that need to control the exact
// local UUID a given Clerk identity resolves to (e.g. so a test can
// pre-create domain data under a known user_id and then authenticate as
// that same user through the HTTP layer).
func (s *UserStore) Seed(u *auth.User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byClerk[u.ClerkUserID] = u
	s.byID[u.ID] = u
}

func (s *UserStore) GetOrCreateByClerkID(_ context.Context, clerkUserID, email string) (*auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.GetOrCreateErr != nil {
		return nil, s.GetOrCreateErr
	}
	if u, ok := s.byClerk[clerkUserID]; ok {
		return u, nil
	}
	u := &auth.User{ID: uuid.New(), ClerkUserID: clerkUserID, Email: email}
	s.byClerk[clerkUserID] = u
	s.byID[u.ID] = u
	return u, nil
}

func (s *UserStore) GetByClerkID(_ context.Context, clerkUserID string) (*auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byClerk[clerkUserID]
	if !ok {
		return nil, auth.ErrUserNotFound
	}
	return u, nil
}

func (s *UserStore) GetByID(_ context.Context, id uuid.UUID) (*auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return nil, auth.ErrUserNotFound
	}
	return u, nil
}

func (s *UserStore) DeleteByClerkID(_ context.Context, clerkUserID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byClerk[clerkUserID]
	if !ok {
		return nil
	}
	delete(s.byClerk, clerkUserID)
	delete(s.byID, u.ID)
	return nil
}

var (
	_ auth.SessionVerifier = (*Verifier)(nil)
	_ auth.LocalUserStore  = (*UserStore)(nil)
)
