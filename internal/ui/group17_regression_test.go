package ui

import (
	"math"
	"testing"

	"gofitsv3/internal/mosaic"
)

func TestGroup17FiniteDialogValidators(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), 0, -1} {
		if finitePositive(v) {
			t.Fatalf("finitePositive(%v) accepted", v)
		}
	}
	if !finitePositive(1) || !finiteUnit(1) || finiteUnit(1.1) {
		t.Fatal("finite numeric validators mismatch")
	}
	if finitePositiveSky(math.NaN()) || finitePositiveSky(math.Inf(-1)) || !finitePositiveSky(2) {
		t.Fatal("sky validator mismatch")
	}
}

func TestGroup17ExposureIdentityRejectsReplacement(t *testing.T) {
	inputs := []mosaic.Input{{Path: "a.fits"}, {Path: "b.fits"}}
	snapshot := []string{exposureInputIdentity(inputs[0]), exposureInputIdentity(inputs[1])}
	if !sameExposureInputIdentities(inputs, snapshot) {
		t.Fatal("matching identities rejected")
	}
	inputs[1].Path = "replacement.fits"
	if sameExposureInputIdentities(inputs, snapshot) {
		t.Fatal("replacement identity accepted")
	}
}

func TestGroup17MeasureOverlapForcesManualSelection(t *testing.T) {
	a := map[string]bool{"shared.fits": true}
	b := map[string]bool{"shared.fits": true}
	if mover, ok := measureAutoSelection(a, b, map[string]int{"shared.fits": 1}); ok || mover != nil {
		t.Fatal("overlap should force manual selection")
	}
}
