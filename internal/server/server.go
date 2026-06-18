package server

import (
	"encoding/json"
	"net/http"
	"time"

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
	Users          auth.UserStore
	JWTSecret      string
	JWTTTL         time.Duration // 0 defaults to 24 h
	MaxUploadBytes int64         // 0 defaults to 32 MiB
}

// NewRouter constructs the application HTTP router.
// POST /auth/register and POST /auth/login are public; all other routes require a valid Bearer JWT.
func NewRouter(deps Deps) http.Handler {
	ttl := deps.JWTTTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /openapi.yaml", serveOpenAPI)

	if deps.Users != nil {
		registerAuthRoutes(mux, deps.Users, deps.JWTSecret, ttl)
	}

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
