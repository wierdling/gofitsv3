package ui

import "testing"

func TestResizeFactorsAndDiscardNoteAllowIncompleteEdges(t *testing.T) {
	got := resizeFactors(5, 3)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("resizeFactors(5, 3) = %v, want [2]", got)
	}
	if got := resizeDiscardNote(5, 3, 2); got != "Discarded edge pixels: rightmost 1 column(s) and bottom 1 row(s)." {
		t.Fatalf("discard note = %q", got)
	}
}
