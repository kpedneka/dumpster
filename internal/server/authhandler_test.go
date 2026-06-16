package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
)

// memUserStore is an in-memory auth.UserStore for tests.
type memUserStore struct {
	mu    sync.Mutex
	byID  map[uuid.UUID]*auth.User
	byEmail map[string]*auth.User
}

func newMemUserStore() *memUserStore {
	return &memUserStore{
		byID:    make(map[uuid.UUID]*auth.User),
		byEmail: make(map[string]*auth.User),
	}
}

func (s *memUserStore) Create(_ context.Context, email, passwordHash string) (*auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byEmail[email]; exists {
		return nil, auth.ErrDuplicateEmail
	}
	u := &auth.User{
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: passwordHash,
		CreatedAt:    time.Now(),
	}
	s.byID[u.ID] = u
	s.byEmail[email] = u
	return u, nil
}

func (s *memUserStore) GetByEmail(_ context.Context, email string) (*auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byEmail[email]
	if !ok {
		return nil, fmt.Errorf("user not found")
	}
	return u, nil
}

func (s *memUserStore) GetByID(_ context.Context, id uuid.UUID) (*auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("user not found")
	}
	return u, nil
}

func authDeps(users auth.UserStore) Deps {
	deps, _, _, _, _ := defaultDeps()
	deps.Users = users
	deps.JWTTTL = time.Hour
	return deps
}

func TestRegister_success(t *testing.T) {
	users := newMemUserStore()
	router := NewRouter(authDeps(users))

	body := `{"email":"alice@example.com","password":"s3cr3t"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body: %s", w.Code, w.Body.String())
	}

	var resp authResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" {
		t.Error("expected non-empty token")
	}
	if resp.User.Email != "alice@example.com" {
		t.Errorf("email: got %q, want %q", resp.User.Email, "alice@example.com")
	}
}

func TestRegister_duplicateEmail(t *testing.T) {
	users := newMemUserStore()
	router := NewRouter(authDeps(users))

	body := `{"email":"alice@example.com","password":"s3cr3t"}`
	for i, want := range []int{http.StatusCreated, http.StatusConflict} {
		req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("attempt %d: status: got %d, want %d", i+1, w.Code, want)
		}
	}
}

func TestRegister_missingFields(t *testing.T) {
	users := newMemUserStore()
	router := NewRouter(authDeps(users))

	cases := []string{
		`{"email":"","password":"s3cr3t"}`,
		`{"email":"alice@example.com","password":""}`,
		`{}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: status: got %d, want 400", body, w.Code)
		}
	}
}

func TestLogin_success(t *testing.T) {
	users := newMemUserStore()
	router := NewRouter(authDeps(users))

	// Register first
	reg := `{"email":"bob@example.com","password":"pass123"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(reg))
	req.Header.Set("Content-Type", "application/json")
	httptest.NewRecorder() // discard

	router.ServeHTTP(httptest.NewRecorder(), req)

	// Login
	login := `{"email":"bob@example.com","password":"pass123"}`
	req2 := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(login))
	req2.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req2)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp authResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" {
		t.Error("expected non-empty token")
	}
}

func TestLogin_wrongPassword(t *testing.T) {
	users := newMemUserStore()
	router := NewRouter(authDeps(users))

	reg := `{"email":"carol@example.com","password":"correct"}`
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(reg)))

	login := `{"email":"carol@example.com","password":"wrong"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(login))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}

func TestLogin_unknownEmail(t *testing.T) {
	users := newMemUserStore()
	router := NewRouter(authDeps(users))

	login := `{"email":"nobody@example.com","password":"whatever"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(login))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}
