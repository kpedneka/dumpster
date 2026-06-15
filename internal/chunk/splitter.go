package chunk

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
	for start := 0; start < len(text); start += step {
		end := start + f.size
		if end > len(text) {
			end = len(text)
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
	}
	return out
}

var _ Splitter = (*FixedWindow)(nil)
