package processing

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"testing"
)

func starMapFixture(width float64, saturated bool) ([]float32, []bool) {
	const w = 97
	p := make([]float32, w*w)
	sat := make([]bool, len(p))
	rng := rand.New(rand.NewSource(41))
	alpha := width / (2 * math.Sqrt(math.Pow(2, 1/2.5)-1))
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			r2 := math.Pow(float64(x)-48.2, 2) + math.Pow(float64(y)-47.8, 2)
			v := 100 * math.Pow(1+r2/(alpha*alpha), -2.5)
			p[y*w+x] = float32(20 + .03*float64(x) + .02*float64(y) + v + .05*rng.NormFloat64())
			if saturated && v > 15 {
				sat[y*w+x] = true
				p[y*w+x] = 35
			}
		}
	}
	return p, sat
}
func TestStarMapFindsPointSourceOnSlopingBackground(t *testing.T) {
	p, _ := starMapFixture(2.6, false)
	m, err := DetectStarMap(context.Background(), p, 97, 97, nil, DefaultStarMapOptions())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range m.Sources {
		if s.Accepted() {
			n++
			if math.Hypot(s.X-48.2, s.Y-47.8) > .4 {
				t.Errorf("centroid %v,%v", s.X, s.Y)
			}
		}
	}
	if n != 1 {
		t.Fatalf("accepted %d: %+v", n, m.Sources)
	}
}
func TestStarMapRejectsExtendedKnotAndHotPixel(t *testing.T) {
	for _, kind := range []string{"extended", "hot", "ridge"} {
		t.Run(kind, func(t *testing.T) {
			p, _ := starMapFixture(7, false)
			if kind == "hot" {
				for i := range p {
					p[i] = 0
				}
				p[48*97+48] = 100
			}
			if kind == "ridge" {
				for y := 0; y < 97; y++ {
					for x := 0; x < 97; x++ {
						p[y*97+x] = float32(100 * math.Exp(-math.Pow(float64(y-48)/2, 2)) * (1 + .1*math.Cos(float64(x)/12)))
					}
				}
			}
			m, err := DetectStarMap(context.Background(), p, 97, 97, nil, DefaultStarMapOptions())
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range m.Sources {
				if s.Accepted() {
					t.Fatalf("accepted %s: %+v", kind, s)
				}
			}
		})
	}
}
func TestStarMapSaturatedWingsSurviveClippedCore(t *testing.T) {
	p, sat := starMapFixture(2.6, true)
	m, err := DetectStarMap(context.Background(), p, 97, 97, sat, DefaultStarMapOptions())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range m.Sources {
		if s.Accepted() && s.Saturated && math.Hypot(s.X-48.2, s.Y-47.8) < 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("saturated star missed: %+v", m.Sources)
	}
}
func TestStarMapNoFalseSelectionOnSaturatedExtendedSource(t *testing.T) {
	p, sat := starMapFixture(8, true)
	m, err := DetectStarMap(context.Background(), p, 97, 97, sat, DefaultStarMapOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range m.Sources {
		if s.Accepted() {
			t.Fatalf("extended saturated knot accepted: %+v", s)
		}
	}
}
func TestStarMapRasterizeProtectsNaNsAndHonorsOverrides(t *testing.T) {
	p := make([]float32, 31*31)
	p[15*31+15] = float32(math.NaN())
	m := &StarMap{Width: 31, Height: 31, Sources: []StarMapSource{{ID: 1, X: 15, Y: 15, Radius: 4, Status: "uncertain"}}}
	a, _, f, err := m.Rasterize(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range a {
		if v != 0 {
			t.Fatal("uncertain source selected")
		}
	}
	if f[15*31+15] != 1 {
		t.Fatal("lost coverage flag")
	}
	m.Sources[0].Override = "accept"
	a, l, f, err := m.Rasterize(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if a[15*31+15] != 0 || a[15*31+16] != 1 || l[15*31+16] != 1 || f[15*31+16]&16 == 0 {
		t.Fatal("override or invalid coverage lost")
	}
	if a[15*31+20] != 0 {
		t.Fatal("footprint leaked")
	}
}
func TestStarMapCancellationAndInvalidInputs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, _ := starMapFixture(2.6, false)
	if _, err := DetectStarMap(ctx, p, 97, 97, nil, DefaultStarMapOptions()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := DetectStarMap(context.Background(), p, 97, 97, []bool{true}, DefaultStarMapOptions()); err == nil {
		t.Fatal("invalid saturation accepted")
	}
	for i := range p {
		p[i] = float32(math.NaN())
	}
	if _, err := DetectStarMap(context.Background(), p, 97, 97, nil, DefaultStarMapOptions()); err == nil {
		t.Fatal("all invalid image accepted")
	}
}
