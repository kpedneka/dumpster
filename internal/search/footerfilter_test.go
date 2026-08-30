package search

import "testing"

func TestFooterFilter_NoFooter_EverythingFlushedEventually(t *testing.T) {
	f := &footerFilter{}
	var got string
	got += f.Write("The answer is ")
	got += f.Write("forty-two.")
	got += f.Flush()

	if got != "The answer is forty-two." {
		t.Errorf("got %q, want the full text with nothing held back", got)
	}
}

func TestFooterFilter_FooterInSingleDelta_HeldBack(t *testing.T) {
	f := &footerFilter{}
	var got string
	got += f.Write("The answer is forty-two.\nCITATIONS: 1,2")
	got += f.Flush()

	if got != "The answer is forty-two.\n" {
		t.Errorf("got %q, want the footer held back entirely", got)
	}
}

func TestFooterFilter_FooterSplitAcrossDeltas_NeverFlashesPartialMarker(t *testing.T) {
	f := &footerFilter{}
	var got string
	got += f.Write("Answer text. ")
	got += f.Write("CITAT") // partial marker: must be held, not flushed as visible text
	got += f.Write("IONS: 1,2,3")
	got += f.Flush()

	if got != "Answer text. " {
		t.Errorf("got %q, want only the pre-footer text, no partial marker leakage", got)
	}
}

func TestFooterFilter_CaseInsensitive(t *testing.T) {
	f := &footerFilter{}
	var got string
	got += f.Write("Answer.\nCiTaTiOnS: 1")
	got += f.Flush()

	if got != "Answer.\n" {
		t.Errorf("got %q, want the mixed-case footer held back", got)
	}
}

func TestFooterFilter_TextEndingInPartialMarkerPrefix_NotFollowedByRealMarker_EventuallyFlushed(t *testing.T) {
	// "cit" looks like the start of "citations:" but the stream ends
	// without it ever completing — Flush must release it as genuine
	// content, not silently drop it.
	f := &footerFilter{}
	var got string
	got += f.Write("this is not a cit")
	got += f.Write("y I've been to")
	got += f.Flush()

	if got != "this is not a city I've been to" {
		t.Errorf("got %q, want the false-positive prefix eventually flushed", got)
	}
}

func TestFooterFilter_TextAfterConfirmedMarker_KeepsBeingSwallowed(t *testing.T) {
	f := &footerFilter{}
	var got string
	got += f.Write("Answer.\nCITATIONS: 1")
	got += f.Write(",2,3") // arrives in a later delta, after the marker matched
	got += f.Flush()

	if got != "Answer.\n" {
		t.Errorf("got %q, want everything after the confirmed marker discarded", got)
	}
}

func TestFooterFilter_EmptyInput_NoPanic(t *testing.T) {
	f := &footerFilter{}
	if got := f.Write(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := f.Flush(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
