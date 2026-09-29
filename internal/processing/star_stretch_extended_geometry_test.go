package processing

import (
	"context"
	"math"
	"testing"

	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func TestExtendedGeometryUsesSameHaloAndSpikeUnionForMaskAndRender(t *testing.T) {
	const w, h = 81, 81
	f := StarTreatmentFit{SourceID: 1, X: 40, Y: 40, Signal: 10, Sigma: 2, OuterRadius: 8, InnerRadius: 5, Noise: .01, Usable: true,
		HaloValidated: true, HaloInnerRadius: 14, HaloRadius: 22, ExtendedBackground: 0, ExtendedNoise: .01,
		Spikes: []StarTreatmentSpike{{Angle: .37, StartRadius: 3, EndRadius: 30, Width: 2}}}
	mask, err := BuildStarTreatmentMask(context.Background(), []StarTreatmentFit{f}, w, h)
	if err != nil {
		t.Fatal(err)
	}
	meta := models.LoadedImage{Mode: stretch.Linear, Background: 0, Peak: 1, ScaledPeak: 1}
	linear := make([]float32, w*h)
	for i := range linear {
		linear[i] = 1
	}
	out, err := ApplyGentlerStarStretch(context.Background(), linear, w, h, meta, []StarTreatmentFit{f}, mask, StarStretchOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]int{{40, 58}, {59, 47}} {
		i := p[1]*w + p[0]
		if mask[i] == 0 || !(out[i] < 1) {
			t.Fatalf("unexpected halo geometry at %v: mask=%v output=%v", p, mask[i], out[i])
		}
	}
	// For v=1, background=0, linear strength=1, the observable correction is
	// exactly .8 times the shared geometry weight. This catches mask/render
	// drift at halo and spike pixels rather than merely checking that output is
	// below one.
	out, err = ApplyGentlerStarStretch(context.Background(), linear, w, h, meta, []StarTreatmentFit{f}, nil, StarStretchOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i := range mask {
		want := 1 - .8*float64(mask[i])
		if math.Abs(float64(out[i])-want) > 1e-6 {
			t.Fatalf("render/mask geometry mismatch at pixel %d: mask=%g output=%g want=%g", i, mask[i], out[i], want)
		}
	}

	// Both spike tapers are continuous at their longitudinal and transverse
	// boundaries. A finite jump here would produce a visible seam.
	for _, d := range []float64{1e-4, 1e-3} {
		before := starTreatmentSpikeWeight((3-d)*math.Cos(.37), (3-d)*math.Sin(.37), f.Spikes[0])
		after := starTreatmentSpikeWeight((3+d)*math.Cos(.37), (3+d)*math.Sin(.37), f.Spikes[0])
		if before > .01 || after > .01 {
			t.Fatalf("spike start seam is not tapered: d=%g before=%g after=%g", d, before, after)
		}
		before = starTreatmentSpikeWeight((30-d)*math.Cos(.37), (30-d)*math.Sin(.37), f.Spikes[0])
		after = starTreatmentSpikeWeight((30+d)*math.Cos(.37), (30+d)*math.Sin(.37), f.Spikes[0])
		if before > .01 || after > .01 {
			t.Fatalf("spike end seam is not tapered: d=%g before=%g after=%g", d, before, after)
		}
		before = starTreatmentSpikeWeight(10*math.Cos(.37)+(2-d)*(-math.Sin(.37)), 10*math.Sin(.37)+(2-d)*math.Cos(.37), f.Spikes[0])
		after = starTreatmentSpikeWeight(10*math.Cos(.37)+(2+d)*(-math.Sin(.37)), 10*math.Sin(.37)+(2+d)*math.Cos(.37), f.Spikes[0])
		if before > .01 || after > .01 {
			t.Fatalf("spike transverse seam is not tapered: d=%g before=%g after=%g", d, before, after)
		}
	}
}

func TestExtendedGeometryRejectsInvalidIntervals(t *testing.T) {
	f := StarTreatmentFit{X: 10, Y: 10, Signal: 1, Sigma: 1, OuterRadius: 4, Noise: .1, Usable: true, HaloValidated: true, HaloInnerRadius: 3, HaloRadius: 8, ExtendedNoise: .1}
	if _, err := BuildStarTreatmentMask(context.Background(), []StarTreatmentFit{f}, 20, 20); err == nil {
		t.Fatal("invalid halo interval accepted")
	}
	f = StarTreatmentFit{X: 10, Y: 10, Signal: 1, Sigma: 1, OuterRadius: 4, Noise: .1, Usable: true, Spikes: []StarTreatmentSpike{{Angle: math.NaN(), StartRadius: 4, EndRadius: 8, Width: 1}}}
	if _, err := BuildStarTreatmentMask(context.Background(), []StarTreatmentFit{f}, 20, 20); err == nil {
		t.Fatal("nonfinite spike accepted")
	}
	f = StarTreatmentFit{X: 10, Y: 10, Signal: 1, Sigma: 1, OuterRadius: 4, Noise: .1, Usable: true, HaloValidated: true, HaloInnerRadius: 5, HaloRadius: math.NaN()}
	if _, err := BuildStarTreatmentMask(context.Background(), []StarTreatmentFit{f}, 20, 20); err == nil {
		t.Fatal("nonfinite halo accepted")
	}
}
