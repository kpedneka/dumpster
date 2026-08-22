package server

import (
	"net/http"
	"time"

	"github.com/kunalpednekar/dumpster/internal/account"
	"github.com/kunalpednekar/dumpster/internal/session"
)

// accountHandler exposes the authenticated session's demo-account expiry status,
// backing the in-app pre-deletion warning banner. It uses the same constants as
// internal/account.Sweep so the banner and sweep always agree on expiry boundaries.
type accountHandler struct {
	sessions session.SessionStore
}

func registerAccountRoutes(mux *http.ServeMux, sessions session.SessionStore) {
	h := &accountHandler{sessions: sessions}
	mux.HandleFunc("GET /account/status", h.status)
}

type accountStatusResponse struct {
	WarningActive bool      `json:"warning_active"`
	DeletesAt     time.Time `json:"deletes_at"`
}

func (h *accountHandler) status(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	s, err := h.sessions.GetByID(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load account")
		return
	}

	now := time.Now()
	hardCapExpiry := s.CreatedAt.Add(account.HardCap)
	idleExpiry := s.LastActiveAt.Add(account.IdleTimeout)

	// Report whichever clock fires first.
	deletesAt := hardCapExpiry
	if idleExpiry.Before(hardCapExpiry) {
		deletesAt = idleExpiry
	}

	writeJSON(w, http.StatusOK, accountStatusResponse{
		WarningActive: deletesAt.Sub(now) <= account.WarningLeadTime,
		DeletesAt:     deletesAt,
	})
}
