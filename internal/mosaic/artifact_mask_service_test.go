package mosaic

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactMaskRejectsPixelCountOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if _, err := NewZeroArtifactMask(maxInt, 2); err == nil {
		t.Fatal("NewZeroArtifactMask error = nil, want overflow")
	}
	if _, err := ApplyRasterMaskOperations(maxInt, 2, nil); err == nil {
		t.Fatal("ApplyRasterMaskOperations error = nil, want overflow")
	}
	if _, err := RasterizeThresholdMask(nil, maxInt, 2, 0, 0, 0, 0, 0, math.MaxFloat32); err == nil {
		t.Fatal("RasterizeThresholdMask error = nil, want overflow")
	}
	if _, err := ProjectAuthoringMaskToDetector(Input{}, Input{}, MaskOutputGeometry{Width: maxInt, Height: 2}, nil, MaskProjectionOptions{}); err == nil {
		t.Fatal("ProjectAuthoringMaskToDetector error = nil, want overflow")
	}
}

func TestApplyRasterMaskOperationsAddsAndErasesBinaryPixels(t *testing.T) {
	add := []bool{true, true, false, false, false, false}
	erase := []bool{false, true, false, false, false, false}

	mask, err := ApplyRasterMaskOperations(3, 2, []RasterMaskOperation{
		{Mode: MaskOperationAdd, Width: 3, Height: 2, Pixels: add},
		{Mode: MaskOperationErase, Width: 3, Height: 2, Pixels: erase},
	})
	if err != nil {
		t.Fatalf("ApplyRasterMaskOperations error = %v", err)
	}
	want := []bool{true, false, false, false, false, false}
	for i := range want {
		if mask[i] != want[i] {
			t.Fatalf("mask[%d] = %v, want %v in %v", i, mask[i], want[i], mask)
		}
	}
}

func TestArtifactMaskMorphologyHandlesEdges(t *testing.T) {
	mask := []bool{
		true, false, false,
		false, false, false,
		false, false, false,
	}
	dilated, err := DilateArtifactMask(mask, 3, 3, 1)
	if err != nil {
		t.Fatalf("DilateArtifactMask error = %v", err)
	}
	wantDilated := []bool{
		true, true, false,
		true, false, false,
		false, false, false,
	}
	for i := range wantDilated {
		if dilated[i] != wantDilated[i] {
			t.Fatalf("dilated[%d] = %v, want %v in %v", i, dilated[i], wantDilated[i], dilated)
		}
	}

	eroded, err := ErodeArtifactMask(dilated, 3, 3, 1)
	if err != nil {
		t.Fatalf("ErodeArtifactMask error = %v", err)
	}
	for i, v := range eroded {
		if v {
			t.Fatalf("eroded[%d] = true, want all false at clipped edge: %v", i, eroded)
		}
	}
}

func TestMIRIArtifactMaskFilenameUsesSourcePathConvention(t *testing.T) {
	input := miriTestInput(filepath.Join("working", "combined.fits"), 2, 2, []float32{1, 2, 3, 4})
	input.SourcePath = filepath.Join("downloads", "jw02731_cal.fits")

	if got := MIRIArtifactMaskFilename(input); got != "jw02731_cal_miri_mask.fits" {
		t.Fatalf("MIRIArtifactMaskFilename = %q, want source stem convention", got)
	}
}

func TestRowDestripeMaskFilenameUsesSourcePathConvention(t *testing.T) {
	input := makeInput(filepath.Join("working", "combined.fits"), 2, 2, []float32{1, 2, 3, 4}, headerWithCRPIX(1, 1))
	input.PrimaryHeader.Cards = map[string]string{"INSTRUME": "'NIRCAM'"}
	input.SourcePath = filepath.Join("downloads", "jw02731_cal.fits")

	if got := RowDestripeMaskFilename(input); got != "jw02731_cal_rowmask.fits" {
		t.Fatalf("RowDestripeMaskFilename = %q, want source stem convention", got)
	}
}

func TestExportMIRIArtifactMaskAtomicWritesBinaryFITSLoadableByPipeline(t *testing.T) {
	dir := t.TempDir()
	input := miriTestInput("jw12345_cal.fits", 3, 2, []float32{1, 2, 3, 4, 5, 6})
	mask := []bool{true, false, true, false, false, true}

	path, err := ExportMIRIArtifactMaskAtomic(input, dir, mask, false)
	if err != nil {
		t.Fatalf("ExportMIRIArtifactMaskAtomic error = %v", err)
	}
	if filepath.Base(path) != "jw12345_cal_miri_mask.fits" {
		t.Fatalf("export path = %q, want current MIRI mask naming convention", path)
	}

	loaded, loadedPath, err := loadMIRIArtifactMask(input, SkysubOptions{
		MIRIArtifactMask:    true,
		MIRIArtifactMaskDir: dir,
	})
	if err != nil {
		t.Fatalf("loadMIRIArtifactMask error = %v", err)
	}
	if loadedPath != path {
		t.Fatalf("loaded path = %q, want %q", loadedPath, path)
	}
	for i := range mask {
		if loaded[i] != mask[i] {
			t.Fatalf("loaded mask[%d] = %v, want %v in %v", i, loaded[i], mask[i], loaded)
		}
	}
}

func TestExportRowDestripeMaskAtomicWritesBinaryFITSLoadableByPipeline(t *testing.T) {
	dir := t.TempDir()
	input := makeInput("jw12345_cal.fits", 3, 2, []float32{1, 2, 3, 4, 5, 6}, headerWithCRPIX(1, 1))
	input.PrimaryHeader.Cards = map[string]string{"INSTRUME": "'NIRCAM'"}
	mask := []bool{false, true, false, false, true, true}

	path, err := ExportRowDestripeMaskAtomic(input, dir, mask, false)
	if err != nil {
		t.Fatalf("ExportRowDestripeMaskAtomic error = %v", err)
	}
	if filepath.Base(path) != "jw12345_cal_rowmask.fits" {
		t.Fatalf("export path = %q, want row mask naming convention", path)
	}

	loaded, err := loadRowDestripeUserMask(input, SkysubOptions{RowDestripeMaskDir: dir})
	if err != nil {
		t.Fatalf("loadRowDestripeUserMask error = %v", err)
	}
	for i := range mask {
		if loaded[i] != mask[i] {
			t.Fatalf("loaded mask[%d] = %v, want %v in %v", i, loaded[i], mask[i], loaded)
		}
	}
}

func TestExportMIRIArtifactMaskAtomicRejectsOverwriteWithoutChangingExistingFile(t *testing.T) {
	dir := t.TempDir()
	input := miriTestInput("jw12345_cal.fits", 2, 2, []float32{1, 2, 3, 4})
	first := []bool{true, false, false, false}
	second := []bool{false, true, true, true}

	path, err := ExportMIRIArtifactMaskAtomic(input, dir, first, false)
	if err != nil {
		t.Fatalf("initial ExportMIRIArtifactMaskAtomic error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile before error = %v", err)
	}

	if _, err := ExportMIRIArtifactMaskAtomic(input, dir, second, false); err == nil {
		t.Fatal("second ExportMIRIArtifactMaskAtomic error = nil, want existing-file rejection")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("existing mask changed after rejected overwrite")
	}

	if _, err := ExportMIRIArtifactMaskAtomic(input, dir, second, true); err != nil {
		t.Fatalf("overwrite ExportMIRIArtifactMaskAtomic error = %v", err)
	}
	loaded, _, err := loadMIRIArtifactMask(input, SkysubOptions{
		MIRIArtifactMask:    true,
		MIRIArtifactMaskDir: dir,
	})
	if err != nil {
		t.Fatalf("loadMIRIArtifactMask after overwrite error = %v", err)
	}
	for i := range second {
		if loaded[i] != second[i] {
			t.Fatalf("loaded mask[%d] = %v, want overwritten %v in %v", i, loaded[i], second[i], loaded)
		}
	}
}

func TestExportMIRIArtifactMaskAtomicRejectsInvalidTargets(t *testing.T) {
	input := miriTestInput("jw12345_cal.fits", 2, 2, []float32{1, 2, 3, 4})
	if _, err := ExportMIRIArtifactMaskAtomic(input, t.TempDir(), []bool{true, false}, false); err == nil {
		t.Fatal("ExportMIRIArtifactMaskAtomic error = nil, want dimension mismatch")
	}

	nonMIRI := makeInput("hst.fits", 2, 2, []float32{1, 2, 3, 4}, headerWithCRPIX(10, 10))
	if _, err := ExportMIRIArtifactMaskAtomic(nonMIRI, t.TempDir(), []bool{true, false, false, false}, false); err == nil {
		t.Fatal("ExportMIRIArtifactMaskAtomic error = nil, want non-MIRI rejection")
	}
}

func TestExportRowDestripeMaskAtomicRejectsInvalidTargets(t *testing.T) {
	input := makeInput("jw12345_cal.fits", 2, 2, []float32{1, 2, 3, 4}, headerWithCRPIX(1, 1))
	input.PrimaryHeader.Cards = map[string]string{"INSTRUME": "'NIRCAM'"}
	if _, err := ExportRowDestripeMaskAtomic(input, t.TempDir(), []bool{true, false}, false); err == nil {
		t.Fatal("ExportRowDestripeMaskAtomic error = nil, want dimension mismatch")
	}

	nonNIRCam := miriTestInput("miri.fits", 2, 2, []float32{1, 2, 3, 4})
	if _, err := ExportRowDestripeMaskAtomic(nonNIRCam, t.TempDir(), []bool{true, false, false, false}, false); err == nil {
		t.Fatal("ExportRowDestripeMaskAtomic error = nil, want non-NIRCam rejection")
	}
}
