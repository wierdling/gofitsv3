package starbench

import (
	"math"
	"testing"

	"gofitsv3/internal/processing"
)

func fixture() (Project, Run, []float32) {
	p := Project{Version: SchemaVersion, DatasetHash: "science", Regions: []Region{{ID: "r", Name: "Test", Width: 64, Height: 64, Split: "development", Reviewed: true}}}
	return p, Run{ID: "baseline", DatasetHash: p.DatasetHash}, make([]float32, 64*64)
}
func star(id int, x, y float64) processing.StarMapSource {
	return processing.StarMapSource{ID: id, X: x, Y: y, Radius: 3, Status: "accepted"}
}
func label(id, kind string, x, y float64) Label {
	return Label{ID: id, RegionID: "r", Kind: kind, X: x, Y: y}
}

func TestEvaluateClosePairUsesOneToOneMatching(t *testing.T) {
	p, r, pixels := fixture()
	p.Labels = []Label{label("a", "star", 10, 10), label("b", "star", 12, 10)}
	r.Sources = []processing.StarMapSource{star(1, 11, 10), star(2, 8.5, 10)}
	report, err := Evaluate(p, r, pixels, 64, 64, 2)
	if err != nil {
		t.Fatal(err)
	}
	c := report.Splits["development"]
	if c.TruePositive != 2 || c.FalseNegative != 0 || c.FalsePositive != 0 {
		t.Fatalf("close-pair assignment: %+v", c)
	}
	r.Sources = append(r.Sources, star(3, 10, 10))
	report, err = Evaluate(p, r, pixels, 64, 64, 2)
	if err != nil {
		t.Fatal(err)
	}
	c = report.Splits["development"]
	if c.TruePositive != 2 || c.FalsePositive != 1 {
		t.Fatalf("duplicate detection must be FP: %+v", c)
	}
}
func TestEvaluateUnresolvedMissedAndManualOverrides(t *testing.T) {
	p, r, pixels := fixture()
	p.Labels = []Label{label("found", "star", 10, 10), label("missed", "star", 20, 20), label("unknown", "ambiguous", 30, 30), label("bad", "nonstar", 40, 40)}
	p.Labels[0].Tags = []string{"bright", "on-nebula"}
	p.Labels[1].Tags = []string{"saturated"}
	r.Sources = []processing.StarMapSource{star(1, 10, 10), star(2, 30, 30), star(3, 40, 40), star(4, 50, 50), star(5, 20, 20)}
	r.Sources[0].Override = "reject"
	r.Sources[4].Status = "uncertain"
	r.Sources[4].Override = "accept"
	report, err := Evaluate(p, r, pixels, 64, 64, 2)
	if err != nil {
		t.Fatal(err)
	}
	c := report.Splits["development"]
	if c.TruePositive != 1 || c.FalsePositive != 2 || c.Unresolved != 1 || c.FalseNegative != 1 || c.Selected != 4 {
		t.Fatalf("automatic classifications: %+v", c)
	}
	if *c.ConservativePrecision != .25 || *c.Recall != .5 || math.Abs(*c.CertainPrecision-1./3) > 1e-12 {
		t.Fatalf("incorrect denominators: %+v", c)
	}
	for _, v := range report.Strata["development"] {
		if v.Tag == "saturated" && (v.Truth != 1 || v.Recovered != 0) {
			t.Fatalf("saturated misses lost: %+v", v)
		}
	}
}
func TestEvaluateIncompleteAndBoundaryLabelsAreExcluded(t *testing.T) {
	p, r, pixels := fixture()
	p.Regions[0].Reviewed = false
	p.Labels = []Label{label("edge", "star", 1, 10)}
	r.Sources = []processing.StarMapSource{star(1, 1, 10)}
	report, err := Evaluate(p, r, pixels, 64, 64, 2)
	if err != nil {
		t.Fatal(err)
	}
	if report.IncompleteRegions != 1 || report.Splits["development"].Recall != nil || len(report.Regions) != 0 {
		t.Fatalf("incomplete region was scored: %+v", report)
	}
	p.Regions[0].Reviewed = true
	report, err = Evaluate(p, r, pixels, 64, 64, 2)
	if err != nil {
		t.Fatal(err)
	}
	if report.Regions[0].IgnoredBoundaryLabels != 1 || report.Splits["development"].Selected != 0 {
		t.Fatal("boundary was not excluded symmetrically")
	}
}
func TestEvaluateProtectedUnionIncludesExternalFootprintAndSkipsNaN(t *testing.T) {
	p, r, pixels := fixture()
	p.Regions[0].X = 10
	p.Regions[0].Y = 10
	p.Regions[0].Width = 20
	p.Regions[0].Height = 20
	p.Protected = []Protected{{ID: "a", RegionID: "r", X: 10, Y: 14, Width: 2, Height: 3}, {ID: "b", RegionID: "r", X: 10, Y: 14, Width: 2, Height: 3}}
	r.Sources = []processing.StarMapSource{star(1, 9, 15)}
	r.Sources[0].Radius = 4
	pixels[14*64+10] = float32(math.NaN())
	report, err := Evaluate(p, r, pixels, 64, 64, 2)
	if err != nil {
		t.Fatal(err)
	}
	rr := report.Regions[0]
	if rr.Counts.Selected != 0 || rr.ProtectedPixels != 5 || rr.TouchedPixels != 5 || rr.MaskWeight != 5 {
		t.Fatalf("protected union / outside footprint: %+v", rr)
	}
}
func TestEvaluateRejectsInvalidData(t *testing.T) {
	for _, name := range []string{"hash", "region", "radius", "source", "dimensions"} {
		t.Run(name, func(t *testing.T) {
			p, r, pixels := fixture()
			tol := 2.
			switch name {
			case "hash":
				r.DatasetHash = "other"
			case "region":
				p.Protected = []Protected{{ID: "bad", RegionID: "r", X: -1, Width: 10, Height: 10}}
			case "radius":
				tol = math.NaN()
			case "source":
				r.Sources = []processing.StarMapSource{star(1, math.NaN(), 0)}
			case "dimensions":
				pixels = pixels[:1]
			}
			if _, err := Evaluate(p, r, pixels, 64, 64, tol); err == nil {
				t.Fatal("invalid data accepted")
			}
		})
	}
}
