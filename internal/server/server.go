package server

import (
	"encoding/json"
	"net/http"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/manifest"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/search"
	"github.com/kunalpednekar/dumpster/internal/session"
)

// Deps holds all dependencies required by the HTTP handlers.
type Deps struct {
	KBs            kb.Repository
	Docs           document.Repository
	Objects        objectstore.ObjectStore
	Publisher      queue.Publisher
	Manifest       manifest.Repository // optional; used for ingestion manifest summaries on PDF/image docs
	Searcher       search.Searcher
	Sessions       session.SessionStore
	MaxUploadBytes int64 // 0 defaults to 32 MiB
}

// NewRouter constructs the application HTTP router. All routes (other than
// /healthz and /openapi.yaml) are wrapped by anonymous session middleware,
// which mints a new session cookie when none is present and never returns 401.
func NewRouter(deps Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /openapi.yaml", serveOpenAPI)

	authed := http.NewServeMux()
	registerKBRoutes(authed, deps.KBs)
	registerDocRoutes(authed, deps.KBs, deps.Docs, deps.Objects, deps.Publisher, deps.Manifest, deps.MaxUploadBytes)
	registerAccountRoutes(authed, deps.Sessions)
	if deps.Searcher != nil {
		registerSearchRoutes(authed, deps.KBs, deps.Searcher)
	}

	mux.Handle("/", auth.Middleware(deps.Sessions, authed))

	return mux
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
