package ui

import (
	"math"
	"testing"
)

func TestClampMosaicZoomFiniteBounds(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{{0, 1.0 / 16}, {-1, 1.0 / 16}, {100, 16}, {1, 1}, {1.5, 1.5}} {
		if got := clampMosaicZoom(tc.in); got != tc.want {
			t.Fatalf("clampMosaicZoom(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if got := clampMosaicZoom(math.NaN()); got != 1 {
		t.Fatalf("NaN clamp = %v, want 1", got)
	}
}
