package objectstore_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/mock"
)

// Compile-time check.
var _ objectstore.ObjectStore = (*mock.Store)(nil)

func TestObjectStore_RoundTrip(t *testing.T) {
	store := mock.New()
	ctx := context.Background()
	content := "hello, dumpster"

	if err := store.Put(ctx, "test/file.txt", strings.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		t.Fatal(err)
	}

	rc, err := store.Get(ctx, "test/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("got %q, want %q", got, content)
	}
}

func TestObjectStore_Delete(t *testing.T) {
	store := mock.New()
	ctx := context.Background()

	if err := store.Put(ctx, "to-delete", bytes.NewReader([]byte("x")), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "to-delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "to-delete"); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestObjectStore_PresignedURL(t *testing.T) {
	store := mock.New()
	ctx := context.Background()

	if err := store.Put(ctx, "doc/f.pdf", bytes.NewReader([]byte("pdf")), 3, "application/pdf"); err != nil {
		t.Fatal(err)
	}
	url, err := store.PresignedURL(ctx, "doc/f.pdf", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if url == "" {
		t.Fatal("presigned URL is empty")
	}
}

func TestObjectStore_PresignedURL_missingKey(t *testing.T) {
	store := mock.New()
	if _, err := store.PresignedURL(context.Background(), "missing", time.Hour); err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestObjectStore_Get_missingKey(t *testing.T) {
	store := mock.New()
	if _, err := store.Get(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for missing key")
	}
}
