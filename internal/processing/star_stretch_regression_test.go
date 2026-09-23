package processing

import (
	"context"
	"math"
	"reflect"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func regressionMeta(mode stretch.Mode) models.LoadedImage {
	return models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 3, Height: 1}}, Mode: mode, Peak: 1, ScaledPeak: 1, AsinhScale: 1}
}

func TestApplyGentlerStarStretchUsesIncrementEquationWithNoisySample(t *testing.T) {
	// The observed sample is below the fitted B+S value. The renderer must still
	// apply the model correction; a per-pixel actual-vs-model gate reintroduces
	// untreated pixels and creates speckled stellar interiors.
	linear := []float32{0.48, 0.50, 0.10}
	fit := []StarTreatmentFit{{X: 0, Y: 0, Background: .10, Signal: .40, Sigma: 1, OuterRadius: .5, Usable: true}}
	mask := []float32{1, 0, 0}
	meta := regressionMeta(stretch.Asinh)
	got, err := ApplyGentlerStarStretch(context.Background(), linear, 3, 1, meta, fit, mask, StarStretchOptions{Strength: 0.7})
	if err != nil {
		t.Fatal(err)
	}
	b := unclippedStarStretch(.10, meta)
	tb := unclippedStarStretch(float64(linear[0]), meta)
	excess := tb - b
	want := b + excess/(1+4*.7*excess)
	if math.Abs(float64(got[0])-want) > 1e-6 {
		t.Fatalf("noisy sample got %v, want equation result %v", got[0], want)
	}
	if got[1] != float32(DiskStretchPreviewValue(linear[1], meta)) || got[2] != float32(DiskStretchPreviewValue(linear[2], meta)) {
		t.Fatalf("unmasked pixels changed: %v", got)
	}
}

func TestApplyGentlerStarStretchIsMonotonicInStrengthAndPreservesInput(t *testing.T) {
	linear := []float32{.52, .25, .5, .8}
	before := append([]float32(nil), linear...)
	fit := []StarTreatmentFit{{X: 0, Y: 0, Background: 0, Signal: .5, Sigma: 1, OuterRadius: .5, Usable: true}}
	mask := []float32{1, 1, 1, 0}
	meta := regressionMeta(stretch.Linear)
	var previous []float32
	for _, strength := range []float64{0, .25, .5, 1} {
		got, err := ApplyGentlerStarStretch(context.Background(), linear, 4, 1, meta, fit, mask, StarStretchOptions{Strength: strength})
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil {
			for i := 0; i < 3; i++ {
				if got[i] > previous[i]+1e-6 {
					t.Fatalf("strength %v increased pixel %d: previous=%v current=%v", strength, i, previous[i], got[i])
				}
			}
		}
		previous = got
	}
	if !reflect.DeepEqual(linear, before) {
		t.Fatalf("input mutated: before=%v after=%v", before, linear)
	}
}

func TestApplyGentlerStarStretchOverlapIsIndependentOfFitOrder(t *testing.T) {
	meta := regressionMeta(stretch.Asinh)
	linear := []float32{.3, .3, .3, .3, .3}
	mask := []float32{.5, 1, 1, 1, .5}
	a := []StarTreatmentFit{{X: 1, Y: 0, Background: .1, Signal: .4, Sigma: 1, OuterRadius: 3, Usable: true}, {X: 3, Y: 0, Background: .1, Signal: .2, Sigma: 1, OuterRadius: 3, Usable: true}}
	b := []StarTreatmentFit{a[1], a[0]}
	left, err := ApplyGentlerStarStretch(context.Background(), linear, 5, 1, meta, a, mask, StarStretchOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}
	right, err := ApplyGentlerStarStretch(context.Background(), linear, 5, 1, meta, b, mask, StarStretchOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("fit order changed overlap result: %v vs %v", left, right)
	}
}

func TestFitStarTreatmentRejectsMalformedAndInsufficientEdgeSources(t *testing.T) {
	pixels := make([]float32, 25*25)
	m := &StarMap{Width: 25, Height: 25, Sources: []StarMapSource{
		{ID: 1, X: math.NaN(), Y: 12, Radius: 5, FWHM: 3, Status: "accepted"},
		{ID: 2, X: 1, Y: 1, Radius: 3, FWHM: 3, Status: "accepted"},
	}}
	fits, err := FitStarTreatment(context.Background(), m, pixels, 25, 25, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fits) != 2 || fits[0].Usable || fits[1].Usable {
		t.Fatalf("malformed/edge sources unexpectedly usable: %+v", fits)
	}
}

func TestFitStarTreatmentHandlesDeterministicBackgroundNoise(t *testing.T) {
	const w, h = 41, 41
	pixels := make([]float32, w*h)
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 7, X: 20, Y: 20, FWHM: 3, Radius: 6, Status: "accepted"}}}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-20, float64(y)-20
			noise := .003 * math.Sin(float64(x*17+y*11))
			pixels[y*w+x] = float32(2 + .01*dx + .02*dy + noise + 20*math.Exp(-.5*(dx*dx+dy*dy)/(1.274*1.274)))
		}
	}
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fits) != 1 || !fits[0].Usable || math.Abs(fits[0].Background-2) > .08 {
		t.Fatalf("noisy fit=%+v", fits)
	}
}
