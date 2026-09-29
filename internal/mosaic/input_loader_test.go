package mosaic

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"context"
	"gofitsv3/internal/astroio"
	"gofitsv3/internal/fitsio"
)

func TestLoadRealMIRIAsdfUsesNativeGWCS(t *testing.T) {
	path := filepath.Join("..", "..", "TestImages", "asdf", "jw09548001001_02101_00001_mirimage_cal.asdf")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real ASDF fixture unavailable: %v", err)
	}
	inputs, err := LoadInputsMetadataFromPath(path)
	if err != nil {
		t.Fatalf("LoadInputsMetadataFromPath: %v", err)
	}
	if len(inputs) != 1 || inputs[0].NativeGWCS == nil {
		t.Fatalf("inputs=%d native=%v, want one native GWCS input", len(inputs), len(inputs) == 1 && inputs[0].NativeGWCS != nil)
	}
	if inputs[0].HDU.Data.Width != 1032 || inputs[0].HDU.Data.Height != 1024 {
		t.Fatalf("dimensions=%dx%d, want 1032x1024", inputs[0].HDU.Data.Width, inputs[0].HDU.Data.Height)
	}
	if inputs[0].NativeGWCSProfile != "MIRI/MIRIMAGE" || inputs[0].HDU.Header.Cards["GWCSMODEL"] != "MIRI_NATIVE_GWCS" {
		t.Fatalf("native GWCS profile/marker = %q/%q, want MIRI/MIRIMAGE/MIRI_NATIVE_GWCS", inputs[0].NativeGWCSProfile, inputs[0].HDU.Header.Cards["GWCSMODEL"])
	}
	source, err := astroio.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("astroio.Open: %v", err)
	}
	meta, err := source.Metadata(context.Background())
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	mapper, err := newInputMapper(inputs[0], inputs[0])
	if err != nil {
		t.Fatalf("newInputMapper: %v", err)
	}
	if meta.NativeGWCS == nil || inputs[0].NativeGWCS == nil {
		t.Fatal("native GWCS evaluator was not retained")
	}
	points := [][2]float64{{0, 0}, {512, 512}, {1031, 1023}}
	mapped := make([][2]float64, len(points))
	for i, point := range points {
		x, y := mapper.MapPixel(point[0], point[1])
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			t.Fatalf("native map returned non-finite coordinates for pixel (%v,%v): (%v,%v)", point[0], point[1], x, y)
		}
		mapped[i] = [2]float64{x, y}
	}
	if mapped[0] == mapped[1] || mapped[1] == mapped[2] {
		t.Fatalf("native map did not produce useful spatial variation: %#v", mapped)
	}
}

func TestLoadRealNIRCamAsdfUsesNativeGWCS(t *testing.T) {
	path := filepath.Join("..", "..", "TestImages", "asdf", "nircam", "jw09548002001_02101_00001_nrcb1_cal.asdf")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real ASDF fixture unavailable: %v", err)
	}
	inputs, err := LoadInputsMetadataFromPath(path)
	if err != nil {
		t.Fatalf("LoadInputsMetadataFromPath: %v", err)
	}
	if len(inputs) != 1 || inputs[0].NativeGWCS == nil || inputs[0].NativeGWCSProfile == "" {
		t.Fatalf("inputs=%d native=%v profile=%q", len(inputs), len(inputs) == 1 && inputs[0].NativeGWCS != nil, inputs[0].NativeGWCSProfile)
	}
	if inputs[0].PrimaryHeader.Cards["GWCSMODEL"] != "NIRCAM_NATIVE_GWCS" || inputs[0].PrimaryHeader.Cards["DETECTOR"] != "NRCB1" {
		t.Fatalf("NIRCam profile/header not retained: %+v", inputs[0].PrimaryHeader.Cards)
	}
}

func TestASDFMosaicHeaderRequiresLinearWCS(t *testing.T) {
	if _, err := asdfMosaicHeader(map[string]string{"CRPIX1": "1", "CRPIX2": "1", "CRVAL1": "1", "CRVAL2": "2", "CDELT1": "-0.00001"}); err == nil {
		t.Fatal("asdfMosaicHeader accepted incomplete WCS")
	}
	h, err := asdfMosaicHeader(map[string]string{"CRPIX1": "1", "CRPIX2": "2", "CRVAL1": "10", "CRVAL2": "20", "CDELT1": "-0.00001", "CDELT2": "0.00001", "FILTER": "F200W"})
	if err != nil {
		t.Fatalf("asdfMosaicHeader: %v", err)
	}
	if h.Cards["CRVAL1"] != "10" || h.Cards["FILTER"] != "F200W" {
		t.Fatalf("unexpected ASDF WCS header: %#v", h.Cards)
	}
}

func TestASDFMosaicHeaderRejectsInvalidGeometryAndCarriesCTYPE(t *testing.T) {
	base := map[string]string{"CRPIX1": "1", "CRPIX2": "2", "CRVAL1": "10", "CRVAL2": "20", "CDELT1": "-0.00001", "CDELT2": "0.00001", "CTYPE1": "RA---TAN", "CTYPE2": "DEC--TAN"}
	h, err := asdfMosaicHeader(base)
	if err != nil || h.Cards["CTYPE1"] != "RA---TAN" || h.Cards["CTYPE2"] != "DEC--TAN" {
		t.Fatalf("header=%#v err=%v, want valid CTYPE cards", h.Cards, err)
	}
	for _, key := range []string{"CDELT1", "CRPIX1"} {
		bad := map[string]string{}
		for k, v := range base {
			bad[k] = v
		}
		bad[key] = "NaN"
		if _, err := asdfMosaicHeader(bad); err == nil {
			t.Fatalf("accepted invalid %s", key)
		}
	}
	singular := map[string]string{}
	for k, v := range base {
		singular[k] = v
	}
	singular["PC1_1"], singular["PC1_2"], singular["PC2_1"], singular["PC2_2"] = "1", "2", "2", "4"
	if _, err := asdfMosaicHeader(singular); err == nil {
		t.Fatal("accepted singular PC geometry")
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

func TestJWSTDQPixelsAreExcludedNotRepaired(t *testing.T) {
	path := writeSyntheticDQMEF(t, "jwst_dq.fits",
		map[string]string{"INSTRUME": "'NIRCAM'", "DETECTOR": "'NRCA1'"},
		[]float32{10, 20, 30, 40},
		[]float32{1, 512, 2, 0},
	)

	inputs, err := LoadInputsFromPath(path)
	if err != nil {
		t.Fatalf("LoadInputsFromPath error = %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("len(inputs) = %d, want 1", len(inputs))
	}
	pixels := inputs[0].HDU.Data.Pixels
	for _, idx := range []int{0, 1} {
		if !math.IsNaN(float64(pixels[idx])) {
			t.Fatalf("pixel[%d] = %v, want NaN for JWST unusable DQ", idx, pixels[idx])
		}
	}
	if pixels[2] != 30 {
		t.Fatalf("pixel[2] = %v, want informational DQ bit to remain usable", pixels[2])
	}
	if pixels[3] != 40 {
		t.Fatalf("pixel[3] = %v, want unflagged pixel unchanged", pixels[3])
	}

	result, err := Build(inputs, Options{Scale: 1, CRMethod: CRMethodNone})
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	for _, idx := range []int{0, 1} {
		if result.Weights[idx] != 0 {
			t.Fatalf("result weight[%d] = %v, want 0 for excluded JWST pixel", idx, result.Weights[idx])
		}
		if !math.IsNaN(float64(result.Pixels[idx])) {
			t.Fatalf("result pixel[%d] = %v, want NaN for excluded JWST pixel", idx, result.Pixels[idx])
		}
	}
}

func TestJWSTDQEagerAndLazyMasksMatch(t *testing.T) {
	path := writeSyntheticDQMEF(t, "jwst_lazy_dq.fits",
		map[string]string{"INSTRUME": "'MIRI'", "DETECTOR": "'MIRIMAGE'"},
		[]float32{1, 2, 3, 4},
		[]float32{0, 1, 512, 4},
	)

	full, err := LoadInputsFromPath(path)
	if err != nil {
		t.Fatalf("LoadInputsFromPath error = %v", err)
	}
	meta, err := LoadInputsMetadataFromPath(path)
	if err != nil {
		t.Fatalf("LoadInputsMetadataFromPath error = %v", err)
	}
	got, _, _, w, h, err := loadChipFromDisk(meta[0], false)
	if err != nil {
		t.Fatalf("loadChipFromDisk error = %v", err)
	}
	if w != full[0].HDU.Data.Width || h != full[0].HDU.Data.Height {
		t.Fatalf("dims = %dx%d, want %dx%d", w, h, full[0].HDU.Data.Width, full[0].HDU.Data.Height)
	}
	for i, want := range full[0].HDU.Data.Pixels {
		if got[i] != want && !(math.IsNaN(float64(got[i])) && math.IsNaN(float64(want))) {
			t.Fatalf("pixel[%d] lazy = %v, eager = %v", i, got[i], want)
		}
	}
	if got[3] != 4 {
		t.Fatalf("pixel[3] = %v, want MIRI informational DQ bit to remain usable", got[3])
	}
}

func TestHSTDQPixelsStillUseRepairPolicy(t *testing.T) {
	path := writeSyntheticDQMEF(t, "hst_dq.fits",
		map[string]string{"INSTRUME": "'WFC3'", "DETECTOR": "'IR'"},
		[]float32{
			1, 2, 3, 4, 5,
			6, 7, 8, 9, 10,
			11, 12, 1000, 14, 15,
			16, 17, 18, 19, 20,
			21, 22, 23, 24, 25,
		},
		[]float32{
			0, 0, 0, 0, 0,
			0, 0, 0, 0, 0,
			0, 0, 16, 0, 0,
			0, 0, 0, 0, 0,
			0, 0, 0, 0, 0,
		},
	)

	inputs, err := LoadInputsFromPath(path)
	if err != nil {
		t.Fatalf("LoadInputsFromPath error = %v", err)
	}
	got := inputs[0].HDU.Data.Pixels[12]
	if math.IsNaN(float64(got)) {
		t.Fatal("HST repaired pixel is NaN, want interpolation repair")
	}
	if got == 1000 {
		t.Fatal("HST repaired pixel kept original bad value")
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

func TestAuxMatchingRequiresSCIEXTVERUnlessSCIUnversioned(t *testing.T) {
	sci := fitsio.HDU{ExtName: "SCI", Header: fitsio.Header{Cards: map[string]string{"EXTVER": "2"}}, Data: fitsio.ImageData{Width: 2, Height: 2}}
	dqWrong := fitsio.HDU{ExtName: "DQ", Header: fitsio.Header{Cards: map[string]string{"EXTVER": "1"}}, Data: fitsio.ImageData{Width: 2, Height: 2}}
	file := &fitsio.File{HDUs: []fitsio.HDU{dqWrong}}
	if got := matchingDQHDU(file, sci); got != nil {
		t.Fatal("matched DQ with wrong EXTVER")
	}
	sci.Header.Cards = map[string]string{}
	if got := matchingDQHDU(file, sci); got == nil {
		t.Fatal("unversioned SCI did not use size fallback")
	}
	err := fitsio.HDU{ExtName: "ERR", Header: fitsio.Header{Cards: map[string]string{"EXTVER": "1"}}, Data: fitsio.ImageData{Pixels: []float32{1}}}
	file.HDUs = []fitsio.HDU{err}
	if got := loadERRPixels(file, 2); got != nil {
		t.Fatal("versioned SCI used unversioned ERR fallback")
	}
	if got := loadERRPixels(file, 2, true); len(got) != 1 {
		t.Fatal("unversioned SCI did not use ERR fallback")
	}
}

func writeSyntheticDQMEF(t *testing.T, name string, primaryCards map[string]string, sciPixels, dqPixels []float32) string {
	t.Helper()
	if len(sciPixels) != len(dqPixels) {
		t.Fatalf("synthetic SCI/DQ length mismatch: %d vs %d", len(sciPixels), len(dqPixels))
	}
	width := 2
	if len(sciPixels) == 25 {
		width = 5
	}
	height := len(sciPixels) / width
	header := fitsio.Header{Cards: map[string]string{
		"FILTER":  "'F200W'",
		"EXPTIME": "100",
	}}
	for k, v := range primaryCards {
		header.Cards[k] = v
	}
	sciHeader := headerWithCRPIX(10, 10)
	sciHeader.Cards["EXTVER"] = "1"
	dqHeader := fitsio.Header{Cards: map[string]string{"EXTVER": "1"}}
	path := filepath.Join(t.TempDir(), name)
	err := fitsio.WriteFloat32ImageWithExtensions(path, header, fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{0}},
		fitsio.ImageExtension{
			ExtName: "SCI",
			Header:  sciHeader,
			Data:    fitsio.ImageData{Width: width, Height: height, Pixels: sciPixels},
		},
		fitsio.ImageExtension{
			ExtName: "DQ",
			Header:  dqHeader,
			Data:    fitsio.ImageData{Width: width, Height: height, Pixels: dqPixels},
		},
	)
	if err != nil {
		t.Fatalf("WriteFloat32ImageWithExtensions error = %v", err)
	}
	return path
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
