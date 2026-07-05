package mosaic

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestLoadInputsFromPathWFPC2FLTRealFiles(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "TestImages", "WFPC2", "*_flt.fits"))
	if err != nil {
		t.Fatalf("Glob error = %v", err)
	}
	if len(paths) == 0 {
		t.Skip("no WFPC2 FLT test images found")
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			inputs, err := LoadInputsFromPath(path)
			if err != nil {
				t.Fatalf("LoadInputsFromPath error = %v", err)
			}
			if len(inputs) != 4 {
				t.Fatalf("len(inputs) = %d, want 4 WFPC2 science chips", len(inputs))
			}
			for i, input := range inputs {
				wantExt := i + 1
				if input.SCIExt != wantExt {
					t.Fatalf("input[%d].SCIExt = %d, want %d", i, input.SCIExt, wantExt)
				}
				if got := fitsio.HeaderString(input.PrimaryHeader, "INSTRUME"); got != "WFPC2" {
					t.Fatalf("input[%d] INSTRUME = %q, want WFPC2", i, got)
				}
				if input.HDU.ExtName != "SCI" {
					t.Fatalf("input[%d].HDU.ExtName = %q, want SCI", i, input.HDU.ExtName)
				}
				if input.HDU.Data.Width != 800 || input.HDU.Data.Height != 800 {
					t.Fatalf("input[%d] size = %dx%d, want 800x800", i, input.HDU.Data.Width, input.HDU.Data.Height)
				}
				if len(input.HDU.Data.Pixels) != 800*800 {
					t.Fatalf("input[%d] pixel count = %d, want %d", i, len(input.HDU.Data.Pixels), 800*800)
				}
				if len(input.ERRPixels) != len(input.HDU.Data.Pixels) {
					t.Fatalf("input[%d] ERR pixel count = %d, want %d", i, len(input.ERRPixels), len(input.HDU.Data.Pixels))
				}
			}
		})
	}
}

func TestLoadInputsMetadataMatchesFullLoadButOmitsPixels(t *testing.T) {
	var paths []string
	for _, pat := range []string{
		filepath.Join("..", "..", "TestImages", "*_flt.fits"),
		filepath.Join("..", "..", "TestImages", "*_cal.fits"),
		filepath.Join("..", "..", "TestImages", "WFPC2", "*_flt.fits"),
	} {
		matches, err := filepath.Glob(pat)
		if err != nil {
			t.Fatalf("Glob error = %v", err)
		}
		paths = append(paths, matches...)
	}
	if len(paths) == 0 {
		t.Skip("no FLT/CAL test images found")
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			full, err := LoadInputsFromPath(path)
			if err != nil {
				t.Fatalf("LoadInputsFromPath error = %v", err)
			}
			meta, err := LoadInputsMetadataFromPath(path)
			if err != nil {
				t.Fatalf("LoadInputsMetadataFromPath error = %v", err)
			}
			if len(meta) != len(full) {
				t.Fatalf("metadata input count = %d, want %d", len(meta), len(full))
			}
			for i := range full {
				if meta[i].SCIExt != full[i].SCIExt {
					t.Fatalf("input[%d].SCIExt = %d, want %d", i, meta[i].SCIExt, full[i].SCIExt)
				}
				// Dimensions come from the header, so they must match the full load.
				if meta[i].HDU.Data.Width != full[i].HDU.Data.Width || meta[i].HDU.Data.Height != full[i].HDU.Data.Height {
					t.Fatalf("input[%d] metadata size = %dx%d, want %dx%d", i,
						meta[i].HDU.Data.Width, meta[i].HDU.Data.Height, full[i].HDU.Data.Width, full[i].HDU.Data.Height)
				}
				// Pixels (the expensive part) must NOT be loaded.
				if meta[i].HDU.Data.Pixels != nil {
					t.Fatalf("input[%d] metadata unexpectedly carries %d SCI pixels", i, len(meta[i].HDU.Data.Pixels))
				}
				if meta[i].ERRPixels != nil {
					t.Fatalf("input[%d] metadata unexpectedly carries %d ERR pixels", i, len(meta[i].ERRPixels))
				}
				// Header-derived metadata must be identical (proves the header
				// reader stayed byte-aligned past the skipped data blocks).
				if meta[i].ExposureTime != full[i].ExposureTime {
					t.Fatalf("input[%d] ExposureTime = %v, want %v", i, meta[i].ExposureTime, full[i].ExposureTime)
				}
				if meta[i].DateObs != full[i].DateObs {
					t.Fatalf("input[%d] DateObs = %q, want %q", i, meta[i].DateObs, full[i].DateObs)
				}
				if fitsio.HeaderString(meta[i].HDU.Header, "EXTVER") != fitsio.HeaderString(full[i].HDU.Header, "EXTVER") {
					t.Fatalf("input[%d] EXTVER header mismatch", i)
				}
				// Distortion tables must be loaded when the full path has them.
				if (full[i].D2IX == nil) != (meta[i].D2IX == nil) || (full[i].D2IY == nil) != (meta[i].D2IY == nil) {
					t.Fatalf("input[%d] D2I table presence mismatch (full X=%v Y=%v, meta X=%v Y=%v)",
						i, full[i].D2IX != nil, full[i].D2IY != nil, meta[i].D2IX != nil, meta[i].D2IY != nil)
				}
			}
		})
	}
}

// TestLoadCleanedSCIForExtractionMatchesFullLoad verifies the chip-selective
// extraction loader returns pixels byte-identical to a full LoadInputsFromPath
// for every SCI chip — proving the memory optimization does not change the star
// catalog that alignment is built from.
func TestLoadCleanedSCIForExtractionMatchesFullLoad(t *testing.T) {
	var paths []string
	for _, pat := range []string{
		filepath.Join("..", "..", "TestImages", "*_flt.fits"),
		filepath.Join("..", "..", "TestImages", "*_cal.fits"),
		filepath.Join("..", "..", "TestImages", "NIRCAM_SHORT", "*_cal.fits"),
	} {
		m, _ := filepath.Glob(pat)
		paths = append(paths, m...)
	}
	if len(paths) == 0 {
		t.Skip("no FLT/CAL test images found")
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			full, err := LoadInputsFromPath(path)
			if err != nil {
				t.Fatalf("LoadInputsFromPath error = %v", err)
			}
			meta, err := LoadInputsMetadataFromPath(path)
			if err != nil {
				t.Fatalf("LoadInputsMetadataFromPath error = %v", err)
			}
			if len(meta) != len(full) {
				t.Fatalf("input count: metadata %d, full %d", len(meta), len(full))
			}
			for i := range full {
				got, w, h, err := loadCleanedSCIForExtraction(meta[i])
				if err != nil {
					t.Fatalf("input[%d] loadCleanedSCIForExtraction error = %v", i, err)
				}
				if w != full[i].HDU.Data.Width || h != full[i].HDU.Data.Height {
					t.Fatalf("input[%d] dims = %dx%d, want %dx%d", i, w, h, full[i].HDU.Data.Width, full[i].HDU.Data.Height)
				}
				want := full[i].HDU.Data.Pixels
				if len(got) != len(want) {
					t.Fatalf("input[%d] pixel count = %d, want %d", i, len(got), len(want))
				}
				for k := range want {
					a, b := got[k], want[k]
					if a != b && !(math.IsNaN(float64(a)) && math.IsNaN(float64(b))) {
						t.Fatalf("input[%d] cleaned pixel[%d] = %v, want %v", i, k, a, b)
					}
				}
			}
		})
	}
}

// TestLoadFrameFromDiskMatchesFullLoad verifies the chip-selective Build reload
// path returns SCI and ERR pixels byte-identical to a full LoadInputsFromPath for
// every chip, so streaming a multi-chip mosaic produces the same drizzle input.
func TestLoadFrameFromDiskMatchesFullLoad(t *testing.T) {
	var paths []string
	for _, pat := range []string{
		filepath.Join("..", "..", "TestImages", "*_flt.fits"),
		filepath.Join("..", "..", "TestImages", "*_cal.fits"),
		filepath.Join("..", "..", "TestImages", "NIRCAM_SHORT", "*_cal.fits"),
	} {
		m, _ := filepath.Glob(pat)
		paths = append(paths, m...)
	}
	if len(paths) == 0 {
		t.Skip("no FLT/CAL test images found")
	}

	eq := func(a, b []float32) (int, bool) {
		if len(a) != len(b) {
			return -1, false
		}
		for k := range a {
			if a[k] != b[k] && !(math.IsNaN(float64(a[k])) && math.IsNaN(float64(b[k]))) {
				return k, false
			}
		}
		return 0, true
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			full, err := LoadInputsFromPath(path)
			if err != nil {
				t.Fatalf("LoadInputsFromPath error = %v", err)
			}
			meta, err := LoadInputsMetadataFromPath(path)
			if err != nil {
				t.Fatalf("LoadInputsMetadataFromPath error = %v", err)
			}
			if len(meta) != len(full) {
				t.Fatalf("input count: metadata %d, full %d", len(meta), len(full))
			}
			for i := range full {
				sci, errPix, _, derr := loadFrameFromDisk(meta[i])
				if derr != nil {
					t.Fatalf("input[%d] loadFrameFromDisk error = %v", i, derr)
				}
				if k, ok := eq(sci, full[i].HDU.Data.Pixels); !ok {
					t.Fatalf("input[%d] SCI mismatch at %d", i, k)
				}
				if k, ok := eq(errPix, full[i].ERRPixels); !ok {
					t.Fatalf("input[%d] ERR mismatch at %d (got len %d, want len %d)", i, k, len(errPix), len(full[i].ERRPixels))
				}
			}
		})
	}
}

func TestBuildWFPC2FLTRealFilesKeepsReasonableCanvas(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "TestImages", "WFPC2", "*_flt.fits"))
	if err != nil {
		t.Fatalf("Glob error = %v", err)
	}
	if len(paths) == 0 {
		t.Skip("no WFPC2 FLT test images found")
	}

	inputs, err := LoadInputsFromPath(paths[0])
	if err != nil {
		t.Fatalf("LoadInputsFromPath error = %v", err)
	}
	result, err := Build(inputs, Options{Scale: 1, PixFrac: 1, CRMethod: CRMethodNone})
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	if result.Width < 1000 || result.Height < 1000 {
		t.Fatalf("result size = %dx%d, want a multi-chip WFPC2 canvas", result.Width, result.Height)
	}
	if result.Width > 5000 || result.Height > 5000 {
		t.Fatalf("result size = %dx%d, want bounded WFPC2 canvas", result.Width, result.Height)
	}
	if len(result.Pixels) != result.Width*result.Height {
		t.Fatalf("pixel count = %d, want %d", len(result.Pixels), result.Width*result.Height)
	}
	if got := fitsio.HeaderString(result.OutputHeader, "CTYPE1"); got != "RA---TAN" {
		t.Fatalf("CTYPE1 = %q, want RA---TAN", got)
	}
	if got := fitsio.HeaderString(result.OutputHeader, "CTYPE2"); got != "DEC--TAN" {
		t.Fatalf("CTYPE2 = %q, want DEC--TAN", got)
	}
	if _, ok := result.OutputHeader.Cards["A_ORDER"]; ok {
		t.Fatal("output header kept SIP distortion cards")
	}
	if _, ok := result.OutputHeader.Cards["D2IMDIS2"]; ok {
		t.Fatal("output header kept D2IM distortion cards")
	}
	if len(result.InputFootprints) != 4 {
		t.Fatalf("len(InputFootprints) = %d, want 4", len(result.InputFootprints))
	}
	for i, fp := range result.InputFootprints {
		top := footprintEdgeLength(fp[0], fp[1])
		bottom := footprintEdgeLength(fp[2], fp[3])
		left := footprintEdgeLength(fp[0], fp[2])
		right := footprintEdgeLength(fp[1], fp[3])
		if ratioOutside(top, left, 0.95, 1.05) || ratioOutside(top, bottom, 0.95, 1.05) || ratioOutside(left, right, 0.95, 1.05) {
			t.Fatalf("footprint[%d] is not square-like: top=%.1f bottom=%.1f left=%.1f right=%.1f", i, top, bottom, left, right)
		}
		if coverage := finiteCoverageInsideFootprint(result.Pixels, result.Width, result.Height, fp); coverage < 0.85 {
			t.Fatalf("footprint[%d] finite coverage = %.3f, want at least 0.85", i, coverage)
		}
	}
	outPath := filepath.Join(t.TempDir(), "wfpc2_drizzle.fits")
	if err := SaveResultFITS(outPath, result); err != nil {
		t.Fatalf("SaveResultFITS error = %v", err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("Stat saved FITS error = %v", err)
	}
	if info.Size() < 1_000_000 {
		t.Fatalf("saved FITS size = %d bytes, want a real image payload", info.Size())
	}
}

func TestDQEdgeNoDataMaskMarksHeavilyFlaggedRowsAndColumns(t *testing.T) {
	mask := []bool{
		true, true, true, true,
		true, false, false, false,
		true, false, false, false,
		true, false, false, false,
	}
	edge := dqEdgeNoDataMask(mask, 4, 4, 0.75)
	if edge == nil {
		t.Fatal("dqEdgeNoDataMask = nil, want edge mask")
	}
	for _, idx := range []int{0, 1, 2, 3, 4, 8, 12} {
		if !edge[idx] {
			t.Fatalf("edge[%d] = false, want true", idx)
		}
	}
	for _, idx := range []int{5, 6, 9, 10} {
		if edge[idx] {
			t.Fatalf("edge[%d] = true, want false", idx)
		}
	}
}

func footprintEdgeLength(a, b [2]float64) float64 {
	return math.Hypot(a[0]-b[0], a[1]-b[1])
}

func ratioOutside(a, b, minRatio, maxRatio float64) bool {
	if a <= 0 || b <= 0 {
		return true
	}
	ratio := a / b
	return ratio < minRatio || ratio > maxRatio
}

func finiteCoverageInsideFootprint(pixels []float32, width, height int, fp [4][2]float64) float64 {
	minX, maxX := footprintMinMax(fp[0][0], fp[1][0], fp[2][0], fp[3][0])
	minY, maxY := footprintMinMax(fp[0][1], fp[1][1], fp[2][1], fp[3][1])
	x0 := maxIntForFootprint(0, int(math.Floor(minX)))
	y0 := maxIntForFootprint(0, int(math.Floor(minY)))
	x1 := minIntForFootprint(width-1, int(math.Ceil(maxX)))
	y1 := minIntForFootprint(height-1, int(math.Ceil(maxY)))
	inside := 0
	finite := 0
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if !pointInFootprint(float64(x)+0.5, float64(y)+0.5, fp) {
				continue
			}
			inside++
			idx := y*width + x
			if idx < len(pixels) && !math.IsNaN(float64(pixels[idx])) && !math.IsInf(float64(pixels[idx]), 0) {
				finite++
			}
		}
	}
	if inside == 0 {
		return 0
	}
	return float64(finite) / float64(inside)
}

func pointInFootprint(x, y float64, fp [4][2]float64) bool {
	poly := [4][2]float64{fp[0], fp[1], fp[3], fp[2]}
	inside := false
	j := len(poly) - 1
	for i := range poly {
		xi, yi := poly[i][0], poly[i][1]
		xj, yj := poly[j][0], poly[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
		j = i
	}
	return inside
}

func footprintMinMax(vals ...float64) (float64, float64) {
	minV, maxV := vals[0], vals[0]
	for _, v := range vals[1:] {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	return minV, maxV
}

func minIntForFootprint(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxIntForFootprint(a, b int) int {
	if a > b {
		return a
	}
	return b
}

