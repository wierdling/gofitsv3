package mosaic

import (
	"math"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
)

// destripeStripe is a deterministic pseudo-random per-row 1/f offset. (y*37)%7
// cycles through every residue of 7, so its mean over whole cycles is exactly
// zero and over any wide window is near zero, mimicking zero-mean banding.
func destripeStripe(y int) float64 {
	return 0.5 * float64((y*37)%7-3)
}

// destripeTestFrame builds a frame with a smooth vertical gradient (standing
// in for nebulosity), per-row 1/f banding, and a few bright star spikes.
func destripeTestFrame(width, height int, withStripes bool) []float32 {
	pixels := make([]float32, width*height)
	for y := 0; y < height; y++ {
		base := 1000 + 0.02*float64(y)
		if withStripes {
			base += destripeStripe(y)
		}
		row := y * width
		for x := 0; x < width; x++ {
			pixels[row+x] = float32(base)
		}
	}
	for _, idx := range []int{width*40 + 100, width*90 + 300, width*150 + 450} {
		if idx >= 0 && idx < len(pixels) {
			pixels[idx] = 50000
		}
	}
	return pixels
}

func destripePlannedInput(width, height int, referenceOnly bool, header fitsio.Header) plannedInput {
	return plannedInput{input: Input{
		ReferenceOnly: referenceOnly,
		PrimaryHeader: header,
		HDU:           fitsio.HDU{Data: fitsio.ImageData{Width: width, Height: height}},
	}}
}

func TestApplyRowDestripeRemovesBandingAndKeepsGradient(t *testing.T) {
	width, height := 512, 256
	pixels := destripeTestFrame(width, height, true)

	if err := applyRowDestripe(destripePlannedInput(width, height, false, nircamPrimaryHeader()), pixels, SkysubOptions{}, nil); err != nil {
		t.Fatalf("applyRowDestripe error = %v", err)
	}

	// Interior rows (full smoothing window available) must sit on the clean
	// gradient: banding gone, gradient intact. Sample all four amp strips.
	for _, x := range []int{10, 200, 300, 500} {
		for _, y := range []int{80, 128, 180} {
			got := float64(pixels[y*width+x])
			want := 1000 + 0.02*float64(y)
			if math.Abs(got-want) > 0.15 {
				t.Fatalf("pixel(%d,%d) = %v, want ~%v (stripe %v not removed?)",
					x, y, got, want, destripeStripe(y))
			}
		}
	}
}

func TestApplyRowDestripeIsNoOpOnSmoothGradient(t *testing.T) {
	width, height := 512, 256
	pixels := destripeTestFrame(width, height, false)

	if err := applyRowDestripe(destripePlannedInput(width, height, false, nircamPrimaryHeader()), pixels, SkysubOptions{}, nil); err != nil {
		t.Fatalf("applyRowDestripe error = %v", err)
	}

	// With no banding, rowMed equals the linear trend everywhere (the shrinking
	// symmetric window is exactly linear-preserving), so the frame — i.e. the
	// "nebula" — must come through essentially untouched.
	for _, x := range []int{10, 200, 300, 500} {
		for y := 0; y < height; y += 17 {
			got := float64(pixels[y*width+x])
			want := 1000 + 0.02*float64(y)
			if math.Abs(got-want) > 1e-3 {
				t.Fatalf("pixel(%d,%d) = %v, want %v (gradient distorted)", x, y, got, want)
			}
		}
	}
}

func TestApplyRowDestripeSkipsNonNircamAndReferenceOnly(t *testing.T) {
	width, height := 512, 256
	cases := []struct {
		name string
		p    plannedInput
	}{
		{"non-NIRCam", destripePlannedInput(width, height, false,
			fitsio.Header{Cards: map[string]string{"INSTRUME": "'WFC3'"}})},
		{"reference-only", destripePlannedInput(width, height, true, nircamPrimaryHeader())},
	}
	for _, tc := range cases {
		pixels := destripeTestFrame(width, height, true)
		original := append([]float32(nil), pixels...)
		if err := applyRowDestripe(tc.p, pixels, SkysubOptions{}, nil); err != nil {
			t.Fatalf("%s: applyRowDestripe error = %v", tc.name, err)
		}
		for i := range pixels {
			if pixels[i] != original[i] {
				t.Fatalf("%s: pixel[%d] changed: got %v, want %v", tc.name, i, pixels[i], original[i])
			}
		}
	}
}

func TestApplyRowDestripeHandlesNaNRows(t *testing.T) {
	width, height := 512, 256
	pixels := destripeTestFrame(width, height, true)
	// Blank out one full row (all-NaN, as DQ cleaning can produce) and confirm
	// nothing panics and other rows are still corrected.
	nan := float32(math.NaN())
	for x := 0; x < width; x++ {
		pixels[100*width+x] = nan
	}

	if err := applyRowDestripe(destripePlannedInput(width, height, false, nircamPrimaryHeader()), pixels, SkysubOptions{}, nil); err != nil {
		t.Fatalf("applyRowDestripe error = %v", err)
	}

	if !math.IsNaN(float64(pixels[100*width+5])) {
		t.Fatal("NaN row should remain NaN")
	}
	got := float64(pixels[128*width+200])
	want := 1000 + 0.02*128
	if math.Abs(got-want) > 0.15 {
		t.Fatalf("row 128 not corrected in presence of NaN row: got %v, want ~%v", got, want)
	}
}

func TestApplyRowDestripeExternalMaskProtectsBroadSourceRow(t *testing.T) {
	width, height := 512, 256
	pixels := destripeTestFrame(width, height, false)
	sourceRow := 128
	for x := 0; x < width/4; x++ {
		pixels[sourceRow*width+x] += 500
	}
	userMask := make([]bool, width*height)
	for x := 0; x < width/4; x++ {
		userMask[sourceRow*width+x] = true
	}

	if err := applyRowDestripe(destripePlannedInput(width, height, false, nircamPrimaryHeader()), pixels, SkysubOptions{}, userMask); err != nil {
		t.Fatalf("applyRowDestripe error = %v", err)
	}

	got := float64(pixels[sourceRow*width+10])
	want := 1000 + 0.02*float64(sourceRow) + 500
	if math.Abs(got-want) > 1e-3 {
		t.Fatalf("masked broad source pixel = %v, want %v", got, want)
	}
}

func TestPrepareFramePixelsRowDestripeMaskMismatchErrorsBeforeMutation(t *testing.T) {
	width, height := 512, 256
	pixels := destripeTestFrame(width, height, true)
	original := append([]float32(nil), pixels...)
	maskPath := filepath.Join(t.TempDir(), "bad_mask.fits")
	if err := fitsio.WriteFloat32Image(maskPath, fitsio.Header{}, fitsio.ImageData{
		Width:  width + 1,
		Height: height,
		Pixels: filledPixels(width+1, height, 0),
	}); err != nil {
		t.Fatalf("WriteFloat32Image error = %v", err)
	}
	p := destripePlannedInput(width, height, false, nircamPrimaryHeader())
	p.input.HDU.Data.Pixels = pixels

	_, _, _, err := prepareFramePixels(p, Options{Skysub: SkysubOptions{
		RowDestripe:         true,
		AmpPedestal:         true,
		RowDestripeMaskPath: maskPath,
	}}, 0, skyPlane{})
	if err == nil {
		t.Fatal("prepareFramePixels error = nil, want mask dimension mismatch")
	}
	for i := range pixels {
		if pixels[i] != original[i] {
			t.Fatalf("pixel[%d] changed before mask error: got %v, want %v", i, pixels[i], original[i])
		}
	}
}

func TestLoadRowDestripeUserMaskReadsNonzeroPixels(t *testing.T) {
	width, height := 4, 3
	maskPath := filepath.Join(t.TempDir(), "mask.fits")
	if err := fitsio.WriteFloat32Image(maskPath, fitsio.Header{}, fitsio.ImageData{
		Width:  width,
		Height: height,
		Pixels: []float32{0, 1, 0, -2, 0, float32(math.NaN()), 3, 0, 0, 0, 0, 0},
	}); err != nil {
		t.Fatalf("WriteFloat32Image error = %v", err)
	}
	p := destripePlannedInput(width, height, false, nircamPrimaryHeader())
	mask, err := loadRowDestripeUserMask(p.input, SkysubOptions{RowDestripeMaskPath: maskPath})
	if err != nil {
		t.Fatalf("loadRowDestripeUserMask error = %v", err)
	}
	wantTrue := map[int]bool{1: true, 3: true, 6: true}
	for i, got := range mask {
		if got != wantTrue[i] {
			t.Fatalf("mask[%d] = %v, want %v", i, got, wantTrue[i])
		}
	}
}

func TestSmoothValidSeriesPreservesLinearTrend(t *testing.T) {
	vals := make([]float64, 200)
	for i := range vals {
		vals[i] = 3 + 0.5*float64(i)
	}
	out := smoothValidSeries(vals, destripeTrendWindow)
	for i := range out {
		if math.Abs(out[i]-vals[i]) > 1e-9 {
			t.Fatalf("smoothed[%d] = %v, want %v (linear input must be a fixed point)", i, out[i], vals[i])
		}
	}
}

func TestSmoothValidSeriesSkipsNaNs(t *testing.T) {
	vals := []float64{1, math.NaN(), 3, 5, math.NaN(), 7}
	out := smoothValidSeries(vals, 3)
	// Index 1 has window {1, NaN, 3}: mean of valid = 2.
	if math.Abs(out[1]-2) > 1e-9 {
		t.Fatalf("smoothed[1] = %v, want 2", out[1])
	}
	// Ends use a zero-width window: value passes through (including NaN).
	if out[0] != 1 {
		t.Fatalf("smoothed[0] = %v, want 1", out[0])
	}
}
