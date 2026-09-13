package ratelimit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/ratelimit"
	ratelimitMock "github.com/kunalpednekar/dumpster/internal/ratelimit/mock"
)

func TestMiddleware_blocksWhenLimiterDenies(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := ratelimit.Middleware(ratelimitMock.Deny(), next)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("got %d, want 429", w.Code)
	}
}

func TestMiddleware_allowsWhenLimiterPermits(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := ratelimit.Middleware(ratelimitMock.New(), next)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("got %d, want 200", w.Code)
	}
}

// TestMiddleware_failsOpenOnLimiterError verifies that a Limiter error (e.g.
// pgstore's Postgres connection dropping) allows the request through rather
// than blocking it -- rate limiting is defense-in-depth, not a correctness
// gate, so its own backing-store outage shouldn't become a total API outage.
func TestMiddleware_failsOpenOnLimiterError(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := ratelimit.Middleware(ratelimitMock.Failing(errors.New("connection refused")), next)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("got %d, want 200 (fail open)", w.Code)
	}
}

// TestMiddleware_keysOnClientIP verifies that the key passed to Allow is the
// client IP — both from X-Forwarded-For and from RemoteAddr. Keys on the
// LAST X-Forwarded-For entry, not the first: this app sits behind exactly
// one trusted proxy (the ALB), which appends the real connection's source
// IP to the end of any X-Forwarded-For it forwards -- so the last entry is
// the one thing on this header a client can't control, and everything
// before it is attacker-suppliable.
func TestMiddleware_keysOnClientIP(t *testing.T) {
	cases := []struct {
		name        string
		xff         string
		remoteAddr  string
		wantAllowed bool
		wantKey     string
	}{
		{
			name:       "XFF single IP",
			xff:        "10.0.0.1",
			remoteAddr: "127.0.0.1:1234",
			wantKey:    "10.0.0.1",
		},
		{
			// Shape ALB actually produces: it appends the real client IP
			// (192.168.0.1) to whatever X-Forwarded-For chain it received.
			name:       "XFF multiple hops keys on the last (ALB-appended) entry",
			xff:        "10.0.0.2, 172.16.0.1, 192.168.0.1",
			remoteAddr: "127.0.0.1:1234",
			wantKey:    "192.168.0.1",
		},
		{
			// Regression test for the actual vulnerability: a client that
			// prepends a fake leading entry must not be able to make
			// requests key on it -- every request from the same real
			// connection must key on the same value regardless of what
			// the client claims came before it.
			name:       "spoofed leading XFF entry does not change the key",
			xff:        "203.0.113.99, 192.168.0.1",
			remoteAddr: "127.0.0.1:1234",
			wantKey:    "192.168.0.1",
		},
		{
			name:       "no XFF falls back to RemoteAddr",
			remoteAddr: "203.0.113.5:9999",
			wantKey:    "203.0.113.5",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotKey string
			limiter := &ratelimitMock.Limiter{
				AllowFn: func(_ context.Context, key string) (bool, error) {
					gotKey = key
					return true, nil
				},
			}
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			h := ratelimit.Middleware(limiter, next)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)

			if gotKey != tc.wantKey {
				t.Errorf("Allow key: got %q, want %q", gotKey, tc.wantKey)
			}
		})
	}
}
