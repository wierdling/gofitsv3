package mosaic

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
)

func writeSyntheticGMOSCalibration(t *testing.T, path, obstype string, chips int) {
	t.Helper()
	h := fitsio.Header{Cards: map[string]string{"INSTRUME": "GMOS-N", "DETECTOR": "EEV", "CCDSUM": "2 2", "OBSTYPE": "'" + obstype + "'"}}
	exts := make([]fitsio.ImageExtension, 0, chips)
	pixels := []float32{1, 10, 2, 10, 3, 10}
	if obstype == "FLAT" {
		pixels = []float32{20, 10, 20, 10, 20, 10}
	}
	for i := 0; i < chips; i++ {
		eh := fitsio.Header{Cards: map[string]string{"DATASEC": "'[1:2,1:2]'", "BIASSEC": "'[3:3,1:2]'"}}
		exts = append(exts, fitsio.ImageExtension{Header: eh, Data: fitsio.ImageData{Width: 3, Height: 2, Pixels: pixels}})
	}
	if err := fitsio.WriteFloat32ImageWithExtensions(path, h, fitsio.ImageData{}, exts...); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiEffectiveFilterSkipsOpenWheel(t *testing.T) {
	h := fitsio.Header{Cards: map[string]string{"INSTRUME": "GMOS-N", "FILTER1": "open1-6", "FILTER2": "r_G0303"}}
	if got := fitsio.FilterString(h); got != "r_G0303" {
		t.Fatalf("effective filter = %q, want r_G0303", got)
	}
}

func TestGeminiCalibrationHeaderFindsTwilightObject(t *testing.T) {
	cal := fitsio.Header{Cards: map[string]string{"INSTRUME": "GMOS-N", "OBSTYPE": "OBJECT", "OBJECT": "Twilight Flat"}}
	if !IsGeminiCalibrationHeader(cal) {
		t.Fatal("twilight flat was not classified as calibration")
	}
	sci := fitsio.Header{Cards: map[string]string{"INSTRUME": "GMOS-N", "OBSTYPE": "OBJECT", "OBJECT": "NGC 3359"}}
	if IsGeminiCalibrationHeader(sci) {
		t.Fatal("ordinary OBJECT science was classified as calibration")
	}
	bpm := fitsio.Header{Cards: map[string]string{"INSTRUME": "GMOS-N", "OBSTYPE": "BPM"}}
	if !IsGeminiCalibrationHeader(bpm) {
		t.Fatal("BPM header was not classified as calibration")
	}
}

func TestGMOSCompatibilityKeyUsesActualOptionalLayout(t *testing.T) {
	p := fitsio.Header{Cards: map[string]string{"INSTRUME": "GMOS-N", "DETECTOR": "EEV", "CCDSUM": "2 2"}}
	c := fitsio.Header{Cards: map[string]string{"DATASEC": "[2:3,1:2]", "BIASSEC": "[1:1,1:2]", "AMPINTEG": "1"}}
	if got := gmosCompatibilityKeyPair(p, c, 2, 2); got == "" {
		t.Fatal("actual optional GMOS layout produced empty compatibility key")
	}
	if got := gmosCompatibilityKeyPair(p, c, 2, 2); got != gmosCompatibilityKeyPair(p, c, 2, 2) {
		t.Fatal("compatibility key is not deterministic")
	}
}

func TestGMOSCompositeCompatibilityKeyRejectsLaterChipMismatch(t *testing.T) {
	p := fitsio.Header{Cards: map[string]string{"DETECTOR": "EEV", "CCDSUM": "2 2"}}
	chip := func(sec string) fitsio.HDU {
		return fitsio.HDU{Header: fitsio.Header{Cards: map[string]string{"DATASEC": sec, "BIASSEC": "[1:1,1:2]"}}, Data: fitsio.ImageData{Width: 2, Height: 2}}
	}
	a := []fitsio.HDU{chip("[2:3,1:2]"), chip("[2:3,1:2]"), chip("[2:3,1:2]")}
	b := []fitsio.HDU{chip("[2:3,1:2]"), chip("[2:4,1:2]"), chip("[2:3,1:2]")}
	if gmosCompositeCompatibilityKey(p, a) == "" {
		t.Fatal("complete three-chip descriptor is empty")
	}
	if gmosCompositeCompatibilityKey(p, a) == gmosCompositeCompatibilityKey(p, b) {
		t.Fatal("later-chip mismatch was ignored")
	}
}

func TestGMOSMasterCalibrationAndBPM(t *testing.T) {
	frames := []fitsio.ImageData{{Width: 2, Height: 1, Pixels: []float32{10, 20}}, {Width: 2, Height: 1, Pixels: []float32{12, 22}}}
	bias, err := BuildGMOSMasterBias(frames)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := BuildGMOSMasterFlat([]fitsio.ImageData{{Width: 2, Height: 1, Pixels: []float32{13, 25}}}, bias)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ApplyGMOSCalibration(fitsio.ImageData{Width: 2, Height: 1, Pixels: []float32{22, 44}}, bias, flat, []bool{false, true})
	if err != nil {
		t.Fatal(err)
	}
	if !isFinite32(got.Pixels[0]) || got.Pixels[0] <= 0 || !math.IsNaN(float64(got.Pixels[1])) {
		t.Fatalf("calibrated pixels=%v", got.Pixels)
	}
}

func TestBuildGMOSMastersFromSelectionAllowsUnequalEnsembles(t *testing.T) {
	d := t.TempDir()
	b1 := filepath.Join(d, "b1.fits")
	b2 := filepath.Join(d, "b2.fits")
	f1 := filepath.Join(d, "f1.fits")
	writeSyntheticGMOSCalibration(t, b1, "BIAS", 3)
	writeSyntheticGMOSCalibration(t, b2, "BIAS", 3)
	writeSyntheticGMOSCalibration(t, f1, "FLAT", 3)
	s := GMOSCalibrationSelection{Bias: &GMOSCalibrationFrame{Path: b1}, Flat: &GMOSCalibrationFrame{Path: f1}, BiasFrames: []GMOSCalibrationFrame{{Path: b1}, {Path: b2}}, FlatFrames: []GMOSCalibrationFrame{{Path: f1}}}
	bias, flat, err := BuildGMOSMastersFromSelection(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(bias) != 3 || len(flat) != 3 {
		t.Fatalf("masters=%d,%d", len(bias), len(flat))
	}
}

func TestBuildGMOSMastersFromSelectionRejectsMissingChip(t *testing.T) {
	d := t.TempDir()
	b := filepath.Join(d, "b.fits")
	f := filepath.Join(d, "f.fits")
	writeSyntheticGMOSCalibration(t, b, "BIAS", 2)
	writeSyntheticGMOSCalibration(t, f, "FLAT", 3)
	s := GMOSCalibrationSelection{Bias: &GMOSCalibrationFrame{Path: b}, Flat: &GMOSCalibrationFrame{Path: f}, BiasFrames: []GMOSCalibrationFrame{{Path: b}}, FlatFrames: []GMOSCalibrationFrame{{Path: f}}}
	if _, _, err := BuildGMOSMastersFromSelection(s); err == nil {
		t.Fatal("two-chip calibration frame was accepted")
	}
}

func TestEnsureCombinedExposureCanceledGMOSDoesNotPublishMaster(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "raw.fits")
	if _, _, err := EnsureCombinedExposure(path, CombineOptions{Ctx: ctx}); err != ErrCancelled {
		t.Fatalf("canceled ensure error=%v, want ErrCancelled", err)
	}
}

func TestGMOSFiniteMedianUsesCentralAverage(t *testing.T) {
	if got := finiteMedian([]float32{1, 3, 5}); got != 3 {
		t.Fatalf("odd median = %v", got)
	}
	if got := finiteMedian([]float32{1, 5, 3, 9}); got != 4 {
		t.Fatalf("even median = %v, want 4", got)
	}
	if got := finiteMedian([]float32{float32(math.NaN()), 2, 6, float32(math.NaN())}); got != 4 {
		t.Fatalf("finite median = %v, want 4", got)
	}
}

func TestGMOSCalibrationRejectsPixelLengthMismatch(t *testing.T) {
	_, err := BuildGMOSMasterBias([]fitsio.ImageData{{Width: 2, Height: 2, Pixels: []float32{1, 2, 3}}})
	if err == nil {
		t.Fatal("short bias frame was accepted")
	}
	bias := GMOSMaster{Width: 1, Height: 1, Pixels: []float32{1}}
	flat := GMOSMaster{Width: 1, Height: 1, Pixels: []float32{1}}
	_, err = ApplyGMOSCalibration(fitsio.ImageData{Width: 1, Height: 1, Pixels: nil}, bias, flat, nil)
	if err == nil {
		t.Fatal("short science frame was accepted")
	}
}

func TestGMOSMasterFlatRejectsEqualCountDifferentShapeBias(t *testing.T) {
	frames := []fitsio.ImageData{{Width: 2, Height: 2, Pixels: []float32{1, 2, 3, 4}}}
	bias := GMOSMaster{Width: 1, Height: 4, Pixels: []float32{0, 0, 0, 0}}
	if _, err := BuildGMOSMasterFlat(frames, bias); err == nil {
		t.Fatal("equal-count, different-shape bias was accepted")
	}
}

func TestGMOSThreeChipFlatNormalizationPreservesRelativeResponse(t *testing.T) {
	flats := []GMOSMaster{
		{Width: 1, Height: 1, Pixels: []float32{10}},
		{Width: 1, Height: 1, Pixels: []float32{20}},
		{Width: 1, Height: 1, Pixels: []float32{30}},
	}
	if err := normalizeGMOSMasterFlats(flats); err != nil {
		t.Fatal(err)
	}
	for i, want := range []float32{0.5, 1, 1.5} {
		if flats[i].Pixels[0] != want {
			t.Fatalf("chip %d normalized flat = %v, want %v", i, flats[i].Pixels[0], want)
		}
	}
	// Science levels proportional to the detector response calibrate to the
	// same value, while the response ratio remains available to calibration.
	for i, science := range []float32{5, 10, 15} {
		got, err := ApplyGMOSCalibration(fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{science}}, GMOSMaster{Width: 1, Height: 1, Pixels: []float32{0}}, flats[i], nil)
		if err != nil || math.Abs(float64(got.Pixels[0])-10) > 1e-6 {
			t.Fatalf("chip %d calibrated science = %v, err %v", i, got.Pixels, err)
		}
	}
}

func TestGMOSMasterFlatNormalizationRejectsUnusableSamples(t *testing.T) {
	flats := []GMOSMaster{{Width: 2, Height: 1, Pixels: []float32{0, float32(math.NaN())}}}
	if err := normalizeGMOSMasterFlats(flats); err == nil {
		t.Fatal("flat with no positive finite samples was accepted")
	}
	if flats[0].Pixels[0] != 0 || !math.IsNaN(float64(flats[0].Pixels[1])) {
		t.Fatalf("flat was partially mutated after rejection: %v", flats[0].Pixels)
	}
}

func TestGMOSThreeChipFlatNormalizationRejectsOneUnusableChipWithoutMutation(t *testing.T) {
	flats := []GMOSMaster{
		{Width: 1, Height: 1, Pixels: []float32{10}},
		{Width: 1, Height: 1, Pixels: []float32{float32(math.NaN())}},
		{Width: 1, Height: 1, Pixels: []float32{30}},
	}
	if err := normalizeGMOSMasterFlats(flats); err == nil {
		t.Fatal("three-chip flats with one unusable chip were accepted")
	}
	if flats[0].Pixels[0] != 10 || !math.IsNaN(float64(flats[1].Pixels[0])) || flats[2].Pixels[0] != 30 {
		t.Fatalf("flats were partially normalized after failure: %v", flats)
	}
}

func TestSelectGMOSCalibrationsMatchesConfigurationAndFilter(t *testing.T) {
	science := GMOSCalibrationFrame{Kind: GMOSScience, Filter: "r_G0303", Date: "2005-03-30", Key: "det|2 2|1|2|data|bias"}
	m := GMOSCalibrationManifest{
		Bias:     []GMOSCalibrationFrame{{Path: "bias.fits", Kind: GMOSBias, Date: "2005-03-29", Key: science.Key}},
		Twilight: []GMOSCalibrationFrame{{Path: "gflat.fits", Kind: GMOSTwilight, Filter: "g_G0301", Date: science.Date, Key: science.Key}, {Path: "rflat.fits", Kind: GMOSTwilight, Filter: science.Filter, Date: "2005-03-31", Key: science.Key}},
	}
	s := SelectGMOSCalibrations(science, m)
	if s.Bias == nil || s.Bias.Path != "bias.fits" || s.Flat == nil || s.Flat.Path != "rflat.fits" {
		t.Fatalf("selection = %+v", s)
	}
	if len(s.BiasFrames) != 1 || GMOSCalibrationFingerprint(s) == "" {
		t.Fatalf("selection ensemble/fingerprint missing: %+v", s)
	}
	m.Bias = append(m.Bias, GMOSCalibrationFrame{Path: "bad.fits", Kind: GMOSBias, Date: science.Date, Key: science.Key, ExposureTime: 5.18, Anomalous: true})
	if got := SelectGMOSCalibrations(science, m); len(got.BiasFrames) != 1 {
		t.Fatalf("anomalous bias selected by default: %d", len(got.BiasFrames))
	}
	if got := SelectGMOSCalibrationsWithOptions(science, m, true); len(got.BiasFrames) != 2 {
		t.Fatalf("anomalous override selected %d bias frames, want 2", len(got.BiasFrames))
	}
}

func TestPrepareGMOSPixelsTrimsAndSubtractsRowOverscan(t *testing.T) {
	h := fitsio.Header{Cards: map[string]string{
		"DATASEC": "'[2:3,1:2]'", "BIASSEC": "'[1:1,1:2]'",
		"CRPIX1": "2", "CRPIX2": "1",
	}}
	in := fitsio.ImageData{Width: 3, Height: 2, Pixels: []float32{10, 110, 120, 20, 220, 230}}
	out, err := prepareGMOSPixels(h, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Width != 2 || out.Height != 2 {
		t.Fatalf("dimensions = %dx%d", out.Width, out.Height)
	}
	want := []float32{100, 110, 200, 210}
	for i := range want {
		if out.Pixels[i] != want[i] {
			t.Fatalf("pixel %d = %v, want %v", i, out.Pixels[i], want[i])
		}
	}
	shiftGMOSWCS(&h, out.Width, out.Height)
	if v, ok := fitsio.HeaderFloat(h, "CRPIX1"); !ok || math.Abs(v-1) > 1e-9 {
		t.Fatalf("CRPIX1 = %v", v)
	}
}

func TestScienceHDUsRecognizesUnnamedGeminiExtensions(t *testing.T) {
	primary := fitsio.Header{Cards: map[string]string{"INSTRUME": "GMOS-N"}}
	f := &fitsio.File{HDUs: []fitsio.HDU{
		{Header: primary},
		{Data: fitsio.ImageData{Width: 2, Height: 2}},
		{ExtName: "DQ", Data: fitsio.ImageData{Width: 2, Height: 2}},
		{Data: fitsio.ImageData{Width: 2, Height: 2}},
	}}
	got := scienceHDUs(f, primary)
	if len(got) != 2 {
		t.Fatalf("unnamed science extensions = %d, want 2", len(got))
	}
}

func TestDiscoverGMOSCalibrationClassifiesFramesAndExcludesThemFromFilters(t *testing.T) {
	d := t.TempDir()
	science := filepath.Join(d, "science.fits")
	bias := filepath.Join(d, "bias.fits")
	flat := filepath.Join(d, "twilight.fits")
	bpm := filepath.Join(d, "bpm.fits")
	writeGMOSMEF(t, science, "OBJECT", "r_G0303", 45, false)
	writeGMOSMEF(t, bias, "BIAS", "", 5, false)
	writeGMOSMEF(t, flat, "FLAT", "r_G0303", 7, false)
	writeGMOSMEF(t, bpm, "BPM", "", 0, true)

	manifest, err := DiscoverGMOSCalibration(d)
	if err != nil {
		t.Fatalf("DiscoverGMOSCalibration returned error: %v", err)
	}
	if len(manifest.Science) != 1 || manifest.Science[0].Path != science {
		t.Fatalf("science manifest = %+v", manifest.Science)
	}
	if len(manifest.Bias) != 1 || len(manifest.Twilight) != 1 || len(manifest.BPM) != 1 {
		t.Fatalf("calibration manifest counts = bias %d flat %d bpm %d", len(manifest.Bias), len(manifest.Twilight), len(manifest.BPM))
	}
	groups, err := DiscoverFilters(d)
	if err != nil {
		t.Fatalf("DiscoverFilters returned error: %v", err)
	}
	if len(groups) != 1 || len(groups["r_G0303"]) != 1 || groups["r_G0303"][0] != science {
		t.Fatalf("filter groups = %+v", groups)
	}
}

func TestPrepareGMOSPixelsRejectsMalformedAndNonFiniteOverscanRows(t *testing.T) {
	base := fitsio.Header{Cards: map[string]string{"DATASEC": "[2:3,1:2]", "BIASSEC": "[1:1,1:2]"}}
	for _, raw := range []string{"", "[2:1,1:2]", "[2:3]", "[x:3,1:2]"} {
		h := base
		h.Cards = map[string]string{"DATASEC": raw, "BIASSEC": "[1:1,1:2]"}
		if _, err := prepareGMOSPixels(h, fitsio.ImageData{Width: 3, Height: 2, Pixels: make([]float32, 6)}); err == nil {
			t.Fatalf("malformed DATASEC %q was accepted", raw)
		}
	}
	in := fitsio.ImageData{Width: 3, Height: 2, Pixels: []float32{float32(math.NaN()), 10, 20, 5, 30, 40}}
	if _, err := prepareGMOSPixels(base, in); err == nil {
		t.Fatal("row with no finite overscan samples was accepted")
	}
}

func TestGMOSCalibrationRecipeCacheRequiresMatchingNonEmptyFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.fits")
	if GMOSCalibrationCacheValid(path, "recipe-a") {
		t.Fatal("missing recipe was considered valid")
	}
	if err := WriteGMOSCalibrationCacheRecipe(path, "recipe-a"); err != nil {
		t.Fatalf("WriteGMOSCalibrationCacheRecipe: %v", err)
	}
	if !GMOSCalibrationCacheValid(path, "recipe-a") {
		t.Fatal("matching recipe was not considered valid")
	}
	if GMOSCalibrationCacheValid(path, "recipe-b") || GMOSCalibrationCacheValid(path, "") {
		t.Fatal("mismatched or empty fingerprint was considered valid")
	}
	if err := os.WriteFile(path+".recipe", []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if GMOSCalibrationCacheValid(path, "recipe-a") {
		t.Fatal("malformed recipe was considered valid")
	}
}
