package processing

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestAlignChannelUsingWCSTranslatesByCRPIXOffset(t *testing.T) {
	targetHeader := fitsio.Header{Cards: map[string]string{
		"CRPIX1": "12.0 / ref x",
		"CRPIX2": "8.0 / ref y",
		"CRVAL1": "100.0 / ra",
		"CRVAL2": "22.0 / dec",
		"CD1_1":  "1.0 / dx",
		"CD1_2":  "0.0 / dxdy",
		"CD2_1":  "0.0 / dydx",
		"CD2_2":  "1.0 / dy",
	}}
	refHeader := fitsio.Header{Cards: map[string]string{
		"CRPIX1": "10.0 / ref x",
		"CRPIX2": "5.0 / ref y",
		"CRVAL1": "100.0 / ra",
		"CRVAL2": "22.0 / dec",
		"CD1_1":  "1.0 / dx",
		"CD1_2":  "0.0 / dxdy",
		"CD2_1":  "0.0 / dydx",
		"CD2_2":  "1.0 / dy",
	}}

	target := make([]float32, 20*20)
	ref := make([]float32, 20*20)
	_, transform, err := AlignChannelUsingWCS(target, 20, 20, targetHeader, ref, 20, 20, refHeader)
	if err != nil {
		t.Fatalf("AlignChannelUsingWCS returned error: %v", err)
	}

	if math.Abs(transform.C-2) > 1e-6 || math.Abs(transform.F-3) > 1e-6 {
		t.Fatalf("expected translation (2,3), got C=%v F=%v", transform.C, transform.F)
	}
	if math.Abs(transform.A-1) > 1e-6 || math.Abs(transform.E-1) > 1e-6 || math.Abs(transform.B) > 1e-6 || math.Abs(transform.D) > 1e-6 {
		t.Fatalf("expected identity linear terms, got %+v", transform)
	}
}
