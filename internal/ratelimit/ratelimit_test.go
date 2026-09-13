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
// client IP — both from X-Forwarded-For and from RemoteAddr.
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
			name:       "XFF multiple hops",
			xff:        "10.0.0.2, 172.16.0.1, 192.168.0.1",
			remoteAddr: "127.0.0.1:1234",
			wantKey:    "10.0.0.2",
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
