package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/kb"
)

type kbHandler struct {
	repo kb.Repository
}

func registerKBRoutes(mux *http.ServeMux, repo kb.Repository) {
	h := &kbHandler{repo: repo}
	mux.HandleFunc("POST /kbs", h.create)
	mux.HandleFunc("GET /kbs", h.list)
	mux.HandleFunc("GET /kbs/{id}", h.get)
	mux.HandleFunc("PATCH /kbs/{id}", h.rename)
	mux.HandleFunc("DELETE /kbs/{id}", h.delete)
}

func (h *kbHandler) create(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	created, err := h.repo.Create(r.Context(), userID, body.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create knowledge base")
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

func (h *kbHandler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	limit, after := parsePagination(r)

	kbs, err := h.repo.List(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list knowledge bases")
		return
	}

	writeJSON(w, http.StatusOK, paginateKBs(kbs, limit, after))
}

func (h *kbHandler) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	id, ok := parseUUID(w, r.PathValue("id"))
	if !ok {
		return
	}

	k, err := h.repo.Get(r.Context(), userID, id)
	if err != nil {
		writeKBError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, k)
}

func (h *kbHandler) rename(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	id, ok := parseUUID(w, r.PathValue("id"))
	if !ok {
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	updated, err := h.repo.Rename(r.Context(), userID, id, body.Name)
	if err != nil {
		writeKBError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

func (h *kbHandler) delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	id, ok := parseUUID(w, r.PathValue("id"))
	if !ok {
		return
	}

	if err := h.repo.Delete(r.Context(), userID, id); err != nil {
		writeKBError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// requireUserID extracts the authenticated user UUID from the request context.
// It writes a 401 and returns false if the context carries no user.
func requireUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
	}
	return id, ok
}

// parseUUID parses s as a UUID. On failure it writes 400 and returns false.
func parseUUID(w http.ResponseWriter, s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(s)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return uuid.Nil, false
	}
	return id, true
}

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeKBError translates a kb.Repository error to the appropriate HTTP status.
func writeKBError(w http.ResponseWriter, err error) {
	if errors.Is(err, kb.ErrNotFound) {
		writeError(w, http.StatusNotFound, "knowledge base not found")
	} else {
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
