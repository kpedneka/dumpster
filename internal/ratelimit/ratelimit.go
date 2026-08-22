// Package ratelimit provides the Limiter interface and an HTTP middleware that
// enforces per-IP request limits. Implementations live in sub-packages
// (memory, mock) so the limiter strategy is a contained swap.
package ratelimit

import (
	"net"
	"net/http"
	"strings"
)

// Limiter decides whether a request identified by key should be allowed.
// Implementations must be safe for concurrent use.
type Limiter interface {
	Allow(key string) bool
}

// Middleware returns an http.Handler that gates every request through l using
// the client's IP address as the key. Requests that exceed the configured
// limit receive 429 Too Many Requests; allowed requests are forwarded to next.
func Middleware(l Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(clientIP(r)) {
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the client IP from X-Forwarded-For (trusting the first
// listed address) or falls back to RemoteAddr. It always returns a bare IP
// with no port component.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		// X-Forwarded-For may be a comma-separated list: "client, proxy1, proxy2".
		// The leftmost address is the original client.
		if idx := strings.IndexByte(fwd, ','); idx != -1 {
			fwd = fwd[:idx]
		}
		return strings.TrimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
