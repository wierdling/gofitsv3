package mosaic

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"gofitsv3/internal/fitsio"
)

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
