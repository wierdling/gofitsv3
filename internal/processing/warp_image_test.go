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
	if got[2] != 0 || got[3] != 0 || got[4] != 0 || got[5] != 0 || got[6] != 0 || got[7] != 0 || got[8] != 0 {
		t.Fatalf("expected truncated or edge samples to be zeroed, got %#v", got)
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
	if !math.IsNaN(float64(got[2])) || !math.IsNaN(float64(got[3])) || !math.IsNaN(float64(got[4])) || !math.IsNaN(float64(got[5])) || !math.IsNaN(float64(got[6])) || !math.IsNaN(float64(got[7])) || !math.IsNaN(float64(got[8])) {
		t.Fatalf("expected truncated or edge samples to be NaN, got %#v", got)
	}
}
