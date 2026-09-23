package processing

import (
	"context"
	"math"
	"testing"
)

// blendScene renders a planar background plus Gaussian stars; clip saturates
// pixels above the given level so a member can carry a clipped core.
func blendScene(w, h int, bg, slope float64, stars [][4]float64, clip float64) []float32 {
	p := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := bg + slope*float64(x)
			for _, s := range stars {
				dx, dy := float64(x)-s[0], float64(y)-s[1]
				v += s[2] * math.Exp(-.5*(dx*dx+dy*dy)/(s[3]*s[3]))
			}
			if clip > 0 && v > clip {
				v = clip
			}
			p[y*w+x] = float32(v)
		}
	}
	return p
}

func TestBlendedPairIsFitJointly(t *testing.T) {
	const w, h = 61, 61
	pixels := blendScene(w, h, .2, .001, [][4]float64{{30, 30, 4, 1.3}, {35, 30, 3, 1.3}}, 0)
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{
		{ID: 21, X: 30, Y: 30, FWHM: 3, Radius: 6, Status: "accepted"},
		{ID: 22, X: 35, Y: 30, FWHM: 3, Radius: 6, Status: "accepted"},
	}}
	fits, mask, rendered := runFootprintPipeline(t, pixels, w, h, m)
	if len(fits) != 2 {
		t.Fatalf("expected two fits, got %d", len(fits))
	}
	want := []float64{4, 3}
	for i, f := range fits {
		if !f.Usable {
			t.Fatalf("blend member %d not usable: %+v", i, f)
		}
		if f.GroupID != 21 || len(f.Companions) != 1 || f.Companions[0].SourceID != fits[1-i].SourceID {
			t.Fatalf("blend member %d has wrong group/companions: %+v", i, f)
		}
		if math.Abs(f.Signal-want[i]) > .15 || math.Abs(f.Sigma-1.3) > .1 {
			t.Fatalf("blend member %d amplitude/width off: signal=%g sigma=%g", i, f.Signal, f.Sigma)
		}
		if math.Abs(f.Background-(.2+.001*f.X)) > .01 {
			t.Fatalf("blend member %d background off: %g", i, f.Background)
		}
	}
	meta := footprintValidationMeta(w, h)
	for _, s := range m.Sources {
		i := int(s.Y)*w + int(s.X)
		if mask[i] < .999 {
			t.Fatalf("mask does not cover star %d core: %g", s.ID, mask[i])
		}
		if normal := DiskStretchPreviewValue(pixels[i], meta); rendered[i] >= normal {
			t.Fatalf("star %d core was not compressed: normal=%g rendered=%g", s.ID, normal, rendered[i])
		}
	}
	// Outside the union footprint the render must equal the ordinary stretch.
	for i := range pixels {
		normal := DiskStretchPreviewValue(pixels[i], meta)
		if mask[i] == 0 && math.Abs(float64(rendered[i]-normal)) > 1e-7 {
			t.Fatalf("pixel %d outside footprint changed: normal=%g rendered=%g", i, normal, rendered[i])
		}
	}
}

func TestBlendedFaintCompanionIsCoveredByNeighbor(t *testing.T) {
	const w, h = 61, 61
	pixels := blendScene(w, h, .2, 0, [][4]float64{{30, 30, 4, 1.3}, {34, 30, .02, 1.3}}, 0)
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{
		{ID: 1, X: 30, Y: 30, FWHM: 3, Radius: 6, Status: "accepted"},
		{ID: 2, X: 34, Y: 30, FWHM: 3, Radius: 6, Status: "accepted"},
	}}
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{MinSNR: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !fits[0].Usable {
		t.Fatalf("bright member not usable: %+v", fits[0])
	}
	// Synthetic noise is essentially zero, so the faint companion may pass the
	// SNR gate; either way the bright star must know about it.
	if len(fits[0].Companions) != 1 || fits[0].Companions[0].SourceID != 2 {
		t.Fatalf("bright member lacks faint companion: %+v", fits[0])
	}
	if fits[1].Usable && fits[1].Signal > .05 {
		t.Fatalf("faint companion amplitude inflated: %+v", fits[1])
	}
	prepared, err := PrepareStarStretchFits(context.Background(), pixels, w, h, footprintValidationMeta(w, h), fits, m.Sources)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared[0].Usable {
		t.Fatalf("bright member rejected during preparation: %s", prepared[0].Reason)
	}
	if prepared[0].OuterRadius < 4 {
		t.Fatalf("bright member footprint was bounded by its companion: outer=%g", prepared[0].OuterRadius)
	}
}

func TestBlendedSaturatedPairFitsWings(t *testing.T) {
	const w, h = 101, 101
	pixels := blendScene(w, h, .1, 0, [][4]float64{{50, 50, 40, 2.2}, {56, 50, 25, 2.2}}, 5)
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{
		{ID: 7, X: 50, Y: 50, FWHM: 5.2, Radius: 10, Status: "accepted", Saturated: true},
		{ID: 8, X: 56, Y: 50, FWHM: 5.2, Radius: 10, Status: "accepted", Saturated: true},
	}}
	fits, mask, rendered := runFootprintPipeline(t, pixels, w, h, m)
	for i, f := range fits {
		if !f.Usable || !f.WingValidated || f.CoreRadius <= 0 {
			t.Fatalf("saturated blend member %d not usable: %+v", i, f)
		}
		if len(f.Companions) != 1 || f.Companions[0].CoreRadius <= 0 {
			t.Fatalf("saturated blend member %d lacks clipped companion: %+v", i, f)
		}
	}
	meta := footprintValidationMeta(w, h)
	for _, s := range m.Sources {
		i := int(s.Y)*w + int(s.X)
		if mask[i] < .999 {
			t.Fatalf("mask does not cover clipped core of %d: %g", s.ID, mask[i])
		}
		if normal := DiskStretchPreviewValue(pixels[i], meta); rendered[i] >= normal {
			t.Fatalf("clipped core %d was not compressed: normal=%g rendered=%g", s.ID, normal, rendered[i])
		}
	}
}

func TestBlendGroupsClusterOverlappingCores(t *testing.T) {
	sources := []StarMapSource{
		{ID: 1, X: 10, Y: 10, FWHM: 3, Radius: 5, Status: "accepted"},
		{ID: 2, X: 14, Y: 10, FWHM: 3, Radius: 5, Status: "accepted"},
		{ID: 3, X: 18, Y: 10, FWHM: 3, Radius: 5, Status: "accepted"},
		{ID: 4, X: 40, Y: 40, FWHM: 3, Radius: 5, Status: "accepted"},
		{ID: 5, X: 43, Y: 40, FWHM: 3, Radius: 5, Status: "rejected"},
		{ID: 6, X: 60, Y: 60, Radius: 5, Status: "accepted"}, // FWHM missing: fallback radius/1.6
		{ID: 7, X: 63, Y: 60, Radius: 5, Status: "accepted"},
	}
	groups := starBlendGroups(sources, 100, 100)
	if len(groups[1]) != 3 || len(groups[2]) != 3 || len(groups[3]) != 3 {
		t.Fatalf("chain was not clustered transitively: %v", groups)
	}
	if _, ok := groups[4]; ok {
		t.Fatal("rejected neighbor formed a group")
	}
	if len(groups[6]) != 2 {
		t.Fatalf("missing FWHM fallback did not blend: %v", groups[6])
	}
}
