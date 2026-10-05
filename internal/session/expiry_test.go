package session_test

import (
	"testing"
	"time"

	"github.com/kunalpednekar/dumpster/internal/session"
)

func TestExpired(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name                string
		created, lastActive time.Duration // how long ago
		want                bool
	}{
		{"fresh", time.Hour, time.Minute, false},
		{"idle just under the timeout", 2 * time.Hour, session.IdleTimeout - time.Second, false},
		{"idle exactly at the timeout", 7 * time.Hour, session.IdleTimeout, true},
		{"idle past the timeout", 8 * time.Hour, 7 * time.Hour, true},
		{"active but at the hard cap", session.HardCap, time.Minute, true},
		{"active just under the hard cap", session.HardCap - time.Second, time.Minute, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &session.Session{CreatedAt: now.Add(-tc.created), LastActiveAt: now.Add(-tc.lastActive)}
			if got := s.Expired(now); got != tc.want {
				t.Errorf("Expired = %v, want %v", got, tc.want)
			}
		})
	}
}
