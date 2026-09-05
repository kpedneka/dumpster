// Package mock provides an in-memory ObjectStore for use in tests.
package mock

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/kunalpednekar/dumpster/internal/objectstore"
)

type Store struct {
	mu      sync.RWMutex
	objects map[string][]byte
	// DeleteErrFor, when set for a key, is returned by Delete for that key
	// instead of removing it, so tests can exercise partial-failure paths.
	DeleteErrFor map[string]error
}

func New() *Store {
	return &Store{objects: make(map[string][]byte)}
}

func (s *Store) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.objects[key] = data
	s.mu.Unlock()
	return nil
}

func (s *Store) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.RLock()
	data, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("mock store: key %q not found", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *Store) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.DeleteErrFor[key]; ok {
		return err
	}
	delete(s.objects, key)
	return nil
}

func (s *Store) PresignedURL(_ context.Context, key string, _ time.Duration) (string, error) {
	s.mu.RLock()
	_, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("mock store: key %q not found", key)
	}
	return "http://mock/" + key, nil
}

// PresignedPutURL returns a fake URL unconditionally, unlike PresignedURL —
// a real presigned PUT URL is valid for a key that doesn't exist yet (the
// whole point is to create it), so there's no existing object to check
// against here.
func (s *Store) PresignedPutURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "http://mock/" + key, nil
}

// Compile-time check.
var _ objectstore.ObjectStore = (*Store)(nil)
