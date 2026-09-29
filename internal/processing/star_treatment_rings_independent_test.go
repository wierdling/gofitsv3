package processing

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"gofitsv3/internal/stretch"
)

// diffractionRingFixture is intentionally generated from a radial profile,
// rather than from any implementation helper. The small positive halo keeps
// dark gaps measurable while the ring peaks remain well above the background.
func diffractionRingFixture(width int, cx, cy float64, mutate func(r, dx, dy, v float64) float64) ([]float32, StarMapSource) {
	pixels := make([]float32, width*width)
	for y := 0; y < width; y++ {
		for x := 0; x < width; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			r := math.Hypot(dx, dy)
			v := .1 + .09*math.Exp(-r/5)
			v += 5 * math.Exp(-.5*r*r/(1.35*1.35))
			v += 2.8 * math.Exp(-.5*(r-4)*(r-4)/(.62*.62))
			v += 1.35 * math.Exp(-.5*(r-7)*(r-7)/(.72*.72))
			v += .65 * math.Exp(-.5*(r-10)*(r-10)/(.8*.8))
			if mutate != nil {
				v = mutate(r, dx, dy, v)
			}
			pixels[y*width+x] = float32(v)
		}
	}
	return pixels, StarMapSource{ID: 1270, X: cx, Y: cy, FWHM: 3.2, Radius: 10, Status: "accepted"}
}

func ringSeed(s StarMapSource) StarTreatmentFit {
	return StarTreatmentFit{SourceID: s.ID, X: s.X, Y: s.Y, Sigma: 1.4}
}

func ringOptions() StarTreatmentOptions {
	return StarTreatmentOptions{MinSNR: 3, MinSamples: 20, OuterRadius: 16}
}

func TestDiffractionRingFitAcceptsSymmetricOscillatoryProfile(t *testing.T) {
	pixels, source := diffractionRingFixture(101, 50, 50, func(r, dx, dy, v float64) float64 {
		return v + .002*math.Sin(17*dx+11*dy)
	})
	fit, ok := fitDiffractionRingProfile(context.Background(), ringSeed(source), source, []StarMapSource{source}, pixels, 101, 101, ringOptions())
	if !ok || !fit.Usable {
		t.Fatalf("symmetric diffraction rings should be accepted: ok=%v fit=%+v", ok, fit)
	}
	if !fit.RingValidated || fit.WingModel != "EmpiricalRing" || fit.RingOscillations < 2 {
		t.Fatalf("fit did not retain ring diagnostics: %+v", fit)
	}
	if len(fit.RingRadii) < 6 || len(fit.RingRadii) != len(fit.RingValues) {
		t.Fatalf("ring profile diagnostics are incomplete: radii=%d values=%d", len(fit.RingRadii), len(fit.RingValues))
	}
}

func TestDiffractionRingFitRejectsOneSidedArcAndNeighborContamination(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(r, dx, dy, v float64) float64
	}{
		{name: "arc", mutate: func(r, dx, dy, v float64) float64 {
			if r > 3 && r < 11 && dx > 0 && math.Abs(dy) < 1.2 {
				return v + 4
			}
			return v
		}},
		{name: "neighbor", mutate: func(r, dx, dy, v float64) float64 {
			if dx > 0 && dy > 0 && dx < 12 && dy < 12 {
				return v + 10
			}
			return v
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pixels, source := diffractionRingFixture(101, 50, 50, tc.mutate)
			fit, ok := fitDiffractionRingProfile(context.Background(), ringSeed(source), source, []StarMapSource{source}, pixels, 101, 101, ringOptions())
			if ok || fit.RingValidated || fit.Usable {
				t.Fatalf("directional contamination must not validate as a ring: ok=%v fit=%+v", ok, fit)
			}
		})
	}
}

func TestDiffractionRingFitRejectsSmoothGaussian(t *testing.T) {
	pixels := make([]float32, 101*101)
	source := StarMapSource{ID: 1270, X: 50, Y: 50, FWHM: 3.2, Radius: 10, Status: "accepted"}
	for y := 0; y < 101; y++ {
		for x := 0; x < 101; x++ {
			r := math.Hypot(float64(x)-50, float64(y)-50)
			pixels[y*101+x] = float32(.1 + 5*math.Exp(-.5*r*r/(1.35*1.35)))
		}
	}
	fit, ok := fitDiffractionRingProfile(context.Background(), ringSeed(source), source, []StarMapSource{source}, pixels, 101, 101, ringOptions())
	if ok || fit.RingValidated {
		t.Fatalf("monotonic Gaussian must not validate as a ring: ok=%v fit=%+v", ok, fit)
	}
}

func TestDiffractionRingFitRejectsSmoothNoisyMoffat(t *testing.T) {
	const width = 101
	const cx, cy = 50.0, 50.0
	for _, seed := range []int64{7, 91, 2026} {
		t.Run("seed-"+formatRingSeed(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			pixels := make([]float32, width*width)
			source := StarMapSource{ID: 1270, X: cx, Y: cy, FWHM: 4.6, Radius: 10, Status: "accepted"}
			for y := 0; y < width; y++ {
				for x := 0; x < width; x++ {
					dx, dy := float64(x)-cx, float64(y)-cy
					r2 := dx*dx + dy*dy
					// A smooth beta=2.5 Moffat profile with 1% Gaussian-like
					// detector noise; no radial extrema are present in the signal.
					noise := .01 * (rng.Float64()*2 - 1)
					pixels[y*width+x] = float32(.1 + 4*math.Pow(1+r2/(2.0*2.0), -2.5) + noise)
				}
			}
			fit, ok := fitDiffractionRingProfile(context.Background(), ringSeed(source), source, []StarMapSource{source}, pixels, width, width, ringOptions())
			if ok || fit.RingValidated {
				t.Fatalf("smooth noisy Moffat must not validate as a ring: seed=%d ok=%v fit=%+v", seed, ok, fit)
			}
		})
	}
}

func formatRingSeed(seed int64) string {
	return fmt.Sprintf("%d", seed)
}

func TestDiffractionRingFitRejectsMissingRadialDataAndTruncatedSupport(t *testing.T) {
	pixels, source := diffractionRingFixture(101, 50, 50, func(r, dx, dy, v float64) float64 {
		if r > 5.5 && r < 8.5 {
			return float64(math.NaN()) // an unobserved radial gap
		}
		return v
	})
	fit, ok := fitDiffractionRingProfile(context.Background(), ringSeed(source), source, []StarMapSource{source}, pixels, 101, 101, ringOptions())
	if ok || fit.RingValidated {
		t.Fatalf("missing radial support must not validate as a ring: ok=%v fit=%+v", ok, fit)
	}

	pixels, source = diffractionRingFixture(31, 15, 15, nil)
	fit, ok = fitDiffractionRingProfile(context.Background(), ringSeed(source), source, []StarMapSource{source}, pixels, 31, 31, ringOptions())
	if ok || fit.RingValidated {
		t.Fatalf("edge-truncated support must be rejected: ok=%v fit=%+v", ok, fit)
	}
}

func TestDiffractionRingFitAcceptsResolvedDarkGapBetweenRings(t *testing.T) {
	pixels, source := diffractionRingFixture(101, 50, 50, func(r, dx, dy, v float64) float64 {
		if r > 5.5 && r < 8.5 {
			return .1 // a real diffraction minimum at the measured background
		}
		return v
	})
	fit, ok := fitDiffractionRingProfile(context.Background(), ringSeed(source), source, []StarMapSource{source}, pixels, 101, 101, ringOptions())
	if !ok || !fit.RingValidated {
		t.Fatalf("a resolved dark gap between bright rings should remain valid: ok=%v fit=%+v", ok, fit)
	}
}

func TestRingValidationDoesNotExcuseUnrelatedOneSidedNebulosity(t *testing.T) {
	clean, source := diffractionRingFixture(101, 50, 50, nil)
	fit, ok := fitDiffractionRingProfile(context.Background(), ringSeed(source), source, []StarMapSource{source}, clean, 101, 101, ringOptions())
	if !ok || !fit.RingValidated {
		t.Fatalf("clean ring fixture did not validate: ok=%v fit=%+v", ok, fit)
	}
	pixels, _ := diffractionRingFixture(101, 50, 50, func(r, dx, dy, v float64) float64 {
		// A bright filament starts beyond the measured ring core and crosses
		// only one side of the proposed feather. It is unrelated to the star's
		// rotationally consistent profile.
		if dx > 10 && dx < 17 && math.Abs(dy) < 1.1 {
			return v + 1.2
		}
		return v
	})
	prepared, err := PrepareStarStretchFits(context.Background(), pixels, 101, 101, regressionMeta(stretch.Linear), []StarTreatmentFit{fit}, []StarMapSource{source})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 1 {
		t.Fatalf("expected one prepared fit, got %d", len(prepared))
	}
	if prepared[0].Usable {
		t.Fatalf("ring validation must not bypass rejection of unrelated directional nebulosity: %+v", prepared[0])
	}
}
