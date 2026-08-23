package ui

import (
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestCleanHDUWithDQMatchesEXTVERThroughHeaderString(t *testing.T) {
	sci := fitsio.HDU{ExtName: "SCI", Header: fitsio.Header{Cards: map[string]string{"EXTVER": "2 / detector"}}, Data: fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{3}}}
	dq := fitsio.HDU{ExtName: "DQ", Header: fitsio.Header{Cards: map[string]string{"EXTVER": "2 / detector"}}, Data: fitsio.ImageData{Width: 1, Height: 1, Int32Pixels: []int32{0}}}
	file := &fitsio.File{HDUs: []fitsio.HDU{{Header: fitsio.Header{}}, sci, dq}}
	if _, err := cleanHDUWithDQ(sci, file); err != nil {
		t.Fatalf("cleanHDUWithDQ returned error for matching EXTVER: %v", err)
	}
}

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
