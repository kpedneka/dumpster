package chunk

import "unicode/utf8"

// Splitter breaks document text into overlapping Chunk segments.
// Returned chunks have Ordinal, Text, CharStart, CharEnd, and TokenCount set;
// DocumentID/KBID/UserID are left as zero values for the caller to fill in.
type Splitter interface {
	Split(text string) []*Chunk
}

// FixedWindow splits text into fixed-size character windows with configurable overlap.
type FixedWindow struct {
	size    int // window width in bytes
	overlap int // overlap width in bytes
}

// NewFixedWindow returns a FixedWindow splitter.
// size is the window width in bytes; overlap is the number of bytes shared
// between consecutive windows. Overlap must be less than size.
func NewFixedWindow(size, overlap int) *FixedWindow {
	return &FixedWindow{size: size, overlap: overlap}
}

// DefaultFixedWindow returns a FixedWindow tuned for plain-text documents:
// 3 000-byte windows (≈750 tokens) with 300-byte overlap (≈10%).
func DefaultFixedWindow() *FixedWindow {
	return NewFixedWindow(3000, 300)
}

// alignToRuneBoundary walks i backward until it lands on a UTF-8 rune
// boundary (or reaches 0). text[start:end] is a raw byte slice, not a
// rune-aware one -- an unadjusted offset landing inside a multi-byte
// character (curly quotes, em dashes, accented letters, all common in
// real documents) produces a chunk whose Text is a truncated, invalid
// UTF-8 fragment, which Postgres's own UTF8 encoding validation then
// rejects outright on insert. Walking backward (never forward) guarantees
// termination at or before 0, since byte 0 of well-formed UTF-8 text is
// always a rune start.
func alignToRuneBoundary(text string, i int) int {
	for i > 0 && i < len(text) && !utf8.RuneStart(text[i]) {
		i--
	}
	return i
}

// Split divides text into overlapping windows. Returns nil for empty input.
func (f *FixedWindow) Split(text string) []*Chunk {
	if len(text) == 0 {
		return nil
	}
	step := f.size - f.overlap
	if step <= 0 {
		step = 1
	}
	var out []*Chunk
	ordinal := 0
	for start := 0; start < len(text); {
		end := start + f.size
		if end >= len(text) {
			end = len(text)
		} else if aligned := alignToRuneBoundary(text, end); aligned > start {
			end = aligned
		}
		seg := text[start:end]
		out = append(out, &Chunk{
			Ordinal:    ordinal,
			Text:       seg,
			TokenCount: len(seg) / 4,
			CharStart:  start,
			CharEnd:    end,
		})
		ordinal++
		if end == len(text) {
			break
		}

		next := start + step
		if aligned := alignToRuneBoundary(text, next); aligned > start {
			next = aligned
		}
		start = next
	}
	return out
}

var _ Splitter = (*FixedWindow)(nil)
