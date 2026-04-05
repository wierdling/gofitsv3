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
	mask := make([]bool, len(pixels))
	mask[2*w+2] = true // center masked
	img := fitsio.ImageData{Width: w, Height: h, Pixels: pixels}

	out := InterpolateBicubic(img, mask)
	got := out.Pixels[2*w+2]
	if math.IsNaN(float64(got)) || math.IsInf(float64(got), 0) {
		t.Fatalf("interpolated value is invalid: %v", got)
	}
	if got == img.Pixels[2*w+2] {
		t.Fatalf("expected masked pixel to change, stayed %v", got)
	}
	// ensure neighbors unchanged
	for _, idx := range []int{2*w + 1, 2*w + 3, (2-1)*w + 2, (2+1)*w + 2} {
		if out.Pixels[idx] != img.Pixels[idx] {
			t.Fatalf("neighbor pixel %d changed", idx)
		}
	}
}

func TestInterpolateEdge(t *testing.T) {
	img := fitsio.ImageData{Width: 3, Height: 3, Pixels: []float32{1, 2, 3, 4, 5, 6, 7, 8, 9}}
	mask := make([]bool, len(img.Pixels))
	mask[0] = true // top-left corner
	out := InterpolateBicubic(img, mask)
	if math.IsNaN(float64(out.Pixels[0])) || out.Pixels[0] == img.Pixels[0] {
		t.Fatalf("edge interpolation failed: %v", out.Pixels[0])
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
