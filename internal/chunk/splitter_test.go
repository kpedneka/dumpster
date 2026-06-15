package chunk_test

import (
	"strings"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/chunk"
)

func TestFixedWindow_SplitsIntoExpectedCount(t *testing.T) {
	s := chunk.NewFixedWindow(10, 2) // 10-char window, 2-char overlap → step=8
	// 24-char text → ordinals 0,1,2
	text := strings.Repeat("ab", 12) // 24 chars
	chunks := s.Split(text)
	if len(chunks) != 3 {
		t.Fatalf("want 3 chunks, got %d", len(chunks))
	}
}

func TestFixedWindow_OffsetRecovery(t *testing.T) {
	s := chunk.NewFixedWindow(10, 2)
	text := "ABCDEFGHIJKLMNOPQRSTUVWX" // 24 chars
	chunks := s.Split(text)

	for i, c := range chunks {
		got := text[c.CharStart:c.CharEnd]
		if got != c.Text {
			t.Errorf("chunk %d: text[%d:%d]=%q but chunk.Text=%q", i, c.CharStart, c.CharEnd, got, c.Text)
		}
	}
}

func TestFixedWindow_OrdinalsAreSequential(t *testing.T) {
	s := chunk.NewFixedWindow(10, 2)
	text := strings.Repeat("x", 30)
	chunks := s.Split(text)

	for i, c := range chunks {
		if c.Ordinal != i {
			t.Errorf("chunk %d has ordinal %d", i, c.Ordinal)
		}
	}
}

func TestFixedWindow_ShortTextFitsOneChunk(t *testing.T) {
	s := chunk.NewFixedWindow(100, 10)
	text := "hello"
	chunks := s.Split(text)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	if chunks[0].CharStart != 0 || chunks[0].CharEnd != len(text) {
		t.Errorf("offsets: got [%d,%d], want [0,%d]", chunks[0].CharStart, chunks[0].CharEnd, len(text))
	}
}

func TestFixedWindow_EmptyTextReturnsNil(t *testing.T) {
	s := chunk.NewFixedWindow(100, 10)
	if chunks := s.Split(""); len(chunks) != 0 {
		t.Fatalf("expected no chunks for empty text, got %d", len(chunks))
	}
}

func TestFixedWindow_OverlapPreservesContext(t *testing.T) {
	s := chunk.NewFixedWindow(10, 4) // step=6
	text := "0123456789abcdef" // 16 chars
	chunks := s.Split(text)

	// chunk[0] covers [0,10), chunk[1] covers [6,16)
	// so text[6:10] = "6789" appears in both
	overlap := text[chunks[1].CharStart:chunks[0].CharEnd]
	if !strings.Contains(chunks[0].Text, overlap) || !strings.Contains(chunks[1].Text, overlap) {
		t.Errorf("overlap text %q should appear in both consecutive chunks", overlap)
	}
}

func TestFixedWindow_TokenCountIsApproximation(t *testing.T) {
	s := chunk.NewFixedWindow(100, 10)
	text := strings.Repeat("a", 80)
	chunks := s.Split(text)
	// TokenCount should be positive
	if chunks[0].TokenCount <= 0 {
		t.Error("expected positive TokenCount")
	}
}
