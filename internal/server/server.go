package server

import (
	"encoding/json"
	"net/http"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/search"
)

// Deps holds all dependencies required by the HTTP handlers.
type Deps struct {
	KBs            kb.Repository
	Docs           document.Repository
	Objects        objectstore.ObjectStore
	Publisher      queue.Publisher
	Searcher       search.Searcher
	JWTSecret      string
	MaxUploadBytes int64 // maximum multipart upload size in bytes; 0 → 32 MiB
}

// NewRouter constructs the application HTTP router.
// The /healthz and GET /openapi.yaml routes are public; all other routes require a valid Bearer JWT.
func NewRouter(deps Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /openapi.yaml", serveOpenAPI)

	authed := http.NewServeMux()
	registerKBRoutes(authed, deps.KBs)
	registerDocRoutes(authed, deps.KBs, deps.Docs, deps.Objects, deps.Publisher, deps.MaxUploadBytes)
	if deps.Searcher != nil {
		registerSearchRoutes(authed, deps.KBs, deps.Searcher)
	}

	mux.Handle("/", auth.Middleware(deps.JWTSecret, authed))

	return mux
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
