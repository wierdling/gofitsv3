package processing

import (
	"math"
	"testing"
)

func TestWarpImageToSizeHandlesShorterPixelSlice(t *testing.T) {
	pixels := []float32{
		1, 2, 3,
		4, 5, 6,
	}

	got := WarpImageToSize(pixels, 3, 3, 3, 3, IdentityTransform())
	if len(got) != 9 {
		t.Fatalf("len = %d, want 9", len(got))
	}
	if got[0] != 1 || got[1] != 2 {
		t.Fatalf("unexpected preserved pixels: %#v", got)
	}
	if got[2] != 3 || got[3] != 4 || got[4] != 5 || got[5] != 6 || got[6] != 0 || got[7] != 0 || got[8] != 0 {
		t.Fatalf("expected right-edge samples preserved and truncated row zeroed, got %#v", got)
	}
}

func TestWarpImageToSizeWithMaskHandlesShorterPixelSlice(t *testing.T) {
	pixels := []float32{
		1, 2, 3,
		4, 5, 6,
	}

	got, valid := WarpImageToSizeWithMask(pixels, 3, 3, 3, 3, IdentityTransform())
	if len(got) != 9 || len(valid) != 9 {
		t.Fatalf("lens = (%d,%d), want (9,9)", len(got), len(valid))
	}
	if got[0] != 1 || got[1] != 2 || !valid[0] || !valid[1] {
		t.Fatalf("unexpected valid samples: pixels=%#v valid=%#v", got, valid)
	}
	if !valid[2] || !valid[3] || !valid[4] || !valid[5] || got[2] != 3 || got[5] != 6 || !math.IsNaN(float64(got[6])) || !math.IsNaN(float64(got[7])) || !math.IsNaN(float64(got[8])) {
		t.Fatalf("expected right-edge samples valid and truncated row invalid, got pixels=%#v valid=%#v", got, valid)
	}
}

func TestWarpRejectsInfiniteContributors(t *testing.T) {
	pixels := []float32{1, float32(math.Inf(1)), 3, 4}
	got := WarpImageToSize(pixels, 2, 2, 2, 2, IdentityTransform())
	if !math.IsNaN(float64(got[0])) {
		t.Fatalf("unmasked warp = %v, want NaN", got[0])
	}
	masked, valid := WarpImageToSizeWithMask(pixels, 2, 2, 2, 2, IdentityTransform())
	if !math.IsNaN(float64(masked[0])) || valid[0] {
		t.Fatalf("masked warp = (%v,%v), want (NaN,false)", masked[0], valid[0])
	}
}

func TestWarpRejectsInvalidOutputDimensions(t *testing.T) {
	if got := WarpImageToSize(nil, 1, 1, -1, 1, IdentityTransform()); len(got) != 0 {
		t.Fatal("expected empty invalid-dimension output")
	}
	if got, valid := WarpImageToSizeWithMask(nil, 1, 1, int(^uint(0)>>1), 2, IdentityTransform()); len(got) != 0 || len(valid) != 0 {
		t.Fatal("expected empty overflow output")
	}
}
