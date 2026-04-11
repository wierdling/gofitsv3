package mosaic

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"gofitsv3/internal/fitsio"
)

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
}

func TestLooksLikeFLC(t *testing.T) {
	if !LooksLikeFLC(filepath.Join("TestImages", "HST", "ick909c1q_flc.fits")) {
		t.Fatalf("expected _flc path to be recognized")
	}
	if LooksLikeFLC(filepath.Join("TestImages", "HST", "ick909030_drz.fits")) {
		t.Fatalf("did not expect _drz path to be recognized as flc")
	}
}

func makeInput(path string, width, height int, pixels []float32, header fitsio.Header) Input {
	return Input{
		Path:          path,
		PrimaryHeader: fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'", "INSTRUME": "'WFC3'"}},
		HDU: fitsio.HDU{
			Header: header,
			Data:   fitsio.ImageData{Width: width, Height: height, Pixels: pixels},
		},
	}
}

func headerWithCRPIX(crpix1, crpix2 float64) fitsio.Header {
	return fitsio.Header{Cards: map[string]string{
		"CRPIX1": strconv.FormatFloat(crpix1, 'f', -1, 64),
		"CRPIX2": strconv.FormatFloat(crpix2, 'f', -1, 64),
		"CRVAL1": "100",
		"CRVAL2": "22",
		"CD1_1":  "1",
		"CD1_2":  "0",
		"CD2_1":  "0",
		"CD2_2":  "1",
	}}
}

func filledPixels(width, height int, value float32) []float32 {
	pixels := make([]float32, width*height)
	for i := range pixels {
		pixels[i] = value
	}
	return pixels
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
