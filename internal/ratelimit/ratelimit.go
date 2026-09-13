// Package ratelimit provides the Limiter interface and an HTTP middleware that
// enforces per-IP request limits. Implementations live in sub-packages
// (memory, pgstore, mock) so the limiter strategy is a contained swap.
package ratelimit

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// Limiter decides whether a request identified by key should be allowed.
// Implementations must be safe for concurrent use. Allow takes a context and
// returns an error because a shared-store implementation (pgstore) makes a
// real network call — unlike the original in-process-only signature, which
// couldn't fail or need cancellation.
type Limiter interface {
	Allow(ctx context.Context, key string) (bool, error)
}

// Middleware returns an http.Handler that gates every request through l using
// the client's IP address as the key. Requests that exceed the configured
// limit receive 429 Too Many Requests; allowed requests are forwarded to next.
//
// A Limiter error fails open (the request is allowed, and the error is
// logged) rather than fails closed: rate limiting is a defense-in-depth
// guard against abuse, not a correctness gate, so a transient outage in its
// backing store (e.g. pgstore's Postgres connection) shouldn't turn into a
// total API outage for every request.
func Middleware(l Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, err := l.Allow(r.Context(), clientIP(r))
		if err != nil {
			slog.Error("ratelimit: allow check failed, failing open", "err", err)
			next.ServeHTTP(w, r)
			return
		}
		if !allowed {
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the client IP from X-Forwarded-For or falls back to
// RemoteAddr. It always returns a bare IP with no port component.
//
// Trusts the LAST X-Forwarded-For entry, not the first -- deliberately, not
// a historical accident. This app sits behind exactly one trusted proxy
// (the ALB, see terraform/ecs_networking.tf), and AWS ALB appends the real
// client IP to the end of whatever X-Forwarded-For chain it received rather
// than replacing it. Every entry before that last one is caller-supplied
// and unverified: a client can set X-Forwarded-For to anything before the
// request ever reaches the ALB, so trusting the first (leftmost) entry --
// the naive, commonly-copied reading of this header -- let any client
// obtain a fresh rate-limit quota on every request just by varying a
// header they fully control, defeating the limiter entirely. With exactly
// one trusted hop in front of this app, only the last entry is
// authenticated by that hop; if a second trusted proxy is ever added in
// front of the ALB, this needs to trust the second-to-last entry instead,
// not the first.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		// X-Forwarded-For may be a comma-separated list: "client, proxy1, proxy2".
		// The rightmost address is the one the trusted proxy (ALB) itself
		// appended, not a value the client could have set.
		if idx := strings.LastIndexByte(fwd, ','); idx != -1 {
			fwd = fwd[idx+1:]
		}
		return strings.TrimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
