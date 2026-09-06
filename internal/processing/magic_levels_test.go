package processing

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

// makeMagicField builds a synthetic field: flat sky + noise pattern, a diffuse
// nebula blob, and a few very bright compact stars.
func makeMagicField(w, h int) []float32 {
	px := make([]float32, w*h)
	const sky = 100.0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			// Deterministic pseudo-noise around the sky level, amplitude ~3.
			v := sky + float64((x*7+y*13)%7) - 3
			// Diffuse nebula bump in a central region (well above sky+2.5sigma).
			dx, dy := float64(x-w/2), float64(y-h/2)
			if dx*dx+dy*dy < float64((w/4)*(w/4)) {
				v += 40
			}
			px[i] = float32(v)
		}
	}
	// Bright compact stars: single hot peaks far above any nebulosity.
	for _, s := range [][2]int{{10, 10}, {w - 10, 8}, {w / 3, h - 6}} {
		px[s[1]*w+s[0]] = 5000
	}
	return px
}

func TestApplyMagicLevelsAndMTFUsesRobustResultForPaddedField(t *testing.T) {
	const w, h = 96, 80
	px := make([]float32, w*h)
	for y := 20; y < h-20; y++ {
		for x := 20; x < w-20; x++ {
			px[y*w+x] = float32(100 + (x*7+y*11)%5)
		}
	}
	px[(h/2)*w+w/2] = 125
	for y := 20; y < h-20; y++ {
		for x := 60; x < w-20; x++ {
			px[y*w+x] = 150
		}
	}
	// A Gemini-like frame has a majority padded border. The old two-step route
	// included those zeros in Auto MTF's statistics and drove the real field to
	// white; the combined operation must keep it at a midtone.
	old := &models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Pixels: append([]float32(nil), px...), Width: w, Height: h}}}
	ApplyMagicLevels(old, MagicBalanced)
	AutoMTFMidtone(old)
	oldStretched, _ := ApplyStretchParallel(old)
	oldField := float64(oldStretched.Pixels[(h/2)*w+w/2])
	if oldField < 0.75 {
		t.Fatalf("legacy representative field output %.3f is not oversaturated", oldField)
	}
	t.Logf("legacy representative field output %.3f", oldField)
	img := &models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Pixels: px, Width: w, Height: h}}}
	res := ApplyMagicLevelsAndMTF(img, MagicBalanced)
	if res.ValidPixels == 0 || img.Mode != stretch.MTF || !finite(img.MTFMidtone) {
		t.Fatalf("Magic stretch result = %#v, mode=%v, mtf=%v", res, img.Mode, img.MTFMidtone)
	}
	stretched, _ := ApplyStretchParallel(img)
	field := float64(stretched.Pixels[(h/2)*w+w/2])
	t.Logf("shared Magic representative field output %.3f", field)
	if field >= oldField {
		t.Fatalf("shared Magic MTF output %.3f did not improve over legacy %.3f", field, oldField)
	}
	if field >= 0.75 {
		t.Fatalf("representative field output %.3f is oversaturated; result=%+v", field, res)
	}
}

func TestApplyMagicLevelsAndMTFHandlesDegenerateLevels(t *testing.T) {
	img := &models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Pixels: []float32{5, 5, 5, 5}, Width: 2, Height: 2}}}
	ApplyMagicLevelsAndMTF(img, MagicBalanced)
	if !finite(img.MTFMidtone) || img.MTFMidtone <= 0 || img.MTFMidtone > 0.5 {
		t.Fatalf("degenerate Magic MTF = %v, want finite in (0, .5]", img.MTFMidtone)
	}
}

func TestMagicLevelsBlackAndStarExclusion(t *testing.T) {
	w, h := 200, 200
	px := makeMagicField(w, h)

	res := MagicLevels(px, w, h, nil, MagicNebula)

	if res.ValidPixels != w*h {
		t.Fatalf("ValidPixels = %d, want %d", res.ValidPixels, w*h)
	}
	// Black should sit near the sky floor, not the true minimum, and produce
	// only a tiny amount of low clipping.
	if res.Black <= 90 || res.Black >= 110 {
		t.Errorf("Black = %.3f, want near sky ~97-103", res.Black)
	}
	if res.ClipLowPercent > 1.0 {
		t.Errorf("ClipLowPercent = %.3f%%, want tiny (<1%%)", res.ClipLowPercent)
	}
	if !res.StarsExcluded {
		t.Errorf("expected stars to be excluded")
	}
	// Nebula white must be driven by nebulosity (~sky+40), NOT by the 5000 stars.
	if res.White <= res.Black {
		t.Fatalf("White %.3f <= Black %.3f", res.White, res.Black)
	}
	if res.White > 1000 {
		t.Errorf("White = %.3f, nebula white should not be dragged toward stars (5000)", res.White)
	}
	if res.WhiteSampleSource != "diffuse" {
		t.Errorf("WhiteSampleSource = %q, want \"diffuse\"", res.WhiteSampleSource)
	}
}

func TestMagicLevelsIgnoresPaddingAndNaN(t *testing.T) {
	w, h := 64, 64
	px := make([]float32, w*h)
	for i := range px {
		px[i] = 50 // uniform real data
	}
	// Surround/scatter invalid pixels that must be ignored.
	px[0] = 0                    // drizzle padding
	px[1] = float32(math.NaN())  // NaN
	px[2] = float32(math.Inf(1)) // Inf
	res := MagicLevels(px, w, h, nil, MagicBalanced)

	if res.ValidPixels != w*h-3 {
		t.Fatalf("ValidPixels = %d, want %d", res.ValidPixels, w*h-3)
	}
	// All valid data is 50; black/white must bracket it without NaN/Inf leaking.
	if math.IsNaN(res.Black) || math.IsInf(res.Black, 0) || res.Black > 50 {
		t.Errorf("Black = %v, want finite and <= 50", res.Black)
	}
	if res.White <= res.Black {
		t.Errorf("White %.3f must exceed Black %.3f even on flat data", res.White, res.Black)
	}
}

func TestMagicPresetWhiteOrdering(t *testing.T) {
	w, h := 200, 200
	px := makeMagicField(w, h)

	neb := MagicLevels(px, w, h, nil, MagicNebula)
	gal := MagicLevels(px, w, h, nil, MagicGalaxy)

	// Galaxy uses a higher percentile over all non-star pixels, so its white
	// point should be at least as high as the nebula (diffuse-protecting) one.
	if gal.White < neb.White {
		t.Errorf("galaxy white %.3f < nebula white %.3f", gal.White, neb.White)
	}
	if !(gal.Black >= neb.Black) {
		t.Errorf("galaxy black %.3f < nebula black %.3f; galaxy sky floor should be higher", gal.Black, neb.Black)
	}
	if math.IsNaN(gal.Black) || math.IsInf(gal.Black, 0) || gal.ClipLowPercent > 10 {
		t.Errorf("galaxy black diagnostics are not sensible: black=%.3f clip-low=%.3f%%", gal.Black, gal.ClipLowPercent)
	}
}

func TestMagicGalaxyDarkensNormalizedSky(t *testing.T) {
	const w, h = 200, 1
	px := make([]float32, w*h)
	for i := range px {
		px[i] = float32(100 + 0.5*float64(i))
	}
	balanced := MagicLevels(px, w, h, nil, MagicBalanced)
	galaxy := MagicLevels(px, w, h, nil, MagicGalaxy)
	if galaxy.Black <= balanced.Black {
		t.Fatalf("galaxy black %.3f must exceed balanced %.3f", galaxy.Black, balanced.Black)
	}
	const sky = 102.0
	baseNorm := (sky - balanced.Black) / (balanced.White - balanced.Black)
	galNorm := (sky - galaxy.Black) / (galaxy.White - galaxy.Black)
	if galNorm >= baseNorm {
		t.Fatalf("normalized sky %.6f is not darker than baseline %.6f", galNorm, baseNorm)
	}
	nebula := MagicLevels(px, w, h, nil, MagicNebula)
	if nebula.Black != balanced.Black {
		t.Fatalf("nebula black %.3f changed from balanced %.3f", nebula.Black, balanced.Black)
	}
	if galaxy.White <= galaxy.Black {
		t.Fatalf("galaxy white %.3f must exceed black %.3f", galaxy.White, galaxy.Black)
	}
}
