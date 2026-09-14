package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/canonical"
	"github.com/kunalpednekar/dumpster/internal/community"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/inquiry"
	"github.com/kunalpednekar/dumpster/internal/intrusion"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/manifest"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/ratelimit"
	"github.com/kunalpednekar/dumpster/internal/relation"
	"github.com/kunalpednekar/dumpster/internal/search"
	"github.com/kunalpednekar/dumpster/internal/session"
	"github.com/kunalpednekar/dumpster/internal/stats"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
	"github.com/kunalpednekar/dumpster/internal/theme"
)

// Deps holds all dependencies required by the HTTP handlers.
type Deps struct {
	KBs       kb.Repository
	Docs      document.Repository
	Objects   objectstore.ObjectStore
	Publisher queue.Publisher
	// JobStatusReader is optional; when nil, GET /kbs/{id}/documents omits
	// per-document progress info and the frontend falls back to just the
	// coarse document.Status.
	JobStatusReader queue.JobStatusReader
	Manifest        manifest.Repository // optional; used for ingestion manifest summaries on PDF/image docs
	// Canonical is optional; when nil, deleting a document skips reversing
	// its contribution to canonical entity stats (acceptable before
	// canonicalization is wired up — there is nothing to reverse yet).
	Canonical canonical.Repository
	// Communities is optional; when nil, POST/GET /kbs/{id}/communities are
	// not registered.
	Communities community.Repository
	// MaxCommunityGraphEntities and CommunityDetectionTimeout guard
	// POST /kbs/{id}/communities against a pathological KB. Both 0 default
	// (5000 entities, 30s) — see communityhandler.go.
	MaxCommunityGraphEntities int
	CommunityDetectionTimeout time.Duration
	// Themes and ThemeSummarizer are optional together; when either is nil,
	// POST/GET /kbs/{id}/themes are not registered.
	Themes          theme.Repository
	ThemeSummarizer *theme.Summarizer
	// Intrusion and IntrusionTester are optional together; when either is
	// nil, POST/GET /kbs/{id}/intrusion-test are not registered. Reuses
	// Themes as its intrusion.MemberSource (the community-membership query
	// both features need is identical) rather than taking a separate
	// dependency for it.
	Intrusion       intrusion.Repository
	IntrusionTester *intrusion.Tester
	// Relations and RelationExtractor are optional together; when either
	// is nil, POST /kbs/{id}/relations is not registered.
	Relations         relation.Repository
	RelationExtractor *relation.Extractor
	// MaxRelationChunksPerRun bounds POST /kbs/{id}/relations; 0 defaults
	// (see relationhandler.go's defaultMaxRelationChunksPerRun).
	MaxRelationChunksPerRun int
	Searcher                search.Searcher
	// Inquiries is optional; when nil, search results are not persisted as
	// Inquiry history (search itself still works — persistence is a
	// convenience layered on top, not a dependency search needs).
	Inquiries inquiry.Repository
	Sessions  session.SessionStore
	// Stats is optional; when nil, the public GET /stats endpoint is not
	// registered and query execution doesn't get recorded into it.
	Stats stats.Repository
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
	// CookieSecure controls the Secure attribute on the session cookie
	// (config.Config.CookieSecure). Defaults to false (the Go zero value) if
	// unset, so callers must set it explicitly to true in any environment
	// reached over TLS.
	CookieSecure bool
}

// NewRouter constructs the application HTTP router. Every route except
// /healthz lives under /api/ -- see the "Decouple SPA hosting from the API
// instance" dev board card for why: CloudFront picks an origin per request
// using only the URL path (it can't inspect the Accept header the way the
// single-instance same-origin setup this replaced used to), and the SPA's
// own client-side routes (e.g. /kbs/:kbId) would otherwise collide with
// real API paths that share the same shape (GET /kbs/{id}) -- both are
// legitimate requests to the identical path that need to reach different
// origins (S3 for browser navigation, the API for the SPA's own fetch
// calls). The /api prefix is what makes that decision unambiguous by path
// alone. /healthz stays unprefixed: it's an infra-level probe (the ALB's
// own health check hits it directly against the container, never through
// CloudFront's routing at all), not a frontend/backend disambiguation.
//
// All routes under /api/ (other than /openapi.yaml and /stats) are wrapped
// by anonymous session middleware, which mints a new session cookie when
// none is present and never returns 401.
func NewRouter(deps Deps) http.Handler {
	api := http.NewServeMux()

	api.HandleFunc("GET /openapi.yaml", serveOpenAPI)
	if deps.Stats != nil {
		registerStatsRoutes(api, deps.Stats)
	}

	authed := http.NewServeMux()
	registerKBRoutes(authed, deps.KBs)
	registerDocRoutes(authed, deps.KBs, deps.Docs, deps.Objects, deps.Publisher, deps.JobStatusReader, deps.Manifest, deps.Canonical, deps.Instruments, deps.MaxUploadBytes, deps.MaxDocumentsPerSession)
	registerAccountRoutes(authed, deps.Sessions)
	if deps.Searcher != nil {
		registerSearchRoutes(authed, deps.KBs, deps.Docs, deps.Searcher, deps.Inquiries, deps.Instruments, deps.Stats)
	}
	if deps.Communities != nil {
		registerCommunityRoutes(authed, deps.KBs, deps.Communities, deps.MaxCommunityGraphEntities, deps.CommunityDetectionTimeout)
	}
	if deps.Themes != nil && deps.ThemeSummarizer != nil {
		registerThemeRoutes(authed, deps.KBs, deps.Themes, deps.ThemeSummarizer)
	}
	if deps.Themes != nil && deps.Intrusion != nil && deps.IntrusionTester != nil {
		registerIntrusionRoutes(authed, deps.KBs, deps.Themes, deps.Intrusion, deps.IntrusionTester)
	}
	if deps.Relations != nil && deps.RelationExtractor != nil {
		registerRelationRoutes(authed, deps.KBs, deps.Relations, deps.RelationExtractor, deps.MaxRelationChunksPerRun)
	}

	handler := auth.Middleware(deps.Sessions, deps.CookieSecure, deps.Stats, authed)
	if deps.RateLimiter != nil {
		handler = ratelimit.Middleware(deps.RateLimiter, handler)
	}
	api.Handle("/", handler)

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", healthz)
	root.Handle("/api/", http.StripPrefix("/api", api))
	return root
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
