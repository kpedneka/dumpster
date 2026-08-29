package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/manifest"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/ratelimit"
	"github.com/kunalpednekar/dumpster/internal/search"
	"github.com/kunalpednekar/dumpster/internal/session"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

// Deps holds all dependencies required by the HTTP handlers.
type Deps struct {
	KBs       kb.Repository
	Docs      document.Repository
	Objects   objectstore.ObjectStore
	Publisher queue.Publisher
	Manifest  manifest.Repository // optional; used for ingestion manifest summaries on PDF/image docs
	Searcher  search.Searcher
	Sessions  session.SessionStore
	// Instruments is optional; when nil, document upload/delete metrics are
	// not recorded.
	Instruments    *telemetry.Instruments
	MaxUploadBytes int64 // 0 defaults to 32 MiB
	// RateLimiter is applied to all non-healthz routes (including session
	// creation) keyed by client IP. Nil disables IP rate limiting.
	RateLimiter ratelimit.Limiter
	// MaxDocumentsPerSession caps the number of documents a single session
	// may upload across all knowledge bases. 0 defaults to 20.
	MaxDocumentsPerSession int
	// SPADir is the path to the built React SPA (e.g. "web/dist"). When set,
	// the router serves static assets from that directory and falls back to
	// index.html for browser navigation. Leave empty to disable SPA serving
	// (development: Vite dev server handles this instead).
	SPADir string
	// CookieSecure controls the Secure attribute on the session cookie
	// (config.Config.CookieSecure). Defaults to false (the Go zero value) if
	// unset, so callers must set it explicitly to true in any environment
	// reached over TLS.
	CookieSecure bool
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
	registerDocRoutes(authed, deps.KBs, deps.Docs, deps.Objects, deps.Publisher, deps.Manifest, deps.Instruments, deps.MaxUploadBytes, deps.MaxDocumentsPerSession)
	registerAccountRoutes(authed, deps.Sessions)
	if deps.Searcher != nil {
		registerSearchRoutes(authed, deps.KBs, deps.Docs, deps.Searcher, deps.Instruments)
	}

	handler := auth.Middleware(deps.Sessions, deps.CookieSecure, authed)
	if deps.RateLimiter != nil {
		handler = ratelimit.Middleware(deps.RateLimiter, handler)
	}

	// When SPADir is set (production), serve static assets and wrap the API
	// handler so browser navigation returns index.html instead of JSON 404s.
	if deps.SPADir != "" {
		spaFS := http.Dir(deps.SPADir)
		mux.Handle("/assets/", http.FileServer(spaFS))
		mux.Handle("/favicon.ico", http.FileServer(spaFS))
		handler = spaFallback(handler, deps.SPADir+"/index.html")
	}
	mux.Handle("/", handler)

	return mux
}

// spaFallback serves index.html for browser navigation (GET requests that
// include "text/html" in their Accept header) so React Router can handle
// client-side routing. API calls from fetch() use Accept: application/json
// or Accept: */* and pass through to the wrapped handler unchanged.
func spaFallback(next http.Handler, indexPath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.ServeFile(w, r, indexPath)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
