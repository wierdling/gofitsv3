package ui

import (
	"math"
	"testing"
)

// TestManualOffsetRoundTrip verifies extractManualOffset is the exact inverse of
// composeManualOffsetTransform for rotation+translation (the form Auto-Align and
// the Manual Offset fields use).
func TestManualOffsetRoundTrip(t *testing.T) {
	const w, h = 512, 384
	cases := []struct{ dx, dy, rot float64 }{
		{0, 0, 0},
		{12.5, -7.25, 0},
		{-101.76, 9.64, 0},
		{3, 4, 5},
		{-20.5, 33.0, -2.5},
		{0, 0, 1.25},
	}
	for _, c := range cases {
		tr := composeManualOffsetTransform(w, h, c.dx, c.dy, c.rot)
		gotDx, gotDy, gotRot := extractManualOffset(tr, w, h)
		if math.Abs(gotDx-c.dx) > 1e-6 || math.Abs(gotDy-c.dy) > 1e-6 || math.Abs(gotRot-c.rot) > 1e-6 {
			t.Errorf("round trip (dx=%g dy=%g rot=%g) -> (%g %g %g)", c.dx, c.dy, c.rot, gotDx, gotDy, gotRot)
		}
		// Recomposing from the extracted values must reproduce the transform.
		tr2 := composeManualOffsetTransform(w, h, gotDx, gotDy, gotRot)
		for _, p := range [][2]float64{{tr.A, tr2.A}, {tr.B, tr2.B}, {tr.C, tr2.C}, {tr.D, tr2.D}, {tr.E, tr2.E}, {tr.F, tr2.F}} {
			if math.Abs(p[0]-p[1]) > 1e-6 {
				t.Errorf("recomposed transform mismatch for case %+v: %v vs %v", c, tr, tr2)
				break
			}
		}
	}
}
