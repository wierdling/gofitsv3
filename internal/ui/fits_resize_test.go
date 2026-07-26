package ui

import "testing"

func TestDeduplicatePathsPreservesOrder(t *testing.T) {
	got := deduplicatePaths([]string{"first.fits", "second.fit", "first.fits", "third.fts", "second.fit"})
	want := []string{"first.fits", "second.fit", "third.fts"}
	if len(got) != len(want) {
		t.Fatalf("deduplicatePaths() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("deduplicatePaths() = %v, want %v", got, want)
		}
	}
}

func TestResizeFactorsAndDiscardNoteAllowIncompleteEdges(t *testing.T) {
	got := resizeFactors(5, 3)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("resizeFactors(5, 3) = %v, want [2]", got)
	}
	if got := resizeDiscardNote(5, 3, 2); got != "Discarded edge pixels: rightmost 1 column(s) and bottom 1 row(s)." {
		t.Fatalf("discard note = %q", got)
	}
}
