package mosaic

import "testing"

// newLockBaseline returns a ReferenceOnly baseline of the given dimensions.
func newLockBaseline(w, h int) Input {
	b := makeInput("baseline.fits", w, h, filledPixels(w, h, 1), headerWithCRPIX(10, 10))
	b.ReferenceOnly = true
	return b
}

// A locked build pins the output canvas to the baseline's exact grid, even when
// the data footprint is smaller.
func TestBuildLockToReferenceFrameMatchesBaselineDims(t *testing.T) {
	baseline := newLockBaseline(6, 6)
	// 2×2 data footprint that would otherwise yield a 2×2 canvas.
	data := makeInput("chan_flc.fits", 2, 2, filledPixels(2, 2, 3), headerWithCRPIX(10, 10))

	result, err := Build([]Input{baseline, data}, Options{LockToReferenceFrame: true})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if result.Width != 6 || result.Height != 6 {
		t.Fatalf("locked size = %dx%d, want 6x6 (baseline dims)", result.Width, result.Height)
	}
	if result.OriginX != 0 || result.OriginY != 0 {
		t.Fatalf("locked origin = (%v,%v), want (0,0)", result.OriginX, result.OriginY)
	}
	if result.Scale != 1 {
		t.Fatalf("locked scale = %v, want 1", result.Scale)
	}
}

// Two locked builds sharing the same baseline produce identical output
// dimensions regardless of their differing data footprints — the property that
// makes separately-drizzled channels compositable.
func TestBuildLockIsFootprintIndependent(t *testing.T) {
	dataA := makeInput("a_flc.fits", 2, 2, filledPixels(2, 2, 1), headerWithCRPIX(10, 10))
	dataB := makeInput("b_flc.fits", 3, 3, filledPixels(3, 3, 1), headerWithCRPIX(8, 6))

	ra, err := Build([]Input{newLockBaseline(5, 7), dataA}, Options{LockToReferenceFrame: true})
	if err != nil {
		t.Fatalf("build A: %v", err)
	}
	rb, err := Build([]Input{newLockBaseline(5, 7), dataB}, Options{LockToReferenceFrame: true})
	if err != nil {
		t.Fatalf("build B: %v", err)
	}
	if ra.Width != rb.Width || ra.Height != rb.Height {
		t.Fatalf("locked dims differ: A=%dx%d B=%dx%d", ra.Width, ra.Height, rb.Width, rb.Height)
	}
	if ra.Width != 5 || ra.Height != 7 {
		t.Fatalf("locked dims = %dx%d, want 5x7 (baseline dims)", ra.Width, ra.Height)
	}
}
