package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/kunalpednekar/dumpster/internal/auth"
)

type authHandler struct {
	users     auth.UserStore
	jwtSecret string
	jwtTTL    time.Duration
}

func registerAuthRoutes(mux *http.ServeMux, users auth.UserStore, jwtSecret string, jwtTTL time.Duration) {
	h := &authHandler{users: users, jwtSecret: jwtSecret, jwtTTL: jwtTTL}
	mux.HandleFunc("POST /auth/register", h.register)
	mux.HandleFunc("POST /auth/login", h.login)
}

type authRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authResponse struct {
	Token string   `json:"token"`
	User  authUser `json:"user"`
}

type authUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// register creates a new user and returns a signed JWT.
func (h *authHandler) register(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	user, err := h.users.Create(r.Context(), req.Email, hash)
	if err != nil {
		writeError(w, http.StatusConflict, "email already registered")
		return
	}

	token, err := auth.IssueToken(user.ID, h.jwtSecret, h.jwtTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	writeJSON(w, http.StatusCreated, authResponse{
		Token: token,
		User:  authUser{ID: user.ID.String(), Email: user.Email},
	})
}

// login verifies credentials and returns a signed JWT.
func (h *authHandler) login(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	user, err := h.users.GetByEmail(r.Context(), req.Email)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, err := auth.IssueToken(user.ID, h.jwtSecret, h.jwtTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	writeJSON(w, http.StatusOK, authResponse{
		Token: token,
		User:  authUser{ID: user.ID.String(), Email: user.Email},
	})
}

// errDuplicateEmail is a sentinel for duplicate-email errors from UserStore implementations.
var errDuplicateEmail = errors.New("email already registered")
