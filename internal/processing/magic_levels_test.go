package processing

import (
	"math"
	"testing"
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
	px[0] = 0                          // drizzle padding
	px[1] = float32(math.NaN())        // NaN
	px[2] = float32(math.Inf(1))       // Inf
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
}
