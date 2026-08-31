package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/inquiry"
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
	// Inquiries is optional; when nil, search results are not persisted as
	// Inquiry history (search itself still works — persistence is a
	// convenience layered on top, not a dependency search needs).
	Inquiries inquiry.Repository
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
		registerSearchRoutes(authed, deps.KBs, deps.Docs, deps.Searcher, deps.Inquiries, deps.Instruments)
	}

	handler := auth.Middleware(deps.Sessions, deps.CookieSecure, authed)
	if deps.RateLimiter != nil {
		handler = ratelimit.Middleware(deps.RateLimiter, handler)
	}

	// When SPADir is set (production), serve static assets and wrap the API
	// handler so browser navigation returns index.html instead of JSON 404s.
	if deps.SPADir != "" {
		handler = spaHandler(deps.SPADir, handler)
	}
	mux.Handle("/", handler)

	return mux
}

// spaHandler serves whatever actually exists as a file under dir — the
// bundled, hashed JS/CSS under /assets/, and anything Vite copied verbatim
// from web/public/ (favicons, etc.) to the dist root — before falling back
// to index.html for browser navigation (GET requests with "text/html" in
// their Accept header) so React Router can handle client-side routes.
// Everything else (API calls, whose Accept is application/json or */*) passes
// through to next unchanged.
//
// A single existence check replaces the old approach of hardcoding "/assets/"
// and "/favicon.ico" as the only static routes: that missed every other
// public/ file (e.g. favicon.svg) since Accept for an <link rel="icon">
// request is image-ish, not text/html, so those requests fell through past
// the SPA fallback into the API mux and 404ed.
func spaHandler(dir string, next http.Handler) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	indexPath := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		if existingFile(dir, r.URL.Path) {
			fileServer.ServeHTTP(w, r)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.ServeFile(w, r, indexPath)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// existingFile reports whether urlPath resolves to a regular file under dir,
// rejecting any path (e.g. "/../go.mod") that would resolve outside dir.
func existingFile(dir, urlPath string) bool {
	root, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	candidate := filepath.Join(root, filepath.Clean("/"+urlPath))
	if candidate != root && !strings.HasPrefix(candidate, root+string(filepath.Separator)) {
		return false
	}
	info, err := os.Stat(candidate)
	return err == nil && !info.IsDir()
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
