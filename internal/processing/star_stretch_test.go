package processing

import (
	"context"
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func syntheticStarTreatmentImage() ([]float32, int, *StarMap) {
	w, h := 41, 41
	p := make([]float32, w*h)
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 7, X: 20, Y: 20, FWHM: 3, Radius: 6, Status: "accepted"}}}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-20, float64(y)-20
			p[y*w+x] = float32(2 + .01*dx + .02*dy + 20*math.Exp(-.5*(dx*dx+dy*dy)/(1.274*1.274)))
		}
	}
	return p, w, m
}

func TestFitStarTreatmentModelsLinearStarAndBackground(t *testing.T) {
	p, w, m := syntheticStarTreatmentImage()
	fits, err := FitStarTreatment(context.Background(), m, p, w, 41, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fits) != 1 || !fits[0].Usable {
		t.Fatalf("fit=%+v", fits)
	}
	if math.Abs(fits[0].Background-2) > 0.08 {
		t.Fatalf("background=%v", fits[0].Background)
	}
	if fits[0].Signal < 8 || fits[0].SNR < 3 {
		t.Fatalf("fit=%+v", fits[0])
	}
}

func TestStarTreatmentMaskFeathersAndOverlapsByMaximum(t *testing.T) {
	f := []StarTreatmentFit{{X: 10, Y: 10, Signal: 1, Sigma: 1, OuterRadius: 5, Usable: true}, {X: 13, Y: 10, Signal: 2, Sigma: 1, OuterRadius: 5, Usable: true}}
	mask, err := BuildStarTreatmentMask(context.Background(), f, 24, 20)
	if err != nil {
		t.Fatal(err)
	}
	if mask[10*24+10] != 1 || mask[10*24+13] != 1 {
		t.Fatalf("core mask=%v,%v", mask[250], mask[253])
	}
	if mask[10*24+6] <= 0 || mask[10*24+6] >= 1 {
		t.Fatalf("feather=%v", mask[246])
	}
}

func TestApplyGentlerStarStretchReducesOnlyStellarIncrement(t *testing.T) {
	p, w, m := syntheticStarTreatmentImage()
	fits, err := FitStarTreatment(context.Background(), m, p, w, 41, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mask, err := BuildStarTreatmentMask(context.Background(), fits, w, 41)
	if err != nil {
		t.Fatal(err)
	}
	meta := models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: w, Height: 41}}, Mode: stretch.Asinh, Background: 0, Peak: 40, ScaledPeak: 100, AsinhScale: 1}
	normal, err := ApplyGentlerStarStretch(context.Background(), p, w, 41, meta, fits, nil, StarStretchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gentle, err := ApplyGentlerStarStretch(context.Background(), p, w, 41, meta, fits, mask, StarStretchOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}
	center := 20*w + 20
	if !(gentle[center] < normal[center]) {
		t.Fatalf("center gentle=%v normal=%v fit=%+v", gentle[center], normal[center], fits[0])
	}
	bg := 0
	if math.Abs(float64(gentle[bg]-normal[bg])) > 1e-6 {
		t.Fatalf("background changed: %v %v", gentle[bg], normal[bg])
	}
	zero, err := ApplyGentlerStarStretch(context.Background(), p, w, 41, meta, fits, mask, StarStretchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range zero {
		if zero[i] != normal[i] {
			t.Fatalf("zero strength changed pixel %d", i)
		}
	}
}

func TestApplyGentlerStarStretchRejectsHistogramEqualization(t *testing.T) {
	_, err := ApplyGentlerStarStretch(context.Background(), make([]float32, 4), 2, 2, models.LoadedImage{Mode: stretch.HistEq}, nil, nil, StarStretchOptions{})
	if err == nil {
		t.Fatal("expected unsupported mode error")
	}
}
