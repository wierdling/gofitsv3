package badpix

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestMaskFromDQ(t *testing.T) {
	sci := fitsio.HDU{Data: fitsio.ImageData{Width: 3, Height: 2, Pixels: []float32{0, 1, 2, 3, 4, 5}}}
	dq := fitsio.HDU{Data: fitsio.ImageData{Width: 3, Height: 2, Pixels: []float32{0, 1, 2, 0, 4, 0}}}

	mask, err := MaskFromDQ(sci, dq, 0)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	expectTrue := []int{1, 2, 4}
	for i, v := range mask {
		want := contains(expectTrue, i)
		if v != want {
			t.Fatalf("mask[%d]=%v want %v", i, v, want)
		}
	}

	// badBits filtering
	dq.Data.Pixels = []float32{2, 0, 4, 0, 0, 0}
	mask, err = MaskFromDQ(sci, dq, 0x1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	for i, v := range mask {
		if v {
			t.Fatalf("expected mask[%d]=false", i)
		}
	}

	// dimension mismatch
	dq.Data.Width = 2
	if _, err := MaskFromDQ(sci, dq, 0); err == nil {
		t.Fatalf("expected dimension mismatch error")
	}
}

func TestInterpolateBicubic(t *testing.T) {
	w, h := 5, 5
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pixels[y*w+x] = float32(y*w + x)
		}
	}
	pixels[2*w+2] = 100
	mask := make([]bool, len(pixels))
	mask[2*w+2] = true // center masked
	img := fitsio.ImageData{Width: w, Height: h, Pixels: pixels}

	out := RepairMaskedPixels(img, mask)
	got := out.Pixels[2*w+2]
	if math.IsNaN(float64(got)) || math.IsInf(float64(got), 0) {
		t.Fatalf("interpolated value is invalid: %v", got)
	}
	if got == img.Pixels[2*w+2] {
		t.Fatalf("expected masked pixel to change, stayed %v", got)
	}
	for _, idx := range []int{2*w + 1, 2*w + 3, (2-1)*w + 2, (2+1)*w + 2} {
		if out.Pixels[idx] != img.Pixels[idx] {
			t.Fatalf("neighbor pixel %d changed", idx)
		}
	}
}

func TestInterpolateEdge(t *testing.T) {
	img := fitsio.ImageData{Width: 3, Height: 3, Pixels: []float32{100, 2, 3, 4, 5, 6, 7, 8, 9}}
	mask := make([]bool, len(img.Pixels))
	mask[0] = true // top-left corner
	out := RepairMaskedPixels(img, mask)
	if math.IsNaN(float64(out.Pixels[0])) || out.Pixels[0] == img.Pixels[0] {
		t.Fatalf("edge interpolation failed: %v", out.Pixels[0])
	}
}

func TestRepairMaskedPixelsReturnsOriginalForMaskLengthMismatchAndEmptyMask(t *testing.T) {
	img := fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{1, 2, 3, 4}}

	out := RepairMaskedPixels(img, []bool{true})
	for i, want := range img.Pixels {
		if out.Pixels[i] != want {
			t.Fatalf("mismatch fallback pixel[%d] = %v, want %v", i, out.Pixels[i], want)
		}
	}

	mask := make([]bool, len(img.Pixels))
	out = RepairMaskedPixels(img, mask)
	for i, want := range img.Pixels {
		if out.Pixels[i] != want {
			t.Fatalf("empty mask pixel[%d] = %v, want %v", i, out.Pixels[i], want)
		}
	}
}

func TestBicubicAtFallsBackToOriginalWhenMaskMalformedOrNoSamples(t *testing.T) {
	img := fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{42}}

	if got := bicubicAt(img, nil, 0, 0); got != 42 {
		t.Fatalf("bicubicAt malformed mask = %v, want 42", got)
	}

	mask := []bool{true}
	if got := bicubicAt(img, mask, 0, 0); got != 42 {
		t.Fatalf("bicubicAt no samples fallback = %v, want 42", got)
	}
}

func TestWeightedMedianFillAndMirrorHelpers(t *testing.T) {
	img := fitsio.ImageData{
		Width:  3,
		Height: 3,
		Pixels: []float32{
			10, 20, 30,
			40, 0, 60,
			70, 80, 90,
		},
	}
	mask := []bool{
		false, false, false,
		false, true, false,
		false, false, false,
	}

	got, ok := weightedMedianFill(img, mask, 1, 1, 1)
	if !ok {
		t.Fatal("weightedMedianFill should find unmasked neighbors")
	}
	if got != 60 {
		t.Fatalf("weightedMedianFill = %v, want 60", got)
	}

	allMasked := make([]bool, len(mask))
	for i := range allMasked {
		allMasked[i] = true
	}
	if _, ok := weightedMedianFill(img, allMasked, 1, 1, 1); ok {
		t.Fatal("weightedMedianFill should fail with no valid neighbors")
	}

	tests := []struct {
		value int
		max   int
		want  int
	}{
		{value: -1, max: 3, want: 0},
		{value: -2, max: 3, want: 1},
		{value: 3, max: 3, want: 2},
		{value: 4, max: 3, want: 1},
		{value: 0, max: 0, want: 0},
	}
	for _, tt := range tests {
		if got := mirror(tt.value, tt.max); got != tt.want {
			t.Fatalf("mirror(%d,%d) = %d, want %d", tt.value, tt.max, got, tt.want)
		}
	}
}

func TestMaskFromDQWithBitFilterSelectsMatchingBits(t *testing.T) {
	sci := fitsio.HDU{Data: fitsio.ImageData{Width: 4, Height: 1, Pixels: []float32{0, 1, 2, 3}}}
	dq := fitsio.HDU{Data: fitsio.ImageData{Width: 4, Height: 1, Pixels: []float32{1, 2, 3, 4}}}

	mask, err := MaskFromDQ(sci, dq, 0x2)
	if err != nil {
		t.Fatalf("MaskFromDQ error = %v", err)
	}
	want := []bool{false, true, true, false}
	for i, v := range want {
		if mask[i] != v {
			t.Fatalf("mask[%d] = %v, want %v", i, mask[i], v)
		}
	}
}

func contains(arr []int, v int) bool {
	for _, x := range arr {
		if x == v {
			return true
		}
	}
	return false
}
