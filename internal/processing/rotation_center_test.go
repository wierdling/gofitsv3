package processing

import (
	"math"
	"testing"
)

func TestRotationAroundKeepsCenterFixed(t *testing.T) {
	cx, cy := 200.0, 150.0
	tfm := RotationAround(cx, cy, 3*math.Pi/180)

	x, y := ApplyAffineTransform(tfm, cx, cy)
	if math.Abs(x-cx) > 1e-9 || math.Abs(y-cy) > 1e-9 {
		t.Fatalf("center moved to (%.12f, %.12f), want (%.12f, %.12f)", x, y, cx, cy)
	}

	x, y = ApplyAffineTransform(tfm, cx+100, cy)
	if math.Abs(math.Hypot(x-cx, y-cy)-100) > 1e-9 {
		t.Fatalf("radius changed after rotation: got %.12f, want 100", math.Hypot(x-cx, y-cy))
	}
}
