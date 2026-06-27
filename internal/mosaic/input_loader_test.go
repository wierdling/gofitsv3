package mosaic

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestCombineSCIHDUsPlacesExtensionsOnSharedCanvas(t *testing.T) {
	primary := fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'"}}
	ref := fitsio.HDU{
		Header: fitsio.Header{Cards: map[string]string{
			"CRPIX1": "10",
			"CRPIX2": "10",
			"CRVAL1": "100",
			"CRVAL2": "22",
			"CD1_1":  "1",
			"CD1_2":  "0",
			"CD2_1":  "0",
			"CD2_2":  "1",
			"EXTVER": "1",
		}},
		Data:    fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{1, 1, 1, 1}},
		ExtName: "SCI",
	}
	shifted := fitsio.HDU{
		Header: fitsio.Header{Cards: map[string]string{
			"CRPIX1": "8",
			"CRPIX2": "10",
			"CRVAL1": "100",
			"CRVAL2": "22",
			"CD1_1":  "1",
			"CD1_2":  "0",
			"CD2_1":  "0",
			"CD2_2":  "1",
			"EXTVER": "2",
		}},
		Data:    fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{2, 2, 2, 2}},
		ExtName: "SCI",
	}

	combined, _, err := combineSCIHDUs("test_flc.fits", primary, []fitsio.HDU{ref, shifted}, &fitsio.File{HDUs: []fitsio.HDU{ref, shifted}})
	if err != nil {
		t.Fatalf("combineSCIHDUs returned error: %v", err)
	}
	if combined.Data.Width != 4 || combined.Data.Height != 2 {
		t.Fatalf("combined size = %dx%d, want 4x2", combined.Data.Width, combined.Data.Height)
	}
	for i, want := range []float32{1, 1, 2, 2, 1, 1, 2, 2} {
		if math.Abs(float64(combined.Data.Pixels[i]-want)) > 1e-6 {
			t.Fatalf("pixel[%d] = %v, want %v", i, combined.Data.Pixels[i], want)
		}
	}
}

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

func TestCombineSCIHDUsShiftsCRPIXForExpandedCanvas(t *testing.T) {
	primary := fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'"}}
	ref := fitsio.HDU{
		Header: fitsio.Header{Cards: map[string]string{
			"CRPIX1": "10",
			"CRPIX2": "10",
			"CRVAL1": "100",
			"CRVAL2": "22",
			"CD1_1":  "1",
			"CD1_2":  "0",
			"CD2_1":  "0",
			"CD2_2":  "1",
		}},
		Data:    fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{1, 1, 1, 1}},
		ExtName: "SCI",
	}
	left := fitsio.HDU{
		Header: fitsio.Header{Cards: map[string]string{
			"CRPIX1": "12",
			"CRPIX2": "10",
			"CRVAL1": "100",
			"CRVAL2": "22",
			"CD1_1":  "1",
			"CD1_2":  "0",
			"CD2_1":  "0",
			"CD2_2":  "1",
		}},
		Data:    fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{2, 2, 2, 2}},
		ExtName: "SCI",
	}

	combined, _, err := combineSCIHDUs("test_flc.fits", primary, []fitsio.HDU{ref, left}, &fitsio.File{HDUs: []fitsio.HDU{ref, left}})
	if err != nil {
		t.Fatalf("combineSCIHDUs returned error: %v", err)
	}
	if got := fitsio.HeaderString(combined.Header, "CRPIX1"); got != "12" {
		t.Fatalf("CRPIX1 = %q, want 12", got)
	}
	if got := fitsio.HeaderString(combined.Header, "CRPIX2"); got != "10" {
		t.Fatalf("CRPIX2 = %q, want 10", got)
	}
}

func TestChipPlacementTransformIgnoresRotationTerms(t *testing.T) {
	refHeader := fitsio.Header{Cards: map[string]string{
		"CRPIX1": "10",
		"CRPIX2": "10",
		"CRVAL1": "100",
		"CRVAL2": "22",
		"CD1_1":  "1",
		"CD1_2":  "0",
		"CD2_1":  "0",
		"CD2_2":  "1",
	}}
	chipHeader := fitsio.Header{Cards: map[string]string{
		"CRPIX1": "8",
		"CRPIX2": "10",
		"CRVAL1": "100",
		"CRVAL2": "22",
		"CD1_1":  "0",
		"CD1_2":  "-1",
		"CD2_1":  "1",
		"CD2_2":  "0",
	}}

	transform, err := chipPlacementTransform(chipHeader, refHeader)
	if err != nil {
		t.Fatalf("chipPlacementTransform returned error: %v", err)
	}
	if transform.A != 1 || transform.B != 0 || transform.D != 0 || transform.E != 1 {
		t.Fatalf("expected translation-only transform, got %+v", transform)
	}
}
