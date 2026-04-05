package ui

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

func almostEqual(a, b, tol float64) bool {
	if a > b {
		return a-b < tol
	}
	return b-a < tol
}

func TestApplyStretchLinear(t *testing.T) {
	img := &models.LoadedImage{
		HDU:        fitsio.HDU{Data: fitsio.ImageData{Width: 3, Height: 1, Pixels: []float32{0, 0.5, 1}}},
		Mode:       stretch.Linear,
		Black:      0,
		White:      1,
		Background: 0,
		Peak:       1,
		ScaledPeak: 1,
		ShowClip:   true,
	}
	out, mask := processing.ApplyStretchParallel(img)
	if len(mask) != 3 {
		t.Fatalf("expected mask len 3")
	}
	want := []float64{0, 0.5, 1}
	for i, v := range want {
		if !almostEqual(float64(out.Pixels[i]), v, 1e-6) {
			t.Fatalf("linear pixel %d got %f want %f", i, out.Pixels[i], v)
		}
	}
	if mask[0] != 0 || mask[1] != 0 || mask[2] != 0 {
		t.Fatalf("unexpected masks: %v", mask)
	}
}

func TestApplyStretchLog(t *testing.T) {
	img := &models.LoadedImage{
		HDU:        fitsio.HDU{Data: fitsio.ImageData{Width: 3, Height: 1, Pixels: []float32{0, 0.5, 1}}},
		Mode:       stretch.Log,
		Black:      0,
		White:      1,
		Background: 0,
		Peak:       1,
		ScaledPeak: 10,
	}
	out, _ := processing.ApplyStretchParallel(img)
	want0 := 0.0
	want1 := math.Log1p(5) / math.Log1p(10)
	want2 := 1.0
	got := out.Pixels
	if !almostEqual(float64(got[0]), want0, 1e-6) || !almostEqual(float64(got[1]), want1, 1e-6) || !almostEqual(float64(got[2]), want2, 1e-6) {
		t.Fatalf("log stretch got %v want [%f %f %f]", got, want0, want1, want2)
	}
}

func TestApplyStretchMasking(t *testing.T) {
	img := &models.LoadedImage{
		HDU:        fitsio.HDU{Data: fitsio.ImageData{Width: 3, Height: 1, Pixels: []float32{0.1, float32(math.NaN()), 0.9}}},
		Mode:       stretch.Linear,
		Black:      0.2,
		White:      0.8,
		Background: 0,
		Peak:       1,
		ScaledPeak: 1,
		ShowClip:   true,
	}
	_, mask := processing.ApplyStretchParallel(img)
	if mask[0] != 1 {
		t.Fatalf("expected mask 1 for low pixel, got %d", mask[0])
	}
	if mask[1] != 3 {
		t.Fatalf("expected mask 3 for NaN, got %d", mask[1])
	}
	if mask[2] != 2 {
		t.Fatalf("expected mask 2 for high pixel, got %d", mask[2])
	}
}
