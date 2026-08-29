package mosaic

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

func TestAlignInputsBySelectedStarsWithModeCtxHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := AlignInputsBySelectedStarsWithModeCtx(ctx, nil, []processing.Star{{X: 1, Y: 1}}, 1, AlignmentModeRScale, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestBuildCancellationDuringOnlyDrizzleReturnsNoResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := func(stage string, done, total int) {
		if stage == "Drizzling" && done == 0 {
			cancel()
		}
	}
	result, err := Build([]Input{
		makeInput("ref_flc.fits", 2, 2, []float32{1, 2, 3, 4}, headerWithCRPIX(10, 10)),
	}, Options{Scale: 1, Ctx: ctx, Progress: progress})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("Build error = %v, want ErrCancelled", err)
	}
	if result != nil {
		t.Fatal("cancelled Build returned a partial result")
	}
}

func TestPlanInputsMalformedWCSBookkeeping(t *testing.T) {
	refBad := makeInput("ref_bad.fits", 2, 2, filledPixels(2, 2, 1), headerWithCRPIX(10, 10))
	refBad.HDU.Header.Cards["CRPIX1"] = "not-a-number"
	_, statuses, _, _, _, _, err := planInputs([]Input{refBad}, 1)
	if err == nil || statuses[0].Status != "failed" || statuses[0].Error == "" {
		t.Fatalf("malformed reference: err=%v status=%+v, want failure diagnostics", err, statuses[0])
	}

	ref := makeInput("ref.fits", 2, 2, filledPixels(2, 2, 1), headerWithCRPIX(10, 10))
	targetBad := makeInput("target_bad.fits", 2, 2, filledPixels(2, 2, 2), headerWithCRPIX(10, 10))
	targetBad.HDU.Header.Cards["CRPIX2"] = "not-a-number"
	planned, statuses, _, _, _, _, err := planInputs([]Input{ref, targetBad}, 1)
	if err != nil {
		t.Fatalf("malformed non-reference should retain reference: %v", err)
	}
	if len(planned) != 1 || !statuses[0].Included {
		t.Fatalf("planned=%d reference included=%v, want only reference planned", len(planned), statuses[0].Included)
	}
	if statuses[1].Status != "failed" || statuses[1].Included || statuses[1].Error == "" {
		t.Fatalf("malformed target status=%+v, want failed/excluded diagnostics", statuses[1])
	}
}

func TestBuildSingleImageIdentity(t *testing.T) {
	inputs := []Input{
		makeInput("ref_flc.fits", 2, 2, []float32{1, 2, 3, 4}, headerWithCRPIX(10, 10)),
	}

	result, err := Build(inputs, Options{Scale: 1})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if result.Width != 2 || result.Height != 2 {
		t.Fatalf("result size = %dx%d, want 2x2", result.Width, result.Height)
	}
	for i, want := range []float32{1, 2, 3, 4} {
		if math.Abs(float64(result.Pixels[i]-want)) > 1e-6 {
			t.Fatalf("pixel[%d] = %v, want %v", i, result.Pixels[i], want)
		}
		if math.Abs(float64(result.Weights[i]-1)) > 1e-6 {
			t.Fatalf("weight[%d] = %v, want 1", i, result.Weights[i])
		}
	}
}

func TestBuildExpandsCanvasForTranslatedInput(t *testing.T) {
	inputs := []Input{
		makeInput("ref_flc.fits", 4, 4, filledPixels(4, 4, 1), headerWithCRPIX(10, 10)),
		makeInput("shifted_flc.fits", 4, 4, filledPixels(4, 4, 2), headerWithCRPIX(8, 10)),
	}

	result, err := Build(inputs, Options{Scale: 1})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if result.Width != 6 || result.Height != 4 {
		t.Fatalf("result size = %dx%d, want 6x4", result.Width, result.Height)
	}
	if result.OriginX != 0 || result.OriginY != 0 {
		t.Fatalf("origin = (%v,%v), want (0,0)", result.OriginX, result.OriginY)
	}
}

func TestBuildAveragesOverlappingInputs(t *testing.T) {
	inputs := []Input{
		makeInput("ref_flc.fits", 3, 3, filledPixels(3, 3, 2), headerWithCRPIX(10, 10)),
		makeInput("other_flc.fits", 3, 3, filledPixels(3, 3, 4), headerWithCRPIX(10, 10)),
	}

	result, err := Build(inputs, Options{Scale: 1})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	for i := range result.Pixels {
		if math.Abs(float64(result.Pixels[i]-3)) > 1e-6 {
			t.Fatalf("pixel[%d] = %v, want 3", i, result.Pixels[i])
		}
		if math.Abs(float64(result.Weights[i]-2)) > 1e-6 {
			t.Fatalf("weight[%d] = %v, want 2", i, result.Weights[i])
		}
	}
}

func TestBuildExposureWeightingNormalizesToRate(t *testing.T) {
	ref := makeInput("ref_flc.fits", 2, 2, filledPixels(2, 2, 100), headerWithCRPIX(10, 10))
	ref.ExposureTime = 100
	ref.PrimaryHeader.Cards["EXPTIME"] = "100"
	other := makeInput("other_flc.fits", 2, 2, filledPixels(2, 2, 110), headerWithCRPIX(10, 10))
	other.ExposureTime = 110
	other.PrimaryHeader.Cards["EXPTIME"] = "110"

	result, err := Build([]Input{ref, other}, Options{Scale: 1, WeightingMode: WeightExposure})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	for i := range result.Pixels {
		if math.Abs(float64(result.Pixels[i]-1)) > 1e-6 {
			t.Fatalf("pixel[%d] = %v, want 1", i, result.Pixels[i])
		}
		if math.Abs(float64(result.Weights[i]-210)) > 1e-6 {
			t.Fatalf("weight[%d] = %v, want 210", i, result.Weights[i])
		}
	}
}

func TestBuildExposureWeightingPreservesCalibratedFlux(t *testing.T) {
	// JWST-style calibrated frames (MJy/sr) are already absolute flux, not
	// counts, so Exposure/ERR weighting must NOT divide them by EXPTIME the way
	// it does for count data (see TestBuildExposureWeightingNormalizesToRate).
	ref := makeInput("ref_cal.fits", 2, 2, filledPixels(2, 2, 100), headerWithCRPIX(10, 10))
	ref.ExposureTime = 100
	ref.PrimaryHeader.Cards["EXPTIME"] = "100"
	ref.BUnit = "MJy/sr"
	other := makeInput("other_cal.fits", 2, 2, filledPixels(2, 2, 100), headerWithCRPIX(10, 10))
	other.ExposureTime = 110
	other.PrimaryHeader.Cards["EXPTIME"] = "110"
	other.BUnit = "MJy/sr"

	result, err := Build([]Input{ref, other}, Options{Scale: 1, WeightingMode: WeightExposure})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	for i := range result.Pixels {
		if math.Abs(float64(result.Pixels[i]-100)) > 1e-4 {
			t.Fatalf("pixel[%d] = %v, want 100 (calibrated flux must not be divided by EXPTIME)", i, result.Pixels[i])
		}
	}
}

func TestBuildExposureWeightingWithDrizzleCRKeepsValidPixels(t *testing.T) {
	ref := makeInput("ref_flc.fits", 2, 2, []float32{100, 110, 90, 100}, headerWithCRPIX(10, 10))
	ref.ExposureTime = 100
	ref.PrimaryHeader.Cards["EXPTIME"] = "100"
	other := makeInput("other_flc.fits", 2, 2, []float32{110, 121, 99, 110}, headerWithCRPIX(10, 10))
	other.ExposureTime = 110
	other.PrimaryHeader.Cards["EXPTIME"] = "110"

	result, err := Build([]Input{ref, other}, Options{Scale: 1, WeightingMode: WeightExposure, CRMethod: CRMethodDrizzle})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	want := []float32{1, 1.1, 0.9, 1}
	for i, w := range want {
		if math.IsNaN(float64(result.Pixels[i])) {
			t.Fatalf("pixel[%d] is NaN, want %v", i, w)
		}
		if math.Abs(float64(result.Pixels[i]-w)) > 1e-5 {
			t.Fatalf("pixel[%d] = %v, want %v", i, result.Pixels[i], w)
		}
	}
}

func TestBuildSkipsNaNPixels(t *testing.T) {
	inputs := []Input{
		makeInput("ref_flc.fits", 2, 2, []float32{1, 1, 1, 1}, headerWithCRPIX(10, 10)),
		makeInput("nan_flc.fits", 2, 2, []float32{float32(math.NaN()), 3, 3, 3}, headerWithCRPIX(10, 10)),
	}

	result, err := Build(inputs, Options{Scale: 1})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if math.Abs(float64(result.Pixels[0]-1)) > 1e-6 {
		t.Fatalf("pixel[0] = %v, want 1", result.Pixels[0])
	}
	if math.Abs(float64(result.Weights[0]-1)) > 1e-6 {
		t.Fatalf("weight[0] = %v, want 1", result.Weights[0])
	}
	for _, idx := range []int{1, 2, 3} {
		if math.Abs(float64(result.Pixels[idx]-2)) > 1e-6 {
			t.Fatalf("pixel[%d] = %v, want 2", idx, result.Pixels[idx])
		}
	}
}

func TestBuildMarksBadAlignmentAndKeepsReference(t *testing.T) {
	inputs := []Input{
		makeInput("ref_flc.fits", 3, 3, filledPixels(3, 3, 5), headerWithCRPIX(10, 10)),
		makeInput("bad_flc.fits", 3, 3, filledPixels(3, 3, 9), fitsio.Header{Cards: map[string]string{}}),
	}

	result, err := Build(inputs, Options{Scale: 1})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(result.Inputs) != 2 {
		t.Fatalf("len(result.Inputs) = %d, want 2", len(result.Inputs))
	}
	if !result.Inputs[0].Included {
		t.Fatalf("reference input should be included")
	}
	if result.Inputs[1].Included {
		t.Fatalf("bad input should not be included")
	}
	if result.Inputs[1].Status != "failed" {
		t.Fatalf("bad input status = %q, want failed", result.Inputs[1].Status)
	}
}

func TestBuildUpdatesOutputWCSForExpandedScaledCanvas(t *testing.T) {
	inputs := []Input{
		makeInput("ref_flc.fits", 4, 4, filledPixels(4, 4, 1), headerWithCRPIX(10, 12)),
		makeInput("shifted_flc.fits", 4, 4, filledPixels(4, 4, 2), headerWithCRPIX(8, 10)),
	}

	result, err := Build(inputs, Options{Scale: 2})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got := fitsio.HeaderString(result.OutputHeader, "CRPIX1"); got != "19" {
		t.Fatalf("CRPIX1 = %q, want 19", got)
	}
	if got := fitsio.HeaderString(result.OutputHeader, "CRPIX2"); got != "23" {
		t.Fatalf("CRPIX2 = %q, want 23", got)
	}
	if got := fitsio.HeaderString(result.OutputHeader, "CD1_1"); got != "0.5" {
		t.Fatalf("CD1_1 = %q, want 0.5", got)
	}
	if got := fitsio.HeaderString(result.OutputHeader, "CD2_2"); got != "0.5" {
		t.Fatalf("CD2_2 = %q, want 0.5", got)
	}
}

func TestSaveResultFITSRoundTrip(t *testing.T) {
	result, err := Build([]Input{
		makeInput("ref_flc.fits", 3, 2, []float32{1, 2, 3, 4, 5, 6}, headerWithCRPIX(10, 10)),
	}, Options{Scale: 1})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "out.fits")
	if err := SaveResultFITS(path, result); err != nil {
		t.Fatalf("SaveResultFITS returned error: %v", err)
	}
	loaded, err := fitsio.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile returned error: %v", err)
	}
	if len(loaded.HDUs) == 0 {
		t.Fatalf("expected saved FITS to contain an HDU")
	}
	if loaded.HDUs[0].Data.Width != 3 || loaded.HDUs[0].Data.Height != 2 {
		t.Fatalf("saved size = %dx%d, want 3x2", loaded.HDUs[0].Data.Width, loaded.HDUs[0].Data.Height)
	}
	if got := fitsio.HeaderString(loaded.HDUs[0].Header, "NCOMBINE"); got != "1" {
		t.Fatalf("NCOMBINE = %q, want 1", got)
	}
	for _, key := range []string{"CRVAL1", "CRVAL2", "CRPIX1", "CRPIX2", "CD1_1", "CD1_2", "CD2_1", "CD2_2", "CTYPE1", "CTYPE2", "DATE-OBS"} {
		want := fitsio.HeaderString(result.OutputHeader, key)
		if got := fitsio.HeaderString(loaded.HDUs[0].Header, key); got != want {
			t.Fatalf("saved %s = %q, want output header value %q", key, got, want)
		}
	}
}

func TestSaveResultFITSWritesOptionalDiagnosticProducts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostic.fits")
	result := &Result{
		Pixels: []float32{1, 2, 3, 4}, Width: 2, Height: 2,
		OutputHeader: fitsio.Header{Cards: map[string]string{}},
		Weights:      []float32{1, 2, 3, 4}, DiagnosticProducts: true,
		NContrib: []int32{1, 2, 1, 0}, Context: []uint32{1, 3, 2, 0}, ContextPlanes: [][]uint32{{1, 3, 2, 0}, {0, 0, 1, 0}},
		CRMask: []int32{0, 1, 0, 0}, DQ: []int32{0, 0, 2, 0},
		SkyModel: []float32{0.5, 0.5, 0.5, 0.5}, Seam: []float32{0, 0, 1, 0},
	}
	if err := SaveResultFITS(path, result); err != nil {
		t.Fatalf("SaveResultFITS: %v", err)
	}
	file, err := fitsio.LoadFile(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := []string{"WHT", "NCONTRIB", "CTX", "CTX01", "CTX02", "CRMASK", "DQ", "SKYMODEL", "SEAM"}
	for _, name := range want {
		if file.GetHDU(name) == nil {
			t.Errorf("missing diagnostic extension %s", name)
		}
	}
}

func TestSaveResultFITSLeavesLegacyOutputWithoutDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.fits")
	result := &Result{Pixels: []float32{1}, Width: 1, Height: 1, Weights: []float32{1}, OutputHeader: fitsio.Header{Cards: map[string]string{}}}
	if err := SaveResultFITS(path, result); err != nil {
		t.Fatalf("SaveResultFITS: %v", err)
	}
	file, err := fitsio.LoadFile(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if file.GetHDU("WHT") != nil {
		t.Fatal("legacy result unexpectedly contains WHT")
	}
}

func TestLooksLikeFLC(t *testing.T) {
	if !LooksLikeFLC(filepath.Join("TestImages", "HST", "ick909c1q_flc.fits")) {
		t.Fatalf("expected _flc path to be recognized")
	}
	if !LooksLikeFLC(filepath.Join("TestImages", "WFPC2", "u6l60101m_flt.fits")) {
		t.Fatalf("expected _flt path to be recognized")
	}
	if LooksLikeFLC(filepath.Join("TestImages", "HST", "ick909030_drz.fits")) {
		t.Fatalf("did not expect _drz path to be recognized as calibrated input")
	}
}

func TestEffectiveEdgeTrimForInputIsDisabled(t *testing.T) {
	// Edge trimming is disabled: no input loses border data during drizzle,
	// regardless of detector or scale.
	p := plannedInput{input: Input{
		PrimaryHeader: fitsio.Header{Cards: map[string]string{
			"INSTRUME": "'WFPC2'",
			"DETECTOR": "'PC'",
		}},
		HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 800, Height: 800}},
	}}
	if got := effectiveEdgeTrimForInput(p, 800, 1); got != 0 {
		t.Fatalf("effectiveEdgeTrimForInput(WFPC2) = %d, want 0", got)
	}

	p.input.PrimaryHeader = fitsio.Header{Cards: map[string]string{
		"INSTRUME": "'WFC3'",
		"DETECTOR": "'UVIS'",
	}}
	if got := effectiveEdgeTrimForInput(p, 800, 2); got != 0 {
		t.Fatalf("effectiveEdgeTrimForInput(WFC3/UVIS) = %d, want 0", got)
	}
}

func TestNormalizeSurfaceBrightnessInputsScalesSCIAndERRByMappedArea(t *testing.T) {
	planned := []plannedInput{
		{
			sourcePixelScale: 2,
			input: Input{
				HDU:       fitsio.HDU{Data: fitsio.ImageData{Width: 2, Height: 1, Pixels: []float32{8, float32(math.NaN())}}},
				ERRPixels: []float32{4, 8},
			},
		},
	}
	sci, errPix, _, err := prepareFramePixels(planned[0], Options{SurfaceBrightnessNorm: true}, 0, skyPlane{})
	if err != nil {
		t.Fatalf("prepareFramePixels returned error: %v", err)
	}
	if got := sci[0]; got != 2 {
		t.Fatalf("normalized SCI pixel = %v, want 2", got)
	}
	if !math.IsNaN(float64(sci[1])) {
		t.Fatalf("normalized SCI NaN changed to %v", sci[1])
	}
	if got := errPix[0]; got != 1 {
		t.Fatalf("normalized ERR pixel = %v, want 1", got)
	}
	if planned[0].input.HDU.Data.Pixels[0] != 8 || planned[0].input.ERRPixels[0] != 4 {
		t.Fatal("prepareFramePixels mutated original input")
	}
}

func TestDrizzlePixelWeightPrefersWeightPixels(t *testing.T) {
	// ExposureTime 0 so the ERR fallback returns 1/(e*e) directly (the rate
	// conversion branch is gated on exptime > 0).
	p := plannedInput{input: Input{ERRPixels: []float32{2, 4}}}

	// A present, positive WeightPixels value is used verbatim.
	p.input.WeightPixels = []float32{5, 0}
	if got := drizzlePixelWeight(p, 0, WeightERR); got != 5 {
		t.Fatalf("weight[0] with WeightPixels = %v, want 5", got)
	}
	// A zero WeightPixels value falls through to the ERR-derived weight.
	if got := drizzlePixelWeight(p, 1, WeightERR); got != 1.0/16.0 {
		t.Fatalf("weight[1] zero-WHT fallback = %v, want %v", got, 1.0/16.0)
	}
	// A non-finite WeightPixels value also falls through.
	p.input.WeightPixels = []float32{float32(math.NaN()), 4}
	if got := drizzlePixelWeight(p, 0, WeightERR); got != 1.0/4.0 {
		t.Fatalf("weight[0] NaN-WHT fallback = %v, want %v", got, 1.0/4.0)
	}
	// Nil WeightPixels leaves the pure-ERR behavior unchanged.
	p.input.WeightPixels = nil
	if got := drizzlePixelWeight(p, 0, WeightERR); got != 1.0/4.0 {
		t.Fatalf("weight[0] nil WeightPixels = %v, want %v", got, 1.0/4.0)
	}
}

func TestPrepareFramePixelsScalesWeightPixels(t *testing.T) {
	planned := []plannedInput{
		{
			sourcePixelScale: 2, // area 4 → sbScale 0.25 → inverse-variance scale 16
			input: Input{
				HDU:          fitsio.HDU{Data: fitsio.ImageData{Width: 2, Height: 1, Pixels: []float32{8, 8}}},
				WeightPixels: []float32{4, 4},
			},
		},
	}
	_, _, wht, err := prepareFramePixels(planned[0], Options{SurfaceBrightnessNorm: true}, 0, skyPlane{})
	if err != nil {
		t.Fatalf("prepareFramePixels returned error: %v", err)
	}
	if got := wht[0]; got != 64 {
		t.Fatalf("scaled WeightPixels = %v, want 64", got)
	}
	if planned[0].input.WeightPixels[0] != 4 {
		t.Fatal("prepareFramePixels mutated original WeightPixels")
	}
}

func makeInput(path string, width, height int, pixels []float32, header fitsio.Header) Input {
	return Input{
		Path:          path,
		PrimaryHeader: fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'", "INSTRUME": "'WFC3'", "EXPTIME": "100"}},
		ExposureTime:  100,
		HDU: fitsio.HDU{
			Header: header,
			Data:   fitsio.ImageData{Width: width, Height: height, Pixels: pixels},
		},
	}
}

func headerWithCRPIX(crpix1, crpix2 float64) fitsio.Header {
	return fitsio.Header{Cards: map[string]string{
		"CRPIX1":   strconv.FormatFloat(crpix1, 'f', -1, 64),
		"CRPIX2":   strconv.FormatFloat(crpix2, 'f', -1, 64),
		"CRVAL1":   "100",
		"CRVAL2":   "22",
		"CD1_1":    "1",
		"CD1_2":    "0",
		"CD2_1":    "0",
		"CD2_2":    "1",
		"CTYPE1":   "'RA---TAN'",
		"CTYPE2":   "'DEC--TAN'",
		"DATE-OBS": "'2024-01-01T00:00:00Z'",
	}}
}

func TestSortInputsByWCSDistanceKeepsSpatialLocationsTogether(t *testing.T) {
	inputs := []Input{
		makeInput("origin-1.fits", 100, 100, nil, headerWithCRPIX(50, 50)),
		makeInput("bottom-1.fits", 100, 100, nil, headerWithCRPIX(50, -50)),
		makeInput("right-1.fits", 100, 100, nil, headerWithCRPIX(-50, 50)),
		makeInput("origin-2.fits", 100, 100, nil, headerWithCRPIX(50, 50)),
		makeInput("bottom-2.fits", 100, 100, nil, headerWithCRPIX(50, -50)),
		makeInput("right-2.fits", 100, 100, nil, headerWithCRPIX(-50, 50)),
		makeInput("origin-3.fits", 100, 100, nil, headerWithCRPIX(50, 50)),
		makeInput("bottom-3.fits", 100, 100, nil, headerWithCRPIX(50, -50)),
		makeInput("right-3.fits", 100, 100, nil, headerWithCRPIX(-50, 50)),
	}

	SortInputsByWCSDistance(inputs, nil)
	got := make([]string, len(inputs))
	for i := range inputs {
		got[i] = inputs[i].Path
	}
	want := []string{
		"origin-1.fits", "origin-2.fits", "origin-3.fits",
		"bottom-1.fits", "bottom-2.fits", "bottom-3.fits",
		"right-1.fits", "right-2.fits", "right-3.fits",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sorted paths = %v, want %v", got, want)
	}
}

func TestPropagateSameExposureAlignment(t *testing.T) {
	inputs := []Input{
		{Path: "a_flc.fits", SCIExt: 1},
		{Path: "a_flc.fits", SCIExt: 2}, // same exposure, unaligned sibling
		{Path: "b_flc.fits", SCIExt: 1},
	}
	results := make([]StarAlignmentResult, 3)
	results[0] = StarAlignmentResult{OffsetX: 5, OffsetY: -3, ManualTransform: processing.IdentityTransform(), HasManualTransform: true, Applied: true}
	results[2] = StarAlignmentResult{OffsetX: 99, OffsetY: 99, Applied: true}
	aligned := []bool{true, false, true}

	filled := propagateSameExposureAlignment(inputs, results, aligned)
	if filled != 1 {
		t.Fatalf("filled = %d, want 1", filled)
	}
	if !aligned[1] {
		t.Fatal("sci,2 sibling should be marked aligned after propagation")
	}
	if results[1].OffsetX != 5 || results[1].OffsetY != -3 || !results[1].Applied || !results[1].HasManualTransform {
		t.Fatalf("sci,2 did not inherit sibling alignment: %+v", results[1])
	}
	// Must not pull from a different exposure.
	if results[1].OffsetX == 99 {
		t.Fatal("sci,2 incorrectly inherited from a different file")
	}
}

func TestFramesMayOverlap(t *testing.T) {
	hdr := func(crval1 string) fitsio.Header {
		return fitsio.Header{Cards: map[string]string{
			"CRPIX1": "5", "CRPIX2": "5",
			"CRVAL1": crval1, "CRVAL2": "22",
			"CD1_1": "1", "CD1_2": "0", "CD2_1": "0", "CD2_2": "1",
		}}
	}
	a := makeInput("a.fits", 10, 10, nil, hdr("100"))
	b := makeInput("b.fits", 10, 10, nil, hdr("100")) // same pointing → overlaps
	c := makeInput("c.fits", 10, 10, nil, hdr("160")) // 60° away → cannot overlap

	if !framesMayOverlap(a, b) {
		t.Fatal("co-pointed frames should be reported as possibly overlapping")
	}
	if framesMayOverlap(a, c) {
		t.Fatal("frames 60° apart should be reported as non-overlapping")
	}
	// Missing WCS must be treated as "may overlap" (never skip when unsure).
	noWCS := makeInput("d.fits", 10, 10, nil, fitsio.Header{Cards: map[string]string{}})
	if !framesMayOverlap(a, noWCS) {
		t.Fatal("frames with unparseable WCS must not be skipped")
	}
}

func filledPixels(width, height int, value float32) []float32 {
	pixels := make([]float32, width*height)
	for i := range pixels {
		pixels[i] = value
	}
	return pixels
}

func TestAlignInputsBySelectedStarsAppliesAffineRefinement(t *testing.T) {
	// 400×400 image; stars are >130 px apart so CentroidNear (radius 50) never
	// confuses one star for another even after a small rotation+translation.
	refStars := []processing.Star{
		{X: 60, Y: 60},
		{X: 220, Y: 60},
		{X: 380, Y: 60},
		{X: 140, Y: 220},
		{X: 300, Y: 220},
		{X: 220, Y: 340},
	}
	refPixels := makeTestStarField(400, 400, refStars)
	targetStars := transformStarsAroundCenter(refStars, 200, 200, 3*math.Pi/180, 2.5, -1.75)
	targetPixels := makeTestStarField(400, 400, targetStars)

	// Use tiny CD scale (1e-4 deg/pix) so corner pixels stay within 0.02° of
	// CRVAL, well inside the normalizeAngleDelta range.  Both images share the
	// same header so ComputeWCSTransform returns the identity pixel→pixel map.
	wcsHdr := func() fitsio.Header {
		return fitsio.Header{Cards: map[string]string{
			"CRPIX1": "200", "CRPIX2": "200",
			"CRVAL1": "100", "CRVAL2": "22",
			"CD1_1": "0.0001", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0001",
		}}
	}
	inputs := []Input{
		makeInput("ref_flc.fits", 400, 400, refPixels, wcsHdr()),
		makeInput("target_flc.fits", 400, 400, targetPixels, wcsHdr()),
	}

	results, err := AlignInputsBySelectedStarsWithMode(inputs, refStars, 1, AlignmentModeRScale, 0)
	if err != nil {
		t.Fatalf("AlignInputsBySelectedStarsWithMode returned error: %v", err)
	}
	if !results[1].Applied {
		t.Fatalf("target alignment was not applied")
	}
	if !results[1].HasManualTransform {
		t.Fatalf("expected affine refinement to be recorded")
	}

	for i, ts := range targetStars {
		x, y := processing.ApplyAffineTransform(results[1].ManualTransform, ts.X, ts.Y)
		if math.Hypot(x-refStars[i].X, y-refStars[i].Y) > 2.0 {
			t.Fatalf("star %d remapped to (%.2f, %.2f), want near (%.2f, %.2f)", i, x, y, refStars[i].X, refStars[i].Y)
		}
	}
}

// TestAlignInputsByStarsIsDeterministic guards the whole multi-frame aligner
// (not just the RANSAC solver) against run-to-run variation. The aligner aligns
// frames concurrently and previously enqueued the results in goroutine-completion
// order, which let the chain fallback pick a different intermediate per run and
// produced a different mosaic. Every stage must now be deterministic, so running
// the identical inputs twice must yield byte-identical results.
func TestAlignInputsByStarsIsDeterministic(t *testing.T) {
	refStars := []processing.Star{
		{X: 60, Y: 60}, {X: 220, Y: 60}, {X: 380, Y: 60},
		{X: 140, Y: 220}, {X: 300, Y: 220}, {X: 220, Y: 340},
	}
	refPixels := makeTestStarField(400, 400, refStars)
	wcsHdr := func() fitsio.Header {
		return fitsio.Header{Cards: map[string]string{
			"CRPIX1": "200", "CRPIX2": "200",
			"CRVAL1": "100", "CRVAL2": "22",
			"CD1_1": "0.0001", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0001",
		}}
	}

	// One reference plus several targets at small, distinct rotations+shifts so
	// the concurrent primary pass runs many goroutines whose completion order is a
	// race.
	makeInputs := func() []Input {
		inputs := []Input{makeInput("ref_flc.fits", 400, 400, refPixels, wcsHdr())}
		offsets := []struct {
			angleDeg, dx, dy float64
		}{
			{1.5, 2.0, -1.0}, {-2.0, -1.5, 2.5}, {0.8, -3.0, -2.0}, {2.5, 1.0, 1.5},
		}
		for k, o := range offsets {
			ts := transformStarsAroundCenter(refStars, 200, 200, o.angleDeg*math.Pi/180, o.dx, o.dy)
			px := makeTestStarField(400, 400, ts)
			inputs = append(inputs, makeInput(fmt.Sprintf("target%d_flc.fits", k), 400, 400, px, wcsHdr()))
		}
		return inputs
	}

	first, err := AlignInputsByStarsWithMode(makeInputs(), 1, AlignmentModeTweakRegRScale, 2.0)
	if err != nil {
		t.Fatalf("first align returned error: %v", err)
	}
	// Guard against a vacuous pass: the test is only meaningful if alignment
	// actually ran and produced transforms for the targets.
	for i := 1; i < len(first); i++ {
		if !first[i].Applied || !first[i].HasManualTransform {
			t.Fatalf("target %d did not align (Applied=%v HasManualTransform=%v, err=%q); test would be vacuous",
				i, first[i].Applied, first[i].HasManualTransform, first[i].Error)
		}
	}
	// Run several more times; any nondeterminism (map iteration, goroutine order,
	// unstable sort tie) tends to surface only intermittently, so repeat.
	for run := 0; run < 8; run++ {
		next, err := AlignInputsByStarsWithMode(makeInputs(), 1, AlignmentModeTweakRegRScale, 2.0)
		if err != nil {
			t.Fatalf("run %d align returned error: %v", run, err)
		}
		if len(next) != len(first) {
			t.Fatalf("run %d produced %d results, want %d", run, len(next), len(first))
		}
		for i := range first {
			if next[i] != first[i] {
				t.Fatalf("run %d result[%d] differs:\n first: %+v\n  this: %+v", run, i, first[i], next[i])
			}
		}
	}
}

// TestAlignInputsStreamingMatchesResident verifies the streaming alignment path
// (metadata-only inputs whose pixels are reloaded on demand) produces exactly the
// same result as the resident path (all pixels held in memory). Synthetic star
// fields are written to temp FITS files and loaded both ways, so the test
// exercises the real on-demand reload and the catalog-based TweakReg estimator.
func TestAlignInputsStreamingMatchesResident(t *testing.T) {
	refStars := []processing.Star{
		{X: 60, Y: 60}, {X: 220, Y: 60}, {X: 380, Y: 60},
		{X: 140, Y: 220}, {X: 300, Y: 220}, {X: 220, Y: 340},
	}
	wcsHdr := func() fitsio.Header {
		return fitsio.Header{Cards: map[string]string{
			"CRPIX1": "200", "CRPIX2": "200",
			"CRVAL1": "100", "CRVAL2": "22",
			"CD1_1": "0.0001", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0001",
		}}
	}

	dir := t.TempDir()
	// Write one reference plus several rotated/shifted targets to temp FITS files.
	type frame struct {
		angleDeg, dx, dy float64
	}
	frames := []frame{{0, 0, 0}, {1.5, 2.0, -1.0}, {-2.0, -1.5, 2.5}, {0.8, -3.0, -2.0}}
	// 600x600 float32 = ~1.4 MiB per frame, above LoadFileMetadata's small-data
	// decode threshold, so metadata loads leave the pixels on disk and alignment
	// genuinely streams them back on demand.
	const dim = 600
	var paths []string
	for k, f := range frames {
		stars := refStars
		if k > 0 {
			stars = transformStarsAroundCenter(refStars, 200, 200, f.angleDeg*math.Pi/180, f.dx, f.dy)
		}
		px := makeTestStarField(dim, dim, stars)
		path := filepath.Join(dir, fmt.Sprintf("frame%d_flc.fits", k))
		if err := fitsio.WriteFloat32Image(path, wcsHdr(), fitsio.ImageData{Width: dim, Height: dim, Pixels: px}); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		paths = append(paths, path)
	}

	loadAll := func(loader func(string) ([]Input, error)) []Input {
		var inputs []Input
		for _, p := range paths {
			ins, err := loader(p)
			if err != nil {
				t.Fatalf("load %s: %v", p, err)
			}
			inputs = append(inputs, ins...)
		}
		return inputs
	}

	resident := loadAll(LoadInputsFromPath)
	streamed := loadAll(LoadInputsMetadataFromPath)

	// Confirm the streaming inputs really hold no pixels (the memory win) but can
	// still be reloaded (Path is set).
	for i := range streamed {
		if streamed[i].HDU.Data.Pixels != nil {
			t.Fatalf("streamed input[%d] unexpectedly carries resident pixels", i)
		}
		if streamed[i].Path == "" {
			t.Fatalf("streamed input[%d] has no Path to stream from", i)
		}
	}

	mode := AlignmentModeTweakRegRScale
	resResident, err := AlignInputsByStarsWithMode(resident, 1, mode, 2.0)
	if err != nil {
		t.Fatalf("align (resident) error: %v", err)
	}
	resStreamed, err := AlignInputsByStarsWithMode(streamed, 1, mode, 2.0)
	if err != nil {
		t.Fatalf("align (streamed) error: %v", err)
	}

	if len(resResident) != len(resStreamed) {
		t.Fatalf("result count: resident %d, streamed %d", len(resResident), len(resStreamed))
	}
	for i := range resResident {
		if resResident[i] != resStreamed[i] {
			t.Fatalf("result[%d] differs between resident and streamed paths:\n resident: %+v\n streamed: %+v",
				i, resResident[i], resStreamed[i])
		}
	}

	// Guard against a vacuous pass: at least one target must have actually aligned,
	// so the equivalence check covered the fitting math, not just failures.
	aligned := 0
	for i := 1; i < len(resStreamed); i++ {
		if resStreamed[i].Applied && resStreamed[i].HasManualTransform {
			aligned++
		}
	}
	if aligned == 0 {
		t.Fatal("no target aligned; equivalence held but the fit was not exercised")
	}
}

func makeTestStarField(width, height int, stars []processing.Star) []float32 {
	pixels := make([]float32, width*height)
	for _, star := range stars {
		cx := int(math.Round(star.X))
		cy := int(math.Round(star.Y))
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				x := cx + dx
				y := cy + dy
				if x < 0 || x >= width || y < 0 || y >= height {
					continue
				}
				dist2 := dx*dx + dy*dy
				pixels[y*width+x] = float32(200 - 20*dist2)
			}
		}
	}
	return pixels
}

func transformStarsAroundCenter(stars []processing.Star, cx, cy, angle, dx, dy float64) []processing.Star {
	sinA, cosA := math.Sin(angle), math.Cos(angle)
	out := make([]processing.Star, len(stars))
	for i, star := range stars {
		sx := star.X - cx
		sy := star.Y - cy
		out[i] = processing.Star{
			X: cx + (sx*cosA - sy*sinA) + dx,
			Y: cy + (sx*sinA + sy*cosA) + dy,
		}
	}
	return out
}
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestPlanInputsSameFileSCIChipsGetMapper(t *testing.T) {
	// Same-file SCI chips now use a WCSMapper for per-pixel distortion-aware
	// placement instead of the old translation-only affine hack. Verify that
	// planInputs builds a mapper for chip2 and that the affine in sourceToRef
	// (used for CR detection) reflects the full WCS rotation, not just a shift.
	ref := Input{
		Path:   "single_flc.fits",
		SCIExt: 1,
		HDU: fitsio.HDU{Header: fitsio.Header{Cards: map[string]string{
			"CRPIX1": "10", "CRPIX2": "10",
			"CRVAL1": "100", "CRVAL2": "22",
			"CD1_1": "1", "CD1_2": "0",
			"CD2_1": "0", "CD2_2": "1",
		}}, Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{1, 1, 1, 1}}},
	}
	chip2 := Input{
		Path:   "single_flc.fits",
		SCIExt: 2,
		HDU: fitsio.HDU{Header: fitsio.Header{Cards: map[string]string{
			"CRPIX1": "8", "CRPIX2": "10",
			"CRVAL1": "100", "CRVAL2": "22",
			"CD1_1": "0", "CD1_2": "-1",
			"CD2_1": "1", "CD2_2": "0",
		}}, Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{2, 2, 2, 2}}},
	}

	planned, statuses, _, _, _, _, err := planInputs([]Input{ref, chip2}, 1)
	if err != nil {
		t.Fatalf("planInputs returned error: %v", err)
	}
	if len(planned) != 2 {
		t.Fatalf("planned inputs = %d, want 2", len(planned))
	}
	if statuses[1].Status != "aligned" {
		t.Fatalf("status = %q, want aligned", statuses[1].Status)
	}
	// Chip2 must have a mapper for per-pixel WCS placement.
	if planned[1].mapper == nil {
		t.Fatal("expected mapper for same-file chip2, got nil")
	}
	// The sourceToRef affine should reflect the full WCS rotation from chip2
	// (90° rotation encoded in its CD matrix), not translation-only.
	got := planned[1].sourceToRef
	if got.B == 0 && got.D == 0 {
		t.Fatalf("expected full WCS affine for chip2, got translation-only %+v", got)
	}
	// The reference chip must also carry a mapper so its own distortion
	// (SIP + D2IM) is removed onto the linear output plane, matching the
	// other chips. Otherwise a single multi-chip exposure's chips would be
	// drizzled in mismatched (distorted vs. undistorted) pixel space.
	if planned[0].mapper == nil {
		t.Fatal("expected mapper for reference chip, got nil")
	}
	_ = processing.IdentityTransform() // keep import used
}

func TestEstimateSkyValueMedianIgnoresOutlier(t *testing.T) {
	pixels := []float32{5, 5, 5, 5, 100}
	sky, err := estimateSkyValue(pixels, SkysubOptions{Enabled: true, Stat: SkyStatMedian, Width: 0.1, Clip: 5, LSigma: 4, USigma: 4})
	if err != nil {
		t.Fatalf("estimateSkyValue returned error: %v", err)
	}
	if math.Abs(sky-5) > 1e-6 {
		t.Fatalf("sky = %v, want 5", sky)
	}
}

func TestEstimateSkyValueSkipsNaNsAndBounds(t *testing.T) {
	pixels := []float32{float32(math.NaN()), 1, 2, 3, 50}
	sky, err := estimateSkyValue(pixels, SkysubOptions{Enabled: true, Stat: SkyStatMean, Width: 0.1, Clip: 2, LSigma: 4, USigma: 4, Lower: 1.5, Upper: 3.5, HasLower: true, HasUpper: true})
	if err != nil {
		t.Fatalf("estimateSkyValue returned error: %v", err)
	}
	if math.Abs(sky-2.5) > 1e-6 {
		t.Fatalf("sky = %v, want 2.5", sky)
	}
}

func TestBuildSkysubDisabledIsNoOp(t *testing.T) {
	inputs := []Input{
		makeInput("a_flc.fits", 2, 2, filledPixels(2, 2, 10), headerWithCRPIX(10, 10)),
		makeInput("b_flc.fits", 2, 2, filledPixels(2, 2, 14), headerWithCRPIX(10, 10)),
	}

	result, err := Build(inputs, Options{Scale: 1, Skysub: SkysubOptions{Enabled: false}})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	for i, px := range result.Pixels {
		if math.Abs(float64(px-12)) > 1e-6 {
			t.Fatalf("pixel[%d] = %v, want 12", i, px)
		}
		if result.Inputs[i/4].SkySubtracted {
			t.Fatalf("input %d unexpectedly marked sky-subtracted", i/4)
		}
	}
}

func TestBuildSkysubLocalMinSubtractsPerInput(t *testing.T) {
	inputs := []Input{
		makeInput("a_flc.fits", 2, 2, filledPixels(2, 2, 10), headerWithCRPIX(10, 10)),
		makeInput("b_flc.fits", 2, 2, filledPixels(2, 2, 14), headerWithCRPIX(10, 10)),
	}

	result, err := Build(inputs, Options{Scale: 1, Skysub: SkysubOptions{Enabled: true, Method: SkyMethodLocalMin, Stat: SkyStatMedian, Width: 0.1, Clip: 5, LSigma: 4, USigma: 4}})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	for i, px := range result.Pixels {
		if math.Abs(float64(px)) > 1e-6 {
			t.Fatalf("pixel[%d] = %v, want 0", i, px)
		}
	}
	if !result.Inputs[0].SkySubtracted || !result.Inputs[1].SkySubtracted {
		t.Fatalf("expected both inputs to be marked sky-subtracted: %+v", result.Inputs)
	}
	if math.Abs(result.Inputs[0].SkyValue-10) > 1e-6 || math.Abs(result.Inputs[1].SkyValue-14) > 1e-6 {
		t.Fatalf("unexpected sky values: %+v", result.Inputs)
	}
	if result.Inputs[0].Status != "sky-subtracted and drizzled" {
		t.Fatalf("status[0] = %q, want sky-subtracted and drizzled", result.Inputs[0].Status)
	}
}

func TestPlanSkysubLeavesReferenceOnlyUntouched(t *testing.T) {
	planned := []plannedInput{
		{input: Input{ReferenceOnly: true, HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: filledPixels(2, 2, 20)}}}},
		{input: Input{Path: "data_flc.fits", HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: filledPixels(2, 2, 10)}}}},
	}
	skyOffset, _, applied, skyValues, err := planSkysub(planned, Options{Skysub: SkysubOptions{Enabled: true, Method: SkyMethodLocalMin, Stat: SkyStatMedian, Width: 0.1, Clip: 5, LSigma: 4, USigma: 4}})
	if err != nil {
		t.Fatalf("planSkysub returned error: %v", err)
	}
	if applied[0] {
		t.Fatal("reference-only input should not be sky-subtracted")
	}
	if !applied[1] {
		t.Fatal("data input should be sky-subtracted")
	}
	if skyOffset[0] != 0 {
		t.Fatalf("reference-only sky offset = %v, want 0", skyOffset[0])
	}
	if !math.IsNaN(skyValues[0]) {
		t.Fatal("reference-only sky value should remain NaN")
	}
	// The data frame's pixels must not have been mutated in place (sky is applied
	// later, per frame, in prepareFramePixels).
	if planned[1].input.HDU.Data.Pixels[0] != 10 {
		t.Fatalf("planSkysub mutated source pixels: %v", planned[1].input.HDU.Data.Pixels[0])
	}
}

func TestOverlapSampleStrideKeepsLargeACSCellsWellSampled(t *testing.T) {
	got := overlapSampleStride(4096, 4096)
	if got > 4 {
		t.Fatalf("overlapSampleStride(4096, 4096) = %d, want at most 4", got)
	}
}

func TestComputeMatchedSkyOffsetsChainsAcrossMosaic(t *testing.T) {
	planned := []plannedInput{
		{input: Input{Path: "left_flc.fits"}},
		{input: Input{Path: "middle_flc.fits"}},
		{input: Input{Path: "right_flc.fits"}},
	}
	maps := []map[int64]float64{
		{},
		{},
		{},
	}
	for cell := 0; cell < skyMinOverlapCells; cell++ {
		key := int64(cell)
		maps[0][key] = 10
		maps[1][key] = 13
	}
	for cell := 0; cell < skyMinOverlapCells; cell++ {
		key := int64(100 + cell)
		maps[1][key] = 13
		maps[2][key] = 11
	}

	offsets, matched := computeMatchedSkyOffsets(planned, maps, SkysubOptions{
		Enabled: true,
		Method:  SkyMethodGlobalMinMatch,
		Stat:    SkyStatMedian,
		Width:   0.1,
		Clip:    5,
		LSigma:  4,
		USigma:  2.5,
	})

	for i, ok := range matched {
		if !ok {
			t.Fatalf("matched[%d] = false, want true", i)
		}
	}
	want := []float64{0, 3, 1}
	for i := range want {
		if math.Abs(offsets[i]-want[i]) > 1e-6 {
			t.Fatalf("offsets[%d] = %v, want %v (all offsets: %v)", i, offsets[i], want[i], offsets)
		}
	}
}

func TestBuildOutputHeaderUsesMetaSourceFilterNotWCSReference(t *testing.T) {
	ref := Input{
		Path:          "ref_astrometry.fits",
		ReferenceOnly: true,
		PrimaryHeader: fitsio.Header{Cards: map[string]string{"FILTER": "'F814W'", "INSTRUME": "'ACS'"}},
		HDU:           fitsio.HDU{Header: headerWithCRPIX(3, 3)},
	}
	sci := Input{
		Path:          "sci_f656n.fits",
		PrimaryHeader: fitsio.Header{Cards: map[string]string{"FILTER": "'F656N'", "INSTRUME": "'WFC3'"}},
		HDU:           fitsio.HDU{Header: headerWithCRPIX(3, 3)},
	}

	hdr := buildOutputHeader(ref, sci, 4, 4, 0, 0, 1, 1)

	if got := fitsio.HeaderString(hdr, "FILTER"); got != "F656N" {
		t.Fatalf("FILTER = %q, want F656N (from science input, not WCS reference)", got)
	}
	if got := fitsio.HeaderString(hdr, "INSTRUME"); got != "WFC3" {
		t.Fatalf("INSTRUME = %q, want WFC3 (from science input, not WCS reference)", got)
	}
	// WCS geometry still anchored to ref.
	if _, ok := hdr.Cards["CRPIX1"]; !ok {
		t.Fatal("expected CRPIX1 to be carried over from ref")
	}
}
