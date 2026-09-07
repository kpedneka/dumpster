package evalcorpus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/evalcorpus"
)

func TestCheckAllIndexed_NilForAllIndexed(t *testing.T) {
	docs := []*document.Document{
		{Filename: "a.txt", Status: document.StatusIndexed},
		{Filename: "b.txt", Status: document.StatusIndexed},
	}
	if err := evalcorpus.CheckAllIndexed(docs); err != nil {
		t.Fatalf("CheckAllIndexed = %v, want nil", err)
	}
}

func TestCheckAllIndexed_NamesEveryNonIndexedDocument(t *testing.T) {
	docs := []*document.Document{
		{Filename: "a.txt", Status: document.StatusIndexed},
		{Filename: "b.txt", Status: document.StatusFailed},
		{Filename: "c.txt", Status: document.StatusProcessing},
	}
	err := evalcorpus.CheckAllIndexed(docs)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "b.txt") || !strings.Contains(msg, "failed") {
		t.Errorf("error message %q should name b.txt and its failed status", msg)
	}
	if !strings.Contains(msg, "c.txt") || !strings.Contains(msg, "processing") {
		t.Errorf("error message %q should name c.txt and its processing status", msg)
	}
	if strings.Contains(msg, "a.txt") {
		t.Errorf("error message %q should not mention the already-indexed a.txt", msg)
	}

	var incomplete *evalcorpus.IncompleteDocumentsError
	if !errors.As(err, &incomplete) {
		t.Fatalf("expected an *evalcorpus.IncompleteDocumentsError, got %T", err)
	}
	if len(incomplete.NotIndexed) != 2 {
		t.Errorf("NotIndexed has %d entries, want 2", len(incomplete.NotIndexed))
	}
}
