package server

import (
	"net/http"
	"time"

	"github.com/kunalpednekar/dumpster/internal/account"
	"github.com/kunalpednekar/dumpster/internal/auth"
)

// accountHandler exposes the authenticated user's demo-account TTL status,
// backing the in-app day-6 warning banner. It uses account.TTLDays /
// account.WarningWindowDays so the banner and the lifecycle sweep
// (internal/account.Sweep) always agree on the boundary.
type accountHandler struct {
	users auth.LocalUserStore
}

func registerAccountRoutes(mux *http.ServeMux, users auth.LocalUserStore) {
	h := &accountHandler{users: users}
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

	u, err := h.users.GetByID(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load account")
		return
	}

	days := int(time.Since(u.CreatedAt).Hours() / 24)
	writeJSON(w, http.StatusOK, accountStatusResponse{
		WarningActive: days >= account.WarningWindowDays,
		DeletesAt:     u.CreatedAt.AddDate(0, 0, account.TTLDays),
	})
}
