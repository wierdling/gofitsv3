package processing

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

func TestAutoScaleLikeFitsLiberatorSetsFieldsAndRepairsPeak(t *testing.T) {
	img := &models.LoadedImage{
		HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{5, 5, 5, 5}}},
		Black: 3,
		White: 3,
	}

	AutoScaleLikeFitsLiberator(img)

	if img.Background != img.Black {
		t.Fatalf("Background = %v, want %v", img.Background, img.Black)
	}
	if img.Peak <= img.Background {
		t.Fatalf("Peak = %v, want > Background %v", img.Peak, img.Background)
	}
	if img.ScaledPeak != fitsLiberatorAutoScaledPeak {
		t.Fatalf("ScaledPeak = %v, want %v", img.ScaledPeak, fitsLiberatorAutoScaledPeak)
	}
}

func TestAutoScaleLikeFitsLiberatorNilIsNoOp(t *testing.T) {
	AutoScaleLikeFitsLiberator(nil)
}

func TestAutoLevelsIgnoresInvalidAndHandlesFlatInput(t *testing.T) {
	min, max := AutoLevels([]float32{float32(math.NaN()), 5, float32(math.Inf(1)), 2})
	if min != 2 || max != 5 {
		t.Fatalf("AutoLevels mixed = (%v,%v), want (2,5)", min, max)
	}

	min, max = AutoLevels([]float32{7, 7, 7})
	if min != 7 || max != 8 {
		t.Fatalf("AutoLevels flat = (%v,%v), want (7,8)", min, max)
	}

	min, max = AutoLevels([]float32{float32(math.NaN())})
	if min != 0 || max != 1 {
		t.Fatalf("AutoLevels invalid = (%v,%v), want (0,1)", min, max)
	}
}

func TestToGrayRGBAUsesMaskColorsAndClamp(t *testing.T) {
	img := ToGrayRGBA(
		fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{-1, 0.5, 2, 0.25}},
		[]byte{0, 1, 2, 3},
	)

	want := [][4]byte{
		{0, 0, 0, 255},
		{0, 0, 255, 255},
		{0, 255, 0, 255},
		{255, 0, 0, 255},
	}
	for i, px := range want {
		idx := i * 4
		got := [4]byte{img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2], img.Pix[idx+3]}
		if got != px {
			t.Fatalf("pixel %d = %v, want %v", i, got, px)
		}
	}
}

func TestFlipHelpersReverseRowsInPlace(t *testing.T) {
	data := fitsio.ImageData{Width: 2, Height: 3, Pixels: []float32{1, 2, 3, 4, 5, 6}}
	flippedData := FlipImageData(data)
	wantData := []float32{5, 6, 3, 4, 1, 2}
	for i, want := range wantData {
		if flippedData.Pixels[i] != want {
			t.Fatalf("FlipImageData[%d] = %v, want %v", i, flippedData.Pixels[i], want)
		}
	}

	mask := []byte{1, 2, 3, 4, 5, 6}
	flippedMask := FlipMask(mask, 2, 3)
	wantMask := []byte{5, 6, 3, 4, 1, 2}
	for i, want := range wantMask {
		if flippedMask[i] != want {
			t.Fatalf("FlipMask[%d] = %v, want %v", i, flippedMask[i], want)
		}
	}
	if FlipMask(nil, 2, 3) != nil {
		t.Fatal("FlipMask(nil) should return nil")
	}

	rgba := []byte{
		1, 2, 3, 4,
		5, 6, 7, 8,
		9, 10, 11, 12,
		13, 14, 15, 16,
	}
	flippedRGBA := FlipRGBA(rgba, 2, 2)
	wantRGBA := []byte{
		9, 10, 11, 12,
		13, 14, 15, 16,
		1, 2, 3, 4,
		5, 6, 7, 8,
	}
	for i, want := range wantRGBA {
		if flippedRGBA[i] != want {
			t.Fatalf("FlipRGBA[%d] = %v, want %v", i, flippedRGBA[i], want)
		}
	}
}

func TestResizeChannelInterpolatesAndPropagatesNaNSource(t *testing.T) {
	src := []float32{
		0, 10,
		20, 30,
	}
	got := ResizeChannel(src, 2, 2, 4, 4)
	if len(got) != 16 {
		t.Fatalf("len(ResizeChannel) = %d, want 16", len(got))
	}
	if math.Abs(float64(got[5]-15)) > 1e-6 {
		t.Fatalf("interpolated center-ish value = %v, want 15", got[5])
	}

	withNaN := []float32{
		float32(math.NaN()), 10,
		20, 30,
	}
	got = ResizeChannel(withNaN, 2, 2, 4, 4)
	if !math.IsNaN(float64(got[0])) {
		t.Fatalf("ResizeChannel should preserve NaN fallback, got %v", got[0])
	}
}

func TestGetPixelScalePrefersCDThenCDELTThenPIXSCALE(t *testing.T) {
	scale := GetPixelScale([]string{
		"CD1_1   = 0.0001",
		"CD2_1   = 0.0002",
		"CDELT1  = 0.5",
		"PIXSCALE= 9.9",
	})
	want := math.Sqrt(0.0001*0.0001+0.0002*0.0002) * 3600
	if math.Abs(scale-want) > 1e-9 {
		t.Fatalf("GetPixelScale CD = %v, want %v", scale, want)
	}

	scale = GetPixelScale([]string{"CDELT1  = -0.25"})
	if scale != 900 {
		t.Fatalf("GetPixelScale CDELT1 = %v, want 900", scale)
	}

	scale = GetPixelScale([]string{"PIXSCALE= 0.4"})
	if scale != 0.4 {
		t.Fatalf("GetPixelScale PIXSCALE = %v, want 0.4", scale)
	}

	scale = GetPixelScale([]string{"NOTHING = 1"})
	if scale != 1.0 {
		t.Fatalf("GetPixelScale default = %v, want 1", scale)
	}
}

func TestFiniteSamplePercentileAndRobustSigmaHelpers(t *testing.T) {
	values := finiteSample([]float32{
		0,
		1,
		float32(math.NaN()),
		2,
		3,
		4,
		float32(math.Inf(1)),
		5,
	}, 3)
	if len(values) != 2 || values[0] != 0 || values[1] != 2 {
		t.Fatalf("finiteSample = %v, want [0 2]", values)
	}

	sorted := []float64{10, 20, 30, 40}
	if got := percentileSorted(sorted, -5); got != 10 {
		t.Fatalf("percentileSorted low = %v, want 10", got)
	}
	if got := percentileSorted(sorted, 25); got != 17.5 {
		t.Fatalf("percentileSorted interp = %v, want 17.5", got)
	}
	if got := percentileSorted(sorted, 150); got != 40 {
		t.Fatalf("percentileSorted high = %v, want 40", got)
	}
	if got := percentileSorted(nil, 50); got != 0 {
		t.Fatalf("percentileSorted empty = %v, want 0", got)
	}

	if got := robustSigma([]float64{1, 2, 3, 4, 5}, 3); math.Abs(got-1.4826) > 1e-4 {
		t.Fatalf("robustSigma MAD = %v, want about 1.4826", got)
	}
	if got := robustSigma([]float64{2, 2, 2, 2, 4}, 2); math.Abs(got-0.36) > 1e-2 {
		t.Fatalf("robustSigma fallback = %v, want about 0.36", got)
	}
	if got := robustSigma(nil, 0); got != 1 {
		t.Fatalf("robustSigma empty = %v, want 1", got)
	}
}

func TestSharedDrizzleGridMatchesRelevantMetadata(t *testing.T) {
	makeImage := func(header fitsio.Header, w, h int) *models.LoadedImage {
		return &models.LoadedImage{
			HDU: fitsio.HDU{
				Header: header,
				Data: fitsio.ImageData{
					Width:  w,
					Height: h,
					Pixels: make([]float32, w*h),
				},
			},
		}
	}

	baseHeader := fitsio.Header{Cards: map[string]string{
		"DRIZSCAL": "2",
		"ORIGOFFX": "10.5",
		"ORIGOFFY": "-3.25",
	}}
	ref := makeImage(baseHeader, 3, 2)
	same := makeImage(fitsio.Header{Cards: map[string]string{
		"DRIZSCAL": "2.0000001",
		"ORIGOFFX": "10.5000001",
		"ORIGOFFY": "-3.2500001",
	}}, 3, 2)
	if !sharedDrizzleGrid(same, ref) {
		t.Fatal("sharedDrizzleGrid should accept matching metadata within tolerance")
	}

	diffScale := makeImage(fitsio.Header{Cards: map[string]string{
		"DRIZSCAL": "3",
		"ORIGOFFX": "10.5",
		"ORIGOFFY": "-3.25",
	}}, 3, 2)
	if sharedDrizzleGrid(diffScale, ref) {
		t.Fatal("sharedDrizzleGrid should reject different scale")
	}

	diffSize := makeImage(baseHeader, 4, 2)
	if sharedDrizzleGrid(diffSize, ref) {
		t.Fatal("sharedDrizzleGrid should reject different dimensions")
	}

	missing := makeImage(fitsio.Header{Cards: map[string]string{}}, 3, 2)
	if sharedDrizzleGrid(missing, ref) {
		t.Fatal("sharedDrizzleGrid should reject missing metadata")
	}
}
