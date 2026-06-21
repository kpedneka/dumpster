package auth

import (
	"log/slog"
	"net/http"
	"strings"
)

// Middleware returns an HTTP handler that enforces Clerk-managed session
// auth. It verifies the bearer token via verifier, maps the resulting
// external Clerk identity to a local app user via users (creating the
// local row on first sight of that identity), and places the local user's
// UUID in the request context via WithUserID — the same context contract
// every repository and the RLS TxRunner already rely on.
//
// Unauthenticated or invalid requests receive 401.
func Middleware(verifier SessionVerifier, users LocalUserStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		identity, err := verifier.Verify(r.Context(), token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		user, err := users.GetOrCreateByClerkID(r.Context(), identity.ClerkUserID, identity.Email)
		if err != nil {
			slog.Error("auth: resolve local user", "err", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r.WithContext(WithUserID(r.Context(), user.ID)))
	})
}

func bearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(v, "Bearer ")
}
