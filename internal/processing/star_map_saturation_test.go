package processing

import (
	"context"
	"math"
	"testing"
)

func TestMosaicSaturationRescueRequiresStellarSpikes(t *testing.T) {
	for _, enhanced := range []bool{false, true} {
		for _, spikes := range []bool{false, true} {
			const w = 193
			p := make([]float32, w*w)
			for y := 0; y < w; y++ {
				for x := 0; x < w; x++ {
					dx, dy := float64(x)-96.2, float64(y)-95.8
					r2 := dx*dx + dy*dy
					v := math.Min(30, 300*math.Pow(1+r2/81, -2.5))
					if spikes {
						u, z := (dx+dy)/math.Sqrt2, (dx-dy)/math.Sqrt2
						v += 15 * (math.Exp(-u*u/1.5) + math.Exp(-z*z/1.5)) * math.Exp(-math.Sqrt(r2)/45)
					}
					if enhanced && r2 < 9 {
						v += 1000
					}
					p[y*w+x] = float32(1 + .0001*float64(x) + v + .005*math.Sin(float64(x*13+y*7)))
				}
			}
			p[96*w+96] = float32(math.NaN()) // A masked core pixel must not destroy the surrounding evidence.
			result, err := DetectStarMap(context.Background(), p, w, w, nil, StarMapOptions{MinSNR: 5, MaxResidual: .36})
			var found []StarMapSource
			if result != nil {
				for _, s := range result.Sources {
					if s.Accepted() && s.Saturated {
						found = append(found, s)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if !spikes && len(found) > 0 {
				t.Fatalf("rescued spike-free broad knot: %+v", found)
			}
			if spikes {
				if len(found) != 1 {
					t.Fatalf("wanted one saturated star: %+v", found)
				}
				s := found[0]
				if !s.Accepted() || !s.Saturated || math.Hypot(s.X-96.2, s.Y-95.8) > 2 || s.Radius < 10 {
					t.Fatalf("bad saturated footprint: %+v", s)
				}
			}
		}
	}
}
func TestMosaicSaturationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := mosaicSaturatedStars(ctx, make([]float32, 100*100), 100, 100, DefaultStarMapOptions())
	if err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestMosaicSaturationRejectsInvalidFlatAndEdgeFields(t *testing.T) {
	for _, kind := range []string{"invalid", "flat", "edge"} {
		p := make([]float32, 97*97)
		for i := range p {
			p[i] = 1
			if kind == "invalid" {
				p[i] = float32(math.NaN())
			}
		}
		if kind == "edge" {
			for y := 0; y < 8; y++ {
				for x := 0; x < 8; x++ {
					p[y*97+x] = 1000
				}
			}
		}
		found, err := mosaicSaturatedStars(context.Background(), p, 97, 97, DefaultStarMapOptions())
		if err != nil || len(found) != 0 {
			t.Fatalf("%s: %+v %v", kind, found, err)
		}
	}
}
