package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/kb"
	kbmem "github.com/kunalpednekar/dumpster/internal/kb/memory"
	objmock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
	qmem "github.com/kunalpednekar/dumpster/internal/queue/memory"
	"github.com/kunalpednekar/dumpster/internal/search"
	searchmock "github.com/kunalpednekar/dumpster/internal/search/mock"
)

const testSecret = "test-secret-key"

// testDeps assembles a Deps with all in-memory/mock implementations.
func testDeps(kbRepo kb.Repository, docRepo document.Repository, obj *objmock.Store, pub *qmem.Publisher) Deps {
	return Deps{
		KBs:       kbRepo,
		Docs:      docRepo,
		Objects:   obj,
		Publisher: pub,
		Searcher:  searchmock.NewSearcher(search.Result{}),
		JWTSecret: testSecret,
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

// mintToken issues a JWT for the given user signed with testSecret.
func mintToken(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	tok, err := auth.IssueToken(userID, testSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// authedRequest creates a request with a Bearer token for userID and the
// given body. Pass nil for body when no body is needed.
func authedRequest(t *testing.T, method, target string, body io.Reader, userID uuid.UUID) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Authorization", "Bearer "+mintToken(t, userID))
	return req
}
