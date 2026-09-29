package processing

import (
	"context"
	"math"
	"testing"
)

func syntheticDiffractionRingField(width int) ([]float32, *StarMap) {
	p := make([]float32, width*width)
	c := float64(width-1) / 2
	for y := 0; y < width; y++ {
		for x := 0; x < width; x++ {
			dx, dy := float64(x)-c, float64(y)-c
			r := math.Hypot(dx, dy)
			profile := 7 * math.Exp(-r*r/(2*3.1*3.1)) * (1 + .62*math.Sin(2.2*r))
			if profile < 0 {
				profile = 0
			}
			p[y*width+x] = float32(.1 + profile)
		}
	}
	return p, &StarMap{Width: width, Height: width, Sources: []StarMapSource{{
		ID: 1270, X: c, Y: c, FWHM: 2.355 * 3.1, Radius: 10, Status: "accepted",
	}}}
}

func TestFitStarTreatmentAcceptsValidatedDiffractionRings(t *testing.T) {
	p, m := syntheticDiffractionRingField(101)
	fits, err := FitStarTreatment(context.Background(), m, p, 101, 101, StarTreatmentOptions{})
	if err != nil || len(fits) != 1 {
		t.Fatalf("ring fit failed: fits=%+v err=%v", fits, err)
	}
	f := fits[0]
	if !f.Usable || !f.RingValidated || f.WingModel != "EmpiricalRing" || f.RingOscillations < 2 {
		t.Fatalf("expected validated empirical ring fit: %+v", f)
	}
	if len(f.RingRadii) != len(f.RingValues) || len(f.RingValues) < 6 || f.Residual > .45 {
		t.Fatalf("incomplete ring diagnostics: %+v", f)
	}
}

func TestFitStarTreatmentRejectsMonotonicProfileAsRing(t *testing.T) {
	p, m := clippedWingFixture(101, false)
	m.Sources[0].Saturated = false
	fits, err := FitStarTreatment(context.Background(), m, p, 101, 101, StarTreatmentOptions{})
	if err != nil || len(fits) != 1 {
		t.Fatalf("monotonic fit failed: fits=%+v err=%v", fits, err)
	}
	if fits[0].RingValidated {
		t.Fatalf("monotonic profile must not be accepted as a ring: %+v", fits[0])
	}
}

func TestFitDiffractionRingProfileHonorsCancellation(t *testing.T) {
	p, m := syntheticDiffractionRingField(101)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, ok := fitDiffractionRingProfile(ctx, StarTreatmentFit{SourceID: 1270, X: m.Sources[0].X, Y: m.Sources[0].Y}, m.Sources[0], m.Sources, p, 101, 101, defaultStarTreatmentOptions(StarTreatmentOptions{}))
	if ok || f.RingValidated {
		t.Fatalf("cancelled ring fit returned a result: fit=%+v ok=%v", f, ok)
	}
}
