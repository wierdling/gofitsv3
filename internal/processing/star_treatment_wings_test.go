package processing

import (
	"context"
	"errors"
	"math"
	"testing"

	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func TestSaturatedWingFitPropagatesCancellationFromProgress(t *testing.T) {
	p, m := clippedWingFixture(81, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fits, err := FitStarTreatment(ctx, m, p, 81, 81, StarTreatmentOptions{
		Progress: func(string, int, int) { cancel() },
	})
	if !errors.Is(err, context.Canceled) || fits != nil {
		t.Fatalf("cancelled fit returned results: fits=%v err=%v", fits, err)
	}
	if _, _, _, _, ok := saturatedWingBackground(ctx, 40, 40, 16, func(id int) bool { return id == 31 }, m.Sources, p, 81, 81, 20); ok {
		t.Fatal("cancelled background scan returned a usable fit")
	}
	if radius := saturatedPlateauRadius(ctx, 40, 40, 4, 16, .01, 0, 0, .001, p, 81, 81); radius != 0 {
		t.Fatalf("cancelled plateau scan returned a measured radius: %v", radius)
	}
}

func clippedWingFixture(w int, contam bool) ([]float32, *StarMap) {
	p := make([]float32, w*w)
	c := float64(w-1) / 2
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-c, float64(y)-c
			v := .01 + .9*math.Exp(-.5*(dx*dx+dy*dy)/4)
			if v > .3 {
				v = .3
			} // detector-like clipped core, with usable wings
			if contam && dx > 3 && dy < -1 {
				v += .8 * math.Exp(-.5*((dx-6)*(dx-6)+(dy+4)*(dy+4))/2)
			}
			p[y*w+x] = float32(v)
		}
	}
	m := &StarMap{Width: w, Height: w, Sources: []StarMapSource{{ID: 31, X: c, Y: c, FWHM: 4.7, Radius: 8, Status: "accepted", Saturated: true}}}
	return p, m
}

func TestFitStarTreatmentValidatesClippedSaturatedWings(t *testing.T) {
	p, m := clippedWingFixture(81, false)
	fits, err := FitStarTreatment(context.Background(), m, p, 81, 81, StarTreatmentOptions{})
	if err != nil || len(fits) != 1 || !fits[0].Usable || !fits[0].Saturated || !fits[0].WingValidated {
		t.Fatalf("wing fit=%+v err=%v", fits, err)
	}
	if fits[0].CoreRadius <= 0 || fits[0].CoreRadius >= fits[0].OuterRadius || fits[0].WingModel == "" {
		t.Fatalf("missing core/model diagnostics: %+v", fits[0])
	}
}

func TestFitStarTreatmentRejectsInsufficientSaturatedWings(t *testing.T) {
	p := make([]float32, 41*41)
	m := &StarMap{Width: 41, Height: 41, Sources: []StarMapSource{{ID: 1, X: 20, Y: 20, FWHM: 10, Radius: 12, Status: "accepted", Saturated: true}}}
	fits, err := FitStarTreatment(context.Background(), m, p, 41, 41, StarTreatmentOptions{})
	if err != nil || len(fits) != 1 || fits[0].Usable || fits[0].WingValidated {
		t.Fatalf("insufficient wings accepted: %+v err=%v", fits, err)
	}
}

func TestSaturatedWingFootprintHasNoDarkRing(t *testing.T) {
	p, m := clippedWingFixture(81, false)
	fits, err := FitStarTreatment(context.Background(), m, p, 81, 81, StarTreatmentOptions{})
	if err != nil || !fits[0].Usable {
		t.Fatalf("fit=%+v err=%v", fits, err)
	}
	meta := models.LoadedImage{Mode: stretch.MTF, Background: 0, Peak: .55, MTFMidtone: .5}
	fits, err = PrepareStarStretchFits(context.Background(), p, 81, 81, meta, fits, nil)
	if err != nil {
		t.Fatal(err)
	}
	mask, err := BuildStarTreatmentMask(context.Background(), fits, 81, 81)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ApplyGentlerStarStretch(context.Background(), p, 81, 81, meta, fits, mask, StarStretchOptions{Strength: .75})
	if err != nil {
		t.Fatal(err)
	}
	center := out[40*81+40]
	for r := 1; r < 18; r++ {
		v := out[40*81+40+r]
		if v > center+1e-6 {
			t.Fatalf("dark ring/bright edge at radius %d: center=%v edge=%v", r, center, v)
		}
	}
}
