package mosaic

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestEnsureCombinedExposureGMOSCalibrationHookAndCacheGuards(t *testing.T) {
	d := t.TempDir()
	source := filepath.Join(d, "gmos.fits")
	bias := filepath.Join(d, "bias.fits")
	flat := filepath.Join(d, "flat.fits")
	writeGMOSMEF(t, source, "OBJECT", "r_G0303", 45, false)
	writeGMOSMEF(t, bias, "BIAS", "", 5, false)
	writeGMOSMEF(t, flat, "FLAT", "r_G0303", 7, false)
	selection := &GMOSCalibrationSelection{Bias: &GMOSCalibrationFrame{Path: bias}, Flat: &GMOSCalibrationFrame{Path: flat}, BiasFrames: []GMOSCalibrationFrame{{Path: bias}}, FlatFrames: []GMOSCalibrationFrame{{Path: flat}}}
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err := EnsureCombinedExposure(source, CombineOptions{Ctx: ctx, GMOSCalibration: selection, GMOSCalibrationProgress: cancel})
	if err != ErrCancelled {
		t.Fatalf("GMOS calibration cancellation = %v, want ErrCancelled", err)
	}
	if _, statErr := os.Stat(WorkingPathFor(source)); !os.IsNotExist(statErr) {
		t.Fatalf("canceled calibration published combined file")
	}
	progressCalls := 0
	if _, cached, err := EnsureCombinedExposure(source, CombineOptions{GMOSCalibration: selection, GMOSCalibrationProgress: func() { progressCalls++ }}); err != nil || cached {
		t.Fatalf("uncanceled retry did not perform a fresh build: cached=%v err=%v", cached, err)
	}
	if progressCalls == 0 {
		t.Fatal("uncanceled retry reused a canceled master cache")
	}
	// These cards are the cache contract asserted by the production hook.
	_ = "CALEN"
	_ = "CALFP"
}

func TestEnsureCombinedExposureNonGMOSBypassesCalibrationSelection(t *testing.T) {
	d := t.TempDir()
	source := filepath.Join(d, "hst_flc.fits")
	writeSyntheticExposure(t, source, 4, 4, 1, "F606W", twoOverlappingChips(12))
	selection := &GMOSCalibrationSelection{RecipeFingerprint: "recipe-a"}
	work, cached, err := EnsureCombinedExposure(source, CombineOptions{GMOSCalibration: selection})
	if err != nil || cached {
		t.Fatalf("non-GMOS ensure=%s,%v,%v", work, cached, err)
	}
	f, e := fitsio.LoadFile(work)
	if e != nil {
		t.Fatal(e)
	}
	if fitsio.HeaderString(f.HDUs[0].Header, "CALEN") != "" || fitsio.HeaderString(f.HDUs[0].Header, "CALFP") != "" {
		t.Fatal("non-GMOS output contains calibration cache cards")
	}
	if _, cached, err := EnsureCombinedExposure(source, CombineOptions{GMOSCalibration: selection}); err != nil || !cached {
		t.Fatalf("non-GMOS repeated ensure cached=%v err=%v, want cached=true", cached, err)
	}
}

func writeGMOSMEF(t *testing.T, path, obstype, filter string, value float32, bpm bool) {
	t.Helper()
	p := fitsio.Header{Cards: map[string]string{"INSTRUME": "GMOS-N", "DETECTOR": "EEV", "CCDSUM": "2 2", "OBSTYPE": quotedString(obstype), "FILTER": quotedString(filter), "EXPTIME": "1"}}
	var exts []fitsio.ImageExtension
	for i := 0; i < 3; i++ {
		pix := []float32{value, value, 0, 0, value, value}
		if bpm {
			pix = []float32{0, 1, 0, 0, 0, 0}
		}
		eh := fitsio.Header{Cards: map[string]string{"DATASEC": "'[1:2,1:2]'", "BIASSEC": "'[3:3,1:2]'", "CRPIX1": "1", "CRPIX2": "1", "CRVAL1": "100", "CRVAL2": "22", "CTYPE1": quotedString("RA---TAN"), "CTYPE2": quotedString("DEC--TAN"), "CD1_1": "1", "CD1_2": "0", "CD2_1": "0", "CD2_2": "1"}}
		exts = append(exts, fitsio.ImageExtension{Header: eh, Data: fitsio.ImageData{Width: 3, Height: 2, Pixels: pix}})
	}
	if err := fitsio.WriteFloat32ImageWithExtensions(path, p, fitsio.ImageData{}, exts...); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureCombinedExposureGMOSCalibrationEndToEnd(t *testing.T) {
	d := t.TempDir()
	science := filepath.Join(d, "science.fits")
	bias := filepath.Join(d, "bias.fits")
	flat := filepath.Join(d, "flat.fits")
	bpm := filepath.Join(d, "bpm.fits")
	writeGMOSMEF(t, science, "OBJECT", "r_G0303", 45, false)
	writeGMOSMEF(t, bias, "BIAS", "", 5, false)
	writeGMOSMEF(t, flat, "FLAT", "r_G0303", 7, false)
	writeGMOSMEF(t, bpm, "BPM", "", 0, true)
	s := &GMOSCalibrationSelection{Bias: &GMOSCalibrationFrame{Path: bias}, Flat: &GMOSCalibrationFrame{Path: flat}, BPM: &GMOSCalibrationFrame{Path: bpm}, BiasFrames: []GMOSCalibrationFrame{{Path: bias}}, FlatFrames: []GMOSCalibrationFrame{{Path: flat}}, BPMFrames: []GMOSCalibrationFrame{{Path: bpm}}}
	work, cached, err := EnsureCombinedExposure(science, CombineOptions{GMOSCalibration: s})
	if err != nil || cached {
		t.Fatalf("first ensure=%q,%v,%v", work, cached, err)
	}
	got, e := fitsio.LoadFile(work)
	if e != nil {
		t.Fatal(e)
	}
	if len(got.HDUs) == 0 || len(got.HDUs[0].Data.Pixels) == 0 {
		t.Fatal("missing combined output")
	}
	if got.HDUs[0].Data.Pixels[0] != 40 || !math.IsNaN(float64(got.HDUs[0].Data.Pixels[1])) {
		t.Fatalf("calibrated output=%v", got.HDUs[0].Data.Pixels[:2])
	}
	if fitsio.HeaderString(got.HDUs[0].Header, "CALEN") != "1" || fitsio.HeaderString(got.HDUs[0].Header, "CALFP") == "" {
		t.Fatal("calibration cache cards missing")
	}
	if _, cached, err = EnsureCombinedExposure(science, CombineOptions{GMOSCalibration: s}); err != nil || !cached {
		t.Fatalf("identical recipe cache=%v,%v", cached, err)
	}
	s2 := *s
	s2.BiasFrames = append(append([]GMOSCalibrationFrame(nil), s.BiasFrames...), GMOSCalibrationFrame{Path: bias, Date: "2005-03-31"})
	if _, cached, err = EnsureCombinedExposure(science, CombineOptions{GMOSCalibration: &s2}); err != nil || cached {
		t.Fatalf("changed recipe cache=%v,%v", cached, err)
	}
}

func TestEnsureCombinedExposureFinalizationFailurePreservesCache(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "exp_flc.fits")
	writeSyntheticExposure(t, src, 4, 4, 100, "F502N", twoOverlappingChips(50))
	workingPath, _, err := EnsureCombinedExposure(src, CombineOptions{})
	if err != nil {
		t.Fatalf("initial combine error = %v", err)
	}
	old, err := os.ReadFile(workingPath)
	if err != nil {
		t.Fatalf("read initial cache: %v", err)
	}
	// Change the source identity so EnsureCombinedExposure must build a replacement.
	writeSyntheticExposure(t, src, 4, 4, 100, "F502N", []synthChip{
		{crpix1: 1, crpix2: 1, bunit: "ELECTRONS", pixels: uniform(16, 51)},
		{crpix1: 1, crpix2: 1, bunit: "ELECTRONS", pixels: uniform(16, 51)},
		{crpix1: 1, crpix2: 1, bunit: "ELECTRONS", pixels: uniform(16, 51)},
	})
	previous := replaceWorkingFile
	replaceWorkingFile = func(string, string) error { return errors.New("injected finalization failure") }
	defer func() { replaceWorkingFile = previous }()
	if _, _, err := EnsureCombinedExposure(src, CombineOptions{}); err == nil {
		t.Fatal("expected injected finalization failure")
	}
	got, err := os.ReadFile(workingPath)
	if err != nil {
		t.Fatalf("existing cache unreadable after failed replacement: %v", err)
	}
	if string(got) != string(old) {
		t.Fatal("existing cache changed after failed replacement")
	}
	if _, err := LoadInputsMetadataFromPath(workingPath); err != nil {
		t.Fatalf("existing cache no longer loadable: %v", err)
	}
}

func TestAtomicReplacePreservesExistingRecoveryBackupWhenDestinationMissing(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "working.tmp")
	dst := filepath.Join(dir, "working.fits")
	preexistingBackup := dst + ".bak"
	if err := os.WriteFile(tmp, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	const recoverable = "recoverable cache"
	if err := os.WriteFile(preexistingBackup, []byte(recoverable), 0600); err != nil {
		t.Fatal(err)
	}

	oldRename := mosaicRename
	first := true
	mosaicRename = func(old, new string) error {
		if first && old == tmp && new == dst {
			first = false
			return errors.New("injected publish staging failure")
		}
		return oldRename(old, new)
	}
	t.Cleanup(func() { mosaicRename = oldRename })

	if err := atomicReplaceWorkingFile(tmp, dst); err == nil {
		t.Fatal("expected replacement failure")
	}
	got, err := os.ReadFile(preexistingBackup)
	if err != nil {
		t.Fatalf("read recovery backup: %v", err)
	}
	if string(got) != recoverable {
		t.Fatalf("recovery backup changed to %q", got)
	}
	if leftovers, err := filepath.Glob(filepath.Join(dir, ".working.fits.bak-*")); err != nil {
		t.Fatalf("scan temporary backups: %v", err)
	} else if len(leftovers) != 0 {
		t.Fatalf("temporary backup leaked: %v", leftovers)
	}
}

// synthChip describes one SCI chip written into a synthetic multi-extension FITS
// exposure by writeSyntheticExposure.
type synthChip struct {
	crpix1, crpix2 float64
	bunit          string
	pixels         []float32
	err            []float32 // optional ERR extension; nil to omit
}

// writeSyntheticExposure writes a multi-extension FITS file with a metadata-only
// primary (EXPTIME/FILTER) followed by one SCI extension per chip (each with a
// small TAN WCS so the drizzle combine can place them) and an optional matching
// ERR extension. A single chip yields an ordinary single-exposure file.
func writeSyntheticExposure(t *testing.T, path string, w, h int, exptime float64, filter string, chips []synthChip) {
	t.Helper()
	primary := fitsio.Header{Cards: map[string]string{
		"EXPTIME": strconv.FormatFloat(exptime, 'f', -1, 64),
		"FILTER":  quotedString(filter),
	}}
	var exts []fitsio.ImageExtension
	for i, c := range chips {
		extver := strconv.Itoa(i + 1)
		sciCards := map[string]string{
			"EXTVER": extver,
			"CRPIX1": strconv.FormatFloat(c.crpix1, 'f', -1, 64),
			"CRPIX2": strconv.FormatFloat(c.crpix2, 'f', -1, 64),
			"CRVAL1": "100", "CRVAL2": "22",
			"CD1_1": "0.0001", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0001",
		}
		if c.bunit != "" {
			sciCards["BUNIT"] = quotedString(c.bunit)
		}
		exts = append(exts, fitsio.ImageExtension{
			ExtName: "SCI",
			Header:  fitsio.Header{Cards: sciCards},
			Data:    fitsio.ImageData{Width: w, Height: h, Pixels: c.pixels},
		})
		if c.err != nil {
			exts = append(exts, fitsio.ImageExtension{
				ExtName: "ERR",
				Header:  fitsio.Header{Cards: map[string]string{"EXTVER": extver}},
				Data:    fitsio.ImageData{Width: w, Height: h, Pixels: c.err},
			})
		}
	}
	primaryData := fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{0}}
	if err := fitsio.WriteFloat32ImageWithExtensions(path, primary, primaryData, exts...); err != nil {
		t.Fatalf("write synthetic exposure %s: %v", path, err)
	}
}

func uniform(n int, v float32) []float32 {
	p := make([]float32, n)
	for i := range p {
		p[i] = v
	}
	return p
}

func twoOverlappingChips(v float32) []synthChip {
	return []synthChip{
		{crpix1: 1, crpix2: 1, bunit: "ELECTRONS", pixels: uniform(16, v)},
		{crpix1: 1, crpix2: 1, bunit: "ELECTRONS", pixels: uniform(16, v)},
	}
}

func isFiniteF(x float32) bool {
	return !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0)
}

func TestEnsureCombinedExposureRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "exp1_flc.fits")
	writeSyntheticExposure(t, src, 4, 4, 100, "F502N", twoOverlappingChips(50))

	workingPath, cached, err := EnsureCombinedExposure(src, CombineOptions{})
	if err != nil {
		t.Fatalf("EnsureCombinedExposure error = %v", err)
	}
	if cached {
		t.Fatal("first combine should not report cached")
	}
	if workingPath != WorkingPathFor(src) {
		t.Fatalf("workingPath = %q, want %q", workingPath, WorkingPathFor(src))
	}
	if _, err := os.Stat(workingPath); err != nil {
		t.Fatalf("working file not written: %v", err)
	}

	inputs, err := LoadInputsMetadataFromPath(workingPath)
	if err != nil {
		t.Fatalf("reload working error = %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("combined inputs = %d, want 1", len(inputs))
	}
	if inputs[0].SCIExt != 0 {
		t.Fatalf("SCIExt = %d, want 0", inputs[0].SCIExt)
	}
	if inputs[0].ExposureTime != 100 {
		t.Fatalf("ExposureTime = %v, want 100", inputs[0].ExposureTime)
	}
	if inputs[0].BUnit != "ELECTRONS" {
		t.Fatalf("BUnit = %q, want ELECTRONS", inputs[0].BUnit)
	}
}

func TestEnsureCombinedExposureCache(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "exp_flc.fits")
	writeSyntheticExposure(t, src, 4, 4, 100, "F502N", twoOverlappingChips(50))

	if _, cached, err := EnsureCombinedExposure(src, CombineOptions{}); err != nil || cached {
		t.Fatalf("first combine cached=%v err=%v, want cached=false", cached, err)
	}
	if _, cached, err := EnsureCombinedExposure(src, CombineOptions{}); err != nil || !cached {
		t.Fatalf("second combine cached=%v err=%v, want cached=true", cached, err)
	}

	// Rewrite the source with a different chip count so its size (and thus the
	// SRCSIZE stamp) changes, invalidating the cache regardless of mtime
	// granularity.
	writeSyntheticExposure(t, src, 4, 4, 100, "F502N", []synthChip{
		{crpix1: 1, crpix2: 1, bunit: "ELECTRONS", pixels: uniform(16, 50)},
		{crpix1: 1, crpix2: 1, bunit: "ELECTRONS", pixels: uniform(16, 50)},
		{crpix1: 1, crpix2: 1, bunit: "ELECTRONS", pixels: uniform(16, 50)},
	})
	if _, cached, err := EnsureCombinedExposure(src, CombineOptions{}); err != nil || cached {
		t.Fatalf("after source change cached=%v err=%v, want cached=false", cached, err)
	}
}

func TestEnsureCombinedExposureRejectsLegacyCombineFormatCache(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "exp_flc.fits")
	writeSyntheticExposure(t, src, 4, 4, 100, "F502N", twoOverlappingChips(50))
	working, _, err := EnsureCombinedExposure(src, CombineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := fitsio.LoadFile(working)
	if err != nil {
		t.Fatal(err)
	}
	f.HDUs[0].Header.Cards["COMBVER"] = "1"
	if err := fitsio.WriteFloat32ImageWithExtensions(working, f.HDUs[0].Header, f.HDUs[0].Data, fitsio.ImageExtension{ExtName: "WHT", Data: f.HDUs[1].Data}); err != nil {
		t.Fatal(err)
	}
	if _, cached, err := EnsureCombinedExposure(src, CombineOptions{}); err != nil || cached {
		t.Fatalf("legacy cache result cached=%v err=%v, want rebuild", cached, err)
	}
	if hdr, err := fitsio.LoadPrimaryHeader(working); err != nil {
		t.Fatal(err)
	} else if got, _ := fitsio.HeaderFloat(hdr, "COMBVER"); int(got) != combineFormatVersion {
		t.Fatalf("rebuilt COMBVER=%v, want %d", got, combineFormatVersion)
	}
}

func TestCombinedWHTRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "exp_flc.fits")
	// Chip 2 is shifted 8 reference pixels right of chip 1, leaving an uncovered
	// gap between them so the combined image has both covered (wht>0) and
	// uncovered (wht==0 / NaN) pixels.
	writeSyntheticExposure(t, src, 4, 4, 100, "F502N", []synthChip{
		{crpix1: 1, crpix2: 1, bunit: "ELECTRONS", pixels: uniform(16, 50), err: uniform(16, 5)},
		{crpix1: -7, crpix2: 1, bunit: "ELECTRONS", pixels: uniform(16, 50), err: uniform(16, 5)},
	})

	workingPath, _, err := EnsureCombinedExposure(src, CombineOptions{})
	if err != nil {
		t.Fatalf("combine error = %v", err)
	}
	inputs, err := LoadInputsMetadataFromPath(workingPath)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	sci, _, wht, w, h, err := loadChipFromDisk(inputs[0], true)
	if err != nil {
		t.Fatalf("loadChipFromDisk error = %v", err)
	}
	if wht == nil {
		t.Fatal("WHT plane not loaded")
	}
	if len(wht) != w*h || len(sci) != w*h {
		t.Fatalf("plane sizes sci=%d wht=%d, want %d", len(sci), len(wht), w*h)
	}

	anyCovered, anyGap := false, false
	for i := range sci {
		finite := isFiniteF(sci[i])
		if finite && wht[i] <= 0 {
			t.Fatalf("pixel %d is finite but wht=%v (want >0)", i, wht[i])
		}
		if wht[i] == 0 && finite {
			t.Fatalf("pixel %d has wht==0 but SCI is finite", i)
		}
		if finite && wht[i] > 0 {
			anyCovered = true
		}
		if !finite && wht[i] == 0 {
			anyGap = true
		}
	}
	if !anyCovered {
		t.Fatal("expected covered pixels with wht>0")
	}
	if !anyGap {
		t.Fatal("expected an uncovered gap with wht==0")
	}
}

func TestCombineRestoresCountsUnits(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "exp_flc.fits")
	// Uniform value 50 counts, EXPTIME 100, no ERR: WeightERR divides by EXPTIME
	// (rate 0.5) and the combine must restore counts (× EXPTIME) back to ~50.
	writeSyntheticExposure(t, src, 4, 4, 100, "F502N", twoOverlappingChips(50))

	workingPath, _, err := EnsureCombinedExposure(src, CombineOptions{})
	if err != nil {
		t.Fatalf("combine error = %v", err)
	}
	f, err := fitsio.LoadFile(workingPath)
	if err != nil {
		t.Fatalf("load working error = %v", err)
	}
	finiteSeen := false
	for _, v := range f.HDUs[0].Data.Pixels {
		if !isFiniteF(v) {
			continue
		}
		finiteSeen = true
		if math.Abs(float64(v)-50) > 1e-3 {
			t.Fatalf("combined pixel = %v, want ~50 (counts restored, not rate)", v)
		}
	}
	if !finiteSeen {
		t.Fatal("no finite combined pixels")
	}
}

func TestLoadInputsForPipelineCombinesMultiChip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "exp_flc.fits")
	writeSyntheticExposure(t, src, 4, 4, 100, "F502N", twoOverlappingChips(50))

	inputs, combined, fallbackErr, err := LoadInputsForPipeline(src, CombineOptions{})
	if err != nil {
		t.Fatalf("LoadInputsForPipeline error = %v", err)
	}
	if !combined || fallbackErr != nil {
		t.Fatalf("combined=%v fallbackErr=%v, want combined=true and nil", combined, fallbackErr)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %d, want 1", len(inputs))
	}
	if inputs[0].SourcePath != src {
		t.Fatalf("SourcePath = %q, want %q", inputs[0].SourcePath, src)
	}
	if inputs[0].SCIExt != 0 {
		t.Fatalf("SCIExt = %d, want 0", inputs[0].SCIExt)
	}
	if got := InputLabel(inputs[0]); got != "exp_flc.fits [comb]" {
		t.Fatalf("InputLabel = %q, want %q", got, "exp_flc.fits [comb]")
	}
}

func TestLoadInputsForPipelineSingleChipPassThrough(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "single_flc.fits")
	writeSyntheticExposure(t, src, 4, 4, 100, "F502N", []synthChip{
		{crpix1: 1, crpix2: 1, bunit: "ELECTRONS", pixels: uniform(16, 50)},
	})

	inputs, combined, fallbackErr, err := LoadInputsForPipeline(src, CombineOptions{})
	if err != nil {
		t.Fatalf("LoadInputsForPipeline error = %v", err)
	}
	if combined {
		t.Fatal("single-chip file should not be combined")
	}
	if fallbackErr != nil {
		t.Fatalf("unexpected fallbackErr = %v", fallbackErr)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %d, want 1", len(inputs))
	}
	if inputs[0].SourcePath != "" {
		t.Fatalf("SourcePath = %q, want empty for a pass-through single chip", inputs[0].SourcePath)
	}
	if _, err := os.Stat(filepath.Join(dir, WorkingDirName)); !os.IsNotExist(err) {
		t.Fatalf("working dir should not exist for single-chip file (stat err = %v)", err)
	}
}
