package gaia

import "testing"

func TestSourceAndSpectrumValidation(t *testing.T) {
	s := Source{Release: "DR3", SourceID: 1, RA: 10, Dec: -2, ReferenceEpoch: 2016, PositionError: .1, ProperMotionErrorRA: .1, ProperMotionErrorDec: .1, G: 12, BP: 12.5, RP: 11.5, GError: .01, BPError: .02, RPError: .02}
	if err := s.Validate("DR3"); err != nil {
		t.Fatal(err)
	}
	s.RA = 360
	if err := s.Validate("DR3"); err == nil {
		t.Fatal("invalid coordinate accepted")
	}
	x := XPSpectrum{Release: "DR3", SourceID: 1, RepresentationVersion: "xp-v1", Wavelengths: []float64{400, 500}, Flux: []float64{1, 2}, FluxErrors: []float64{.1, .1}}
	if err := x.Validate("DR3", "xp-v1"); err != nil {
		t.Fatal(err)
	}
	x.FluxErrors = nil
	if err := x.Validate("DR3", "xp-v1"); err == nil {
		t.Fatal("incomplete XP accepted")
	}
}

func TestNormalizeSourcesSortsAndRejectsDuplicates(t *testing.T) {
	base := func(id uint64) Source {
		return Source{Release: "DR3", SourceID: id, RA: 10, Dec: 1, ReferenceEpoch: 2016, PositionError: .1, ProperMotionErrorRA: .1, ProperMotionErrorDec: .1, G: 12, BP: 12, RP: 12, GError: .1, BPError: .1, RPError: .1}
	}
	got, err := NormalizeSources([]Source{base(4), base(2)}, "DR3")
	if err != nil || got[0].SourceID != 2 {
		t.Fatalf("normalized sources = %+v, err=%v", got, err)
	}
	if _, err := NormalizeSources([]Source{base(2), base(2)}, "DR3"); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestFootprintValidation(t *testing.T) {
	f := Footprint{Center: Coordinate{RA: 1, Dec: 2}, RadiusDeg: 1}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	f.RadiusDeg = 0
	if err := f.Validate(); err == nil {
		t.Fatal("empty footprint accepted")
	}
}
