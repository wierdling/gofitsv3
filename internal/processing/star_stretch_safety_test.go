package processing

import (
	"context"
	"errors"
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func TestStarTreatmentRejectsOverflowAndCancellation(t *testing.T) {
	if _, err := BuildStarTreatmentMask(context.Background(), nil, int(^uint(0)>>1), 2); err == nil {
		t.Fatal("overflow dimensions accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := BuildStarTreatmentMask(ctx, nil, 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := FitStarTreatment(ctx, &StarMap{Width: 1, Height: 1}, []float32{0}, 1, 1, StarTreatmentOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestStarStretchDoesNotSubtractAnOversizedModel(t *testing.T) {
	fits := []StarTreatmentFit{{SourceID: 1, X: 0, Y: 0, Background: .2, Signal: .7, Sigma: 1, OuterRadius: .5, Usable: true}}
	out, err := ApplyGentlerStarStretch(context.Background(), []float32{.21}, 1, 1, models.LoadedImage{Mode: stretch.Linear, Peak: 1}, fits, nil, StarStretchOptions{Strength: 1})
	if err != nil || len(out) != 1 || out[0] < .2 || out[0] > .21 {
		t.Fatalf("observed stellar excess must stay above its background: %v, %v", out, err)
	}
}

func TestStarStretchTreatsDisplayClippedCoreFromTheFittedModel(t *testing.T) {
	fits := []StarTreatmentFit{{SourceID: 1, X: 0, Y: 0, Background: .2, Signal: 2, Sigma: 1, OuterRadius: .5, Usable: true}}
	got, err := ApplyGentlerStarStretch(context.Background(), []float32{2.2}, 1, 1, models.LoadedImage{Mode: stretch.Asinh, Peak: 1, ScaledPeak: 10}, fits, nil, StarStretchOptions{Strength: .5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !starFinite(float64(got[0])) || got[0] < 0 || got[0] >= 1 {
		t.Fatalf("clipped core was not gently treated: %v", got)
	}
}

func TestStarStretchTreatsDisplayClippingAtScreenshotMTFSettings(t *testing.T) {
	meta := models.LoadedImage{Mode: stretch.MTF, Background: -0.0219, Peak: 0.1730, ScaledPeak: 10, MTFMidtone: .5}
	const w = 81
	fits := []StarTreatmentFit{{SourceID: 673, X: 40, Y: 40, Background: .001, Signal: 100, Sigma: 2, OuterRadius: 6, Noise: .00001, Usable: true}}
	linear := make([]float32, w*w)
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-40), float64(y-40)
			linear[y*w+x] = float32(.001 + 100*math.Exp(-(dx*dx+dy*dy)/8))
		}
	}
	fits, err := PrepareStarStretchFits(context.Background(), linear, w, w, meta, fits, nil)
	if err != nil {
		t.Fatal(err)
	}
	mask, err := BuildStarTreatmentMask(context.Background(), fits, w, w)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ApplyGentlerStarStretch(context.Background(), linear, w, w, meta, fits, mask, StarStretchOptions{Strength: .35})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range got {
		if !starFinite(float64(v)) || v < 0 || v > 1 {
			t.Fatalf("pixel %d escaped bounded display range: %v", i, v)
		}
	}
	if got[40*w+40] >= 1 {
		t.Fatalf("clipped stellar core was left unchanged: %v", got[40*w+40])
	}
	previous := got[40*w+40]
	for r := 1; r < 30; r++ {
		v := got[40*w+40+r]
		if v > previous+1e-6 {
			t.Fatalf("hollow core/bright ring at radius%d: %v > %v", r, v, previous)
		}
		if math.Abs(float64(v-got[40*w+40-r])) > 1e-6 {
			t.Fatalf("asymmetric correction at radius%d", r)
		}
		if linear[40*w+40+r] >= float32(meta.Peak) && mask[40*w+40+r] != 1 {
			t.Fatalf("clipped plateau tapered prematurely at radius%d", r)
		}
		previous = v
	}
}

func TestStarStretchRejectsInvalidMasksAndModels(t *testing.T) {
	good := StarTreatmentFit{X: 0, Y: 0, Background: .1, Signal: .2, Sigma: 1, OuterRadius: .5, Usable: true}
	meta := models.LoadedImage{Mode: stretch.Linear, Peak: 1}
	for _, v := range []float32{float32(math.NaN()), float32(math.Inf(1)), -.1, 1.1} {
		if _, err := ApplyGentlerStarStretch(context.Background(), []float32{.4}, 1, 1, meta, []StarTreatmentFit{good}, []float32{v}, StarStretchOptions{Strength: .5}); err == nil {
			t.Fatalf("accepted mask %v", v)
		}
	}
	bad := good
	bad.BackgroundX = math.NaN()
	if _, err := ApplyGentlerStarStretch(context.Background(), []float32{.4}, 1, 1, meta, []StarTreatmentFit{good, bad}, nil, StarStretchOptions{Strength: .5}); err == nil {
		t.Fatal("invalid fit in multi-source render accepted")
	}
}

func TestFitStarTreatmentMeasuresWidthAndRejectsSaturation(t *testing.T) {
	p, w, m := syntheticStarTreatmentImage()
	// A misleading catalog width must not be copied blindly into the treatment.
	m.Sources[0].FWHM = 4
	fits, err := FitStarTreatment(context.Background(), m, p, w, w, StarTreatmentOptions{})
	if err != nil || len(fits) != 1 || !fits[0].Usable {
		t.Fatalf("fit %v, %v", fits, err)
	}
	if math.Abs(fits[0].Sigma-1.274) > .09 {
		t.Fatalf("width not measured: %v", fits[0].Sigma)
	}
	m.Sources[0].Saturated = true
	fits, err = FitStarTreatment(context.Background(), m, p, w, w, StarTreatmentOptions{})
	if err != nil || fits[0].Usable {
		t.Fatalf("saturated fit not skipped: %v %v", fits, err)
	}
}

func TestFitStarTreatmentRejectsBrightTruncatedSource(t *testing.T) {
	const w = 41
	p := make([]float32, w*w)
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-2), float64(y-20)
			p[y*w+x] = float32(2 + 20*math.Exp(-(dx*dx+dy*dy)/3.25))
		}
	}
	m := &StarMap{Width: w, Height: w, Sources: []StarMapSource{{ID: 1, X: 2, Y: 20, FWHM: 3, Radius: 6, Status: "accepted"}}}
	fits, err := FitStarTreatment(context.Background(), m, p, w, w, StarTreatmentOptions{})
	if err != nil || fits[0].Usable {
		t.Fatalf("truncated bright source accepted: %v %v", fits, err)
	}
}

func TestStarStretchZeroMatchesComposeScalarModes(t *testing.T) {
	p := []float32{-1, 0, .02, .2, .5, 1, 2, float32(math.NaN())}
	for _, mode := range []stretch.Mode{stretch.Linear, stretch.Log, stretch.Asinh, stretch.Sqrt, stretch.MTF} {
		meta := models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: len(p), Height: 1, Pixels: p}}, Mode: mode, Background: .01, Peak: 1, ScaledPeak: 20, AsinhScale: 1.4, MTFMidtone: .15}
		want, _ := ApplyStretchParallel(&meta)
		got, err := ApplyGentlerStarStretch(context.Background(), p, len(p), 1, meta, nil, nil, StarStretchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for i := range got {
			if math.Abs(float64(got[i]-want.Pixels[i])) > 1e-7 {
				t.Fatalf("mode %v sample %d got %v want %v", mode, i, got[i], want.Pixels[i])
			}
		}
	}
}

func TestStarStretchFootprintDoesNotGrowIntoNeighbor(t *testing.T) {
	const w = 81
	p := make([]float32, w*w)
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-40), float64(y-40)
			p[y*w+x] = float32(.001 + 100*math.Exp(-(dx*dx+dy*dy)/8))
		}
	}
	f := []StarTreatmentFit{{SourceID: 1, X: 40, Y: 40, Background: .001, Signal: 100, Sigma: 2, OuterRadius: 6, Noise: .00001, Usable: true}}
	neighbor := []StarMapSource{{ID: 2, X: 50, Y: 40, Radius: 4, Status: "accepted"}}
	prepared, err := PrepareStarStretchFits(context.Background(), p, w, w, models.LoadedImage{Mode: stretch.MTF, Peak: .173, Background: -.0219, MTFMidtone: .5}, f, neighbor)
	if err != nil {
		t.Fatal(err)
	}
	if prepared[0].Usable || prepared[0].Reason != "visible halo reaches a neighbor or preview boundary" {
		t.Fatalf("expanded footprint overlapped neighboring star: %+v", prepared[0])
	}
	if f[0].OuterRadius != 6 || f[0].InnerRadius != 0 {
		t.Fatal("input fit mutated")
	}
}

func TestUnclippedStarStretchIsMonotonicAcrossWhitePoint(t *testing.T) {
	for _, mode := range []stretch.Mode{stretch.Linear, stretch.Log, stretch.Asinh, stretch.Sqrt, stretch.MTF} {
		for _, mid := range []float64{.1, .5, .9} {
			meta := models.LoadedImage{Mode: mode, Background: 0, Peak: 1, ScaledPeak: 10, MTFMidtone: mid}
			previous := 0.
			for _, v := range []float64{0, .1, .9, .999999, 1, 1.000001, 2, 100} {
				q := unclippedStarStretch(v, meta)
				if !starFinite(q) || q < previous {
					t.Fatalf("nonmonotonic mode %v at %g: %g < %g", mode, v, q, previous)
				}
				if v <= 1 && math.Abs(q-float64(DiskStretchPreviewValue(float32(v), meta))) > 1e-6 {
					t.Fatalf("normal-range mismatch mode%v at %g", mode, v)
				}
				previous = q
			}
		}
	}
}
