package mock_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kunalpednekar/dumpster/internal/objectstore/mock"
)

func TestPutAndGet(t *testing.T) {
	s := mock.New()
	content := "hello world"

	if err := s.Put(context.Background(), "key1", strings.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		t.Fatal(err)
	}

	rc, err := s.Get(context.Background(), "key1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()

	got, _ := io.ReadAll(rc)
	if string(got) != content {
		t.Errorf("content: got %q, want %q", got, content)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := mock.New()
	_, err := s.Get(context.Background(), "missing")
	if err == nil {
		t.Error("expected error for missing key")
	}
}

func TestDelete(t *testing.T) {
	s := mock.New()
	_ = s.Put(context.Background(), "k", bytes.NewReader([]byte("x")), 1, "text/plain")

	if err := s.Delete(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "k"); err == nil {
		t.Error("expected key to be gone after delete")
	}
}

func TestDelete_MissingKey(t *testing.T) {
	s := mock.New()
	// Deleting a non-existent key should succeed silently.
	if err := s.Delete(context.Background(), "nope"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPresignedURL(t *testing.T) {
	s := mock.New()
	_ = s.Put(context.Background(), "doc/file.txt", strings.NewReader("data"), 4, "text/plain")

	url, err := s.PresignedURL(context.Background(), "doc/file.txt", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if url == "" {
		t.Error("expected non-empty URL")
	}
}

func TestPresignedURL_NotFound(t *testing.T) {
	s := mock.New()
	_, err := s.PresignedURL(context.Background(), "missing", time.Minute)
	if err == nil {
		t.Error("expected error for missing key")
	}
}
