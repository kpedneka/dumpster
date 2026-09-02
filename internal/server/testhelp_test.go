package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	canonicalmem "github.com/kunalpednekar/dumpster/internal/canonical/memory"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	inquirymem "github.com/kunalpednekar/dumpster/internal/inquiry/memory"
	"github.com/kunalpednekar/dumpster/internal/kb"
	kbmem "github.com/kunalpednekar/dumpster/internal/kb/memory"
	objmock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
	qmem "github.com/kunalpednekar/dumpster/internal/queue/memory"
	"github.com/kunalpednekar/dumpster/internal/search"
	searchmock "github.com/kunalpednekar/dumpster/internal/search/mock"
	"github.com/kunalpednekar/dumpster/internal/session"
	sessionmock "github.com/kunalpednekar/dumpster/internal/session/mock"
	statsmem "github.com/kunalpednekar/dumpster/internal/stats/memory"
)

// testDeps assembles a Deps with all in-memory/mock implementations.
func testDeps(kbRepo kb.Repository, docRepo document.Repository, obj *objmock.Store, pub *qmem.Publisher) Deps {
	return Deps{
		KBs:       kbRepo,
		Docs:      docRepo,
		Objects:   obj,
		Publisher: pub,
		Canonical: canonicalmem.New(),
		Searcher:  searchmock.NewSearcher(search.Result{}),
		Inquiries: inquirymem.New(),
		Sessions:  sessionmock.New(),
		Stats:     statsmem.New(),
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

// authedRequest creates a request carrying a session_id cookie that resolves
// to exactly sessionID. It seeds that session in deps.Sessions (which must be
// a *sessionmock.Store) so the middleware can find it. Pass nil for body when
// no body is needed.
func authedRequest(t *testing.T, deps Deps, method, target string, body io.Reader, sessionID uuid.UUID) *http.Request {
	t.Helper()
	if store, ok := deps.Sessions.(*sessionmock.Store); ok {
		store.Seed(&session.Session{ID: sessionID, CreatedAt: time.Now()})
	}
	req := httptest.NewRequest(method, target, body)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID.String()})
	return req
}

// unauthRequest creates a request with no session cookie, causing the
// middleware to mint a fresh anonymous session. Useful for testing routes
// that must work even on a brand-new, cookie-less client.
func unauthRequest(method, target string, body io.Reader) *http.Request {
	return httptest.NewRequest(method, target, body)
}
