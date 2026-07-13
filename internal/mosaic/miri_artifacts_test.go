package mosaic

import (
	"math"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestBuildMIRIArtifactMaskExcludesPixelsFromOutput(t *testing.T) {
	maskPath := writeMaskFITS(t, "miri_mask.fits", 2, 2, []float32{1, 0, 0, 1})
	input := miriTestInput("miri_cal.fits", 2, 2, []float32{10, 20, 30, 40})

	result, err := Build([]Input{input}, Options{
		Scale:    1,
		CRMethod: CRMethodNone,
		Skysub: SkysubOptions{
			MIRIArtifactMask:     true,
			MIRIArtifactMaskPath: maskPath,
		},
	})
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	for _, idx := range []int{0, 3} {
		if result.Weights[idx] != 0 {
			t.Fatalf("weight[%d] = %v, want 0 for MIRI artifact mask", idx, result.Weights[idx])
		}
		if !math.IsNaN(float64(result.Pixels[idx])) {
			t.Fatalf("pixel[%d] = %v, want NaN for MIRI artifact mask", idx, result.Pixels[idx])
		}
	}
	if result.Pixels[1] != 20 || result.Pixels[2] != 30 {
		t.Fatalf("unmasked pixels = %v, want 20 and 30", result.Pixels)
	}
}

func TestMIRIArtifactMaskDirectoryUsesInputBasename(t *testing.T) {
	dir := t.TempDir()
	maskPath := filepath.Join(dir, "jw12345_miri_mask.fits")
	if err := fitsio.WriteFloat32Image(maskPath, fitsio.Header{}, fitsio.ImageData{
		Width:  2,
		Height: 2,
		Pixels: []float32{0, 1, 0, 0},
	}); err != nil {
		t.Fatalf("WriteFloat32Image error = %v", err)
	}
	input := miriTestInput(filepath.Join(t.TempDir(), "jw12345.fits"), 2, 2, []float32{1, 2, 3, 4})
	mask, path, err := loadMIRIArtifactMask(input, SkysubOptions{
		MIRIArtifactMask:    true,
		MIRIArtifactMaskDir: dir,
	})
	if err != nil {
		t.Fatalf("loadMIRIArtifactMask error = %v", err)
	}
	if path != maskPath {
		t.Fatalf("mask path = %q, want %q", path, maskPath)
	}
	if len(mask) != 4 || !mask[1] || mask[0] || mask[2] || mask[3] {
		t.Fatalf("mask = %v, want only pixel 1 masked", mask)
	}
}

func TestMIRIArtifactMaskSkipsNonMIRI(t *testing.T) {
	input := makeInput("hst.fits", 2, 2, []float32{1, 2, 3, 4}, headerWithCRPIX(10, 10))
	sci, _, _, err := prepareFramePixels(plannedInput{input: input}, Options{Skysub: SkysubOptions{
		MIRIArtifactMask:     true,
		MIRIArtifactMaskPath: filepath.Join(t.TempDir(), "does_not_exist.fits"),
	}}, 0, skyPlane{})
	if err != nil {
		t.Fatalf("prepareFramePixels error = %v", err)
	}
	for i, want := range input.HDU.Data.Pixels {
		if sci[i] != want {
			t.Fatalf("pixel[%d] = %v, want unchanged %v", i, sci[i], want)
		}
	}
}

func TestMIRIArtifactMaskMismatchErrorsBeforeMutation(t *testing.T) {
	maskPath := writeMaskFITS(t, "bad_miri_mask.fits", 3, 2, filledPixels(3, 2, 1))
	pixels := []float32{1, 2, 3, 4}
	original := append([]float32(nil), pixels...)
	input := miriTestInput("miri_bad_cal.fits", 2, 2, pixels)

	_, _, _, err := prepareFramePixels(plannedInput{input: input}, Options{Skysub: SkysubOptions{
		MIRIArtifactMask:     true,
		MIRIArtifactMaskPath: maskPath,
	}}, 0, skyPlane{})
	if err == nil {
		t.Fatal("prepareFramePixels error = nil, want mask dimension mismatch")
	}
	for i := range pixels {
		if pixels[i] != original[i] {
			t.Fatalf("pixel[%d] changed before mask error: got %v, want %v", i, pixels[i], original[i])
		}
	}
}

func miriTestInput(path string, width, height int, pixels []float32) Input {
	input := makeInput(path, width, height, pixels, headerWithCRPIX(10, 10))
	input.PrimaryHeader = fitsio.Header{Cards: map[string]string{
		"INSTRUME": "'MIRI'",
		"DETECTOR": "'MIRIMAGE'",
		"FILTER":   "'F770W'",
		"EXPTIME":  "100",
	}}
	input.BUnit = "MJy/sr"
	return input
}

func writeMaskFITS(t *testing.T, name string, width, height int, pixels []float32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := fitsio.WriteFloat32Image(path, fitsio.Header{}, fitsio.ImageData{Width: width, Height: height, Pixels: pixels}); err != nil {
		t.Fatalf("WriteFloat32Image error = %v", err)
	}
	return path
}
