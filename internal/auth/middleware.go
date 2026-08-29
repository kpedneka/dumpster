package auth

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/session"
)

const sessionCookieName = "session_id"

// Middleware returns an HTTP handler that resolves an anonymous session from
// the request cookie. If the cookie is absent, unparseable, or refers to a
// session that no longer exists (e.g. already swept), a fresh session is
// minted and a new Set-Cookie header is set. Either way, the session UUID is
// placed in the request context via WithUserID — the same context contract
// every repository and the RLS TxRunner depend on.
//
// secure controls the Set-Cookie Secure attribute (config.Config.CookieSecure).
// It must be true wherever the API is served over TLS — a Secure cookie sent
// over plain http is silently dropped by the browser, which would otherwise
// mint a fresh session (and therefore a fresh tenant identity) on every request.
//
// This handler never returns 401: every request gets a valid session identity.
func Middleware(sessions session.SessionStore, secure bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := resolveSession(r, sessions)
		if sess == nil {
			var err error
			sess, err = sessions.Create(r.Context())
			if err != nil {
				slog.Error("auth: create session", "err", err)
				http.Error(w, "service unavailable", http.StatusServiceUnavailable)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookieName,
				Value:    sess.ID.String(),
				HttpOnly: true,
				Secure:   secure,
				SameSite: http.SameSiteLaxMode,
				Path:     "/",
			})
		}

		next.ServeHTTP(w, r.WithContext(WithUserID(r.Context(), sess.ID)))
	})
}

// resolveSession looks up the session cookie and returns the live session, or
// nil if the cookie is absent, malformed, or the session has been swept.
func resolveSession(r *http.Request, sessions session.SessionStore) *session.Session {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	id, err := uuid.Parse(cookie.Value)
	if err != nil {
		return nil
	}
	sess, err := sessions.GetByID(r.Context(), id)
	if err != nil {
		return nil
	}
	if touchErr := sessions.Touch(r.Context(), sess.ID, time.Now()); touchErr != nil {
		slog.Warn("auth: touch session", "session_id", sess.ID, "err", touchErr)
	}
	return sess
}
