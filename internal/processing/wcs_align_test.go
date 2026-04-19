package processing

import (
	"math"
	"strconv"
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

func TestSIPPixelWorldRoundTrip(t *testing.T) {
	header := fitsio.Header{Cards: map[string]string{
		"CRPIX1":  "50.0",
		"CRPIX2":  "60.0",
		"CRVAL1":  "100.0",
		"CRVAL2":  "22.0",
		"CD1_1":   "1.0",
		"CD1_2":   "0.0",
		"CD2_1":   "0.0",
		"CD2_2":   "1.0",
		"A_ORDER": "2",
		"B_ORDER": "2",
		"A_2_0":   "1e-5",
		"A_1_1":   "-2e-5",
		"B_0_2":   "-1.5e-5",
		"B_1_1":   "1e-5",
	}}
	w, err := parseLinearWCS(header)
	if err != nil {
		t.Fatalf("parseLinearWCS returned error: %v", err)
	}

	ra, dec := pixelToWorldLinear(17.25, 23.75, w)
	x, y, err := worldToPixelLinear(ra, dec, w)
	if err != nil {
		t.Fatalf("worldToPixelLinear returned error: %v", err)
	}
	if math.Abs(x-17.25) > 1e-4 || math.Abs(y-23.75) > 1e-4 {
		t.Fatalf("roundtrip = (%.6f, %.6f), want (17.25, 23.75)", x, y)
	}
}

func TestComputeWCSTransformPreservesTranslationWithMatchingSIP(t *testing.T) {
	header := func(crpix1, crpix2 float64) fitsio.Header {
		return fitsio.Header{Cards: map[string]string{
			"CRPIX1":  strconv.FormatFloat(crpix1, 'f', -1, 64),
			"CRPIX2":  strconv.FormatFloat(crpix2, 'f', -1, 64),
			"CRVAL1":  "100.0",
			"CRVAL2":  "22.0",
			"CD1_1":   "1.0",
			"CD1_2":   "0.0",
			"CD2_1":   "0.0",
			"CD2_2":   "1.0",
			"A_ORDER": "2",
			"B_ORDER": "2",
			"A_2_0":   "1e-5",
			"A_1_1":   "-2e-5",
			"B_0_2":   "-1.5e-5",
			"B_1_1":   "1e-5",
		}}
	}

	transform, err := ComputeWCSTransform(header(12, 8), header(10, 5))
	if err != nil {
		t.Fatalf("ComputeWCSTransform returned error: %v", err)
	}
	if math.Abs(transform.C-2) > 1e-3 || math.Abs(transform.F-3) > 1e-3 {
		t.Fatalf("expected translation (2,3), got C=%v F=%v", transform.C, transform.F)
	}
	if math.Abs(transform.A-1) > 1e-3 || math.Abs(transform.E-1) > 1e-3 || math.Abs(transform.B) > 1e-3 || math.Abs(transform.D) > 1e-3 {
		t.Fatalf("expected near-identity linear terms, got %+v", transform)
	}
}
