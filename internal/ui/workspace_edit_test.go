package ui

import (
	"image"
	"image/color"
	"testing"
)

func TestEditWorkingImageResetAndCommitUseDetachedCopies(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 1, 1))
	base.SetRGBA(0, 0, color.RGBA{R: 10, A: 255})
	es := &editWorkspaceState{base: base}

	if !es.resetWorkingToBase() {
		t.Fatal("resetWorkingToBase returned false")
	}
	es.working.SetRGBA(0, 0, color.RGBA{R: 20, A: 255})
	if got := es.base.RGBAAt(0, 0).R; got != 10 {
		t.Fatalf("base mutated through working image: got %d, want 10", got)
	}

	es.commitWorkingToBase()
	es.working.SetRGBA(0, 0, color.RGBA{R: 30, A: 255})
	if got := es.base.RGBAAt(0, 0).R; got != 20 {
		t.Fatalf("committed base mutated through working image: got %d, want 20", got)
	}

	if !es.resetWorkingToBase() {
		t.Fatal("second resetWorkingToBase returned false")
	}
	if got := es.working.RGBAAt(0, 0).R; got != 20 {
		t.Fatalf("reset working pixel = %d, want 20", got)
	}
}
