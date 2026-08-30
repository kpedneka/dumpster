package search

import "strings"

// citationsMarker matches the lowercase form checked by parseResponse.
const citationsMarker = "citations:"

// footerFilter consumes a token stream incrementally and holds back exactly
// the trailing "CITATIONS: ..." footer that parseResponse later strips from
// the complete response — so a live-streamed answer never flashes that
// footer on screen before the authoritative final summary replaces it.
//
// Known limitation: parseResponse finds the *last* occurrence of
// "citations:" in the complete text, so that a source chunk merely quoting
// the word "citations:" doesn't cause a false split. A live filter can't
// know whether a given occurrence is the last one without seeing the rest
// of the stream, so it treats the *first* occurrence as the footer start.
// In the rare case a chunk's quoted text contains "citations:" earlier in
// the answer, the live preview stops early; the final `done` event still
// carries the correct, fully-parsed summary, so the on-screen result is
// unaffected — only the live-typing effect for that tail is lost.
type footerFilter struct {
	buf     strings.Builder
	matched bool // true once the footer marker has been confirmed
}

// Write appends delta to the filter's internal state and returns the
// portion of the accumulated text that is now safe to display — i.e.
// guaranteed not to be part of the citations footer.
func (f *footerFilter) Write(delta string) string {
	f.buf.WriteString(delta)
	combined := f.buf.String()
	lower := strings.ToLower(combined)

	if idx := strings.Index(lower, citationsMarker); idx >= 0 {
		safe := combined[:idx]
		f.buf.Reset()
		f.buf.WriteString(combined[idx:])
		f.matched = true
		return safe
	}
	if f.matched {
		// Already inside a confirmed footer: keep swallowing everything
		// appended after the marker (the citation number list).
		f.buf.Reset()
		return ""
	}

	// No confirmed marker yet. Hold back the longest suffix of combined
	// that could still grow into "citations:" as more text arrives, so a
	// marker split across two deltas ("citat" + "ions:") is never flushed
	// as visible text prematurely.
	holdFrom := len(combined)
	maxHold := min(len(citationsMarker)-1, len(combined))
	for l := maxHold; l > 0; l-- {
		if strings.HasPrefix(citationsMarker, lower[len(lower)-l:]) {
			holdFrom = len(combined) - l
			break
		}
	}
	safe := combined[:holdFrom]
	f.buf.Reset()
	f.buf.WriteString(combined[holdFrom:])
	return safe
}

// Flush returns any text still held back once the stream has ended. If the
// held-back text turned out not to be a footer (matched is false), it's
// genuine trailing content that was never confirmed safe mid-stream and
// must still be shown. If the footer was confirmed, its content is
// discarded — the caller gets the authoritative summary from parseResponse
// on the complete text instead.
func (f *footerFilter) Flush() string {
	if f.matched {
		f.buf.Reset()
		return ""
	}
	s := f.buf.String()
	f.buf.Reset()
	return s
}
