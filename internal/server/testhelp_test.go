package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	authmock "github.com/kunalpednekar/dumpster/internal/auth/mock"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/kb"
	kbmem "github.com/kunalpednekar/dumpster/internal/kb/memory"
	objmock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
	qmem "github.com/kunalpednekar/dumpster/internal/queue/memory"
	"github.com/kunalpednekar/dumpster/internal/search"
	searchmock "github.com/kunalpednekar/dumpster/internal/search/mock"
)

// testVerifier is an auth.SessionVerifier for tests: it treats the bearer
// token itself as the asserted Clerk external user ID, with no real
// verification — appropriate since the unit suite never talks to Clerk.
type testVerifier struct{}

func (testVerifier) Verify(_ context.Context, token string) (auth.Identity, error) {
	return auth.Identity{ClerkUserID: token}, nil
}

// clerkIDForUserStore is an auth.LocalUserStore for tests. It derives the
// local user UUID directly from the Clerk ID (which, paired with
// testVerifier, is just whatever token authedRequest minted), so a test
// can choose an arbitrary userID, create domain data under it, and then
// authenticate as that exact user through the HTTP layer.
type clerkIDForUserStore struct {
	*authmock.UserStore
}

func newTestUserStore() *clerkIDForUserStore {
	return &clerkIDForUserStore{UserStore: authmock.NewUserStore()}
}

func (s *clerkIDForUserStore) GetOrCreateByClerkID(ctx context.Context, clerkUserID, email string) (*auth.User, error) {
	if id, ok := parseClerkID(clerkUserID); ok {
		s.Seed(&auth.User{ID: id, ClerkUserID: clerkUserID, Email: email})
	}
	return s.UserStore.GetOrCreateByClerkID(ctx, clerkUserID, email)
}

// clerkIDFor deterministically derives a fake Clerk external ID from a
// local user UUID, so authedRequest and clerkIDForUserStore agree on the
// resulting local identity without any shared mutable state.
func clerkIDFor(userID uuid.UUID) string {
	return "clerk_" + userID.String()
}

func parseClerkID(clerkUserID string) (uuid.UUID, bool) {
	const prefix = "clerk_"
	if len(clerkUserID) <= len(prefix) || clerkUserID[:len(prefix)] != prefix {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(clerkUserID[len(prefix):])
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// testDeps assembles a Deps with all in-memory/mock implementations.
func testDeps(kbRepo kb.Repository, docRepo document.Repository, obj *objmock.Store, pub *qmem.Publisher) Deps {
	return Deps{
		KBs:       kbRepo,
		Docs:      docRepo,
		Objects:   obj,
		Publisher: pub,
		Searcher:  searchmock.NewSearcher(search.Result{}),
		Verifier:  testVerifier{},
		Users:     newTestUserStore(),
	}
}

// defaultDeps returns fresh in-memory deps and their underlying stores.
func defaultDeps() (Deps, *kbmem.Repository, *docmem.Repository, *objmock.Store, *qmem.Publisher) {
	kbRepo := kbmem.New()
	docRepo := docmem.New()
	obj := objmock.New()
	pub := qmem.New()
	return testDeps(kbRepo, docRepo, obj, pub), kbRepo, docRepo, obj, pub
}

// authedRequest creates a request carrying a Bearer token that resolves
// (via clerkIDForUserStore) to exactly userID. Pass nil for body when no
// body is needed.
func authedRequest(t *testing.T, method, target string, body io.Reader, userID uuid.UUID) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Authorization", "Bearer "+clerkIDFor(userID))
	return req
}
