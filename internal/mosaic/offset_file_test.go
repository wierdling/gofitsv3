package mosaic

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

func TestNormalizeFilterName(t *testing.T) {
	got := normalizeFilterName(" 'F673N  '            / element selected from filter wheel")
	if got != "F673N" {
		t.Fatalf("normalizeFilterName = %q, want F673N", got)
	}
	if OffsetFileName(" 'F673N  '") != "F673N_offsets.json" {
		t.Fatalf("unexpected offset filename: %q", OffsetFileName(" 'F673N  '"))
	}
}

func TestSaveLoadOffsetsRoundTrip(t *testing.T) {
	inputs := []Input{
		{Path: filepath.Join(t.TempDir(), "a_flc.fits"), PrimaryHeader: fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'"}}, OffsetX: 1.5, OffsetY: -2},
		{Path: filepath.Join(t.TempDir(), "b_flc.fits"), PrimaryHeader: fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'"}}, OffsetX: -0.25, OffsetY: 3.75},
	}
	path := filepath.Join(t.TempDir(), OffsetFileName("F502N"))
	if err := SaveOffsetsForInputs(path, "F502N", inputs); err != nil {
		t.Fatalf("SaveOffsetsForInputs error: %v", err)
	}
	filter, records, err := LoadOffsets(path)
	if err != nil {
		t.Fatalf("LoadOffsets error: %v", err)
	}
	if filter != "F502N" {
		t.Fatalf("filter = %q, want F502N", filter)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
	if records["a_flc.fits"].OffsetX != 1.5 || records["a_flc.fits"].OffsetY != -2 {
		t.Fatalf("unexpected record for a_flc.fits: %+v", records["a_flc.fits"])
	}
}

func TestSaveLoadOffsetsRoundTripWithAffine(t *testing.T) {
	theta := 0.5 * math.Pi / 180
	affine := processing.AffineTransform{
		A: math.Cos(theta), B: -math.Sin(theta), C: 1.5,
		D: math.Sin(theta), E: math.Cos(theta), F: -0.7,
	}
	inputs := []Input{
		{Path: filepath.Join(t.TempDir(), "a_flc.fits"), PrimaryHeader: fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'"}},
			OffsetX: 1.5, OffsetY: -2, ManualTransform: affine, HasManualTransform: true},
		{Path: filepath.Join(t.TempDir(), "b_flc.fits"), PrimaryHeader: fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'"}},
			OffsetX: -0.25, OffsetY: 3.75},
	}
	path := filepath.Join(t.TempDir(), OffsetFileName("F502N"))
	if err := SaveOffsetsForInputs(path, "F502N", inputs); err != nil {
		t.Fatalf("SaveOffsetsForInputs error: %v", err)
	}
	_, records, err := LoadOffsets(path)
	if err != nil {
		t.Fatalf("LoadOffsets error: %v", err)
	}
	rec := records["a_flc.fits"]
	if !rec.HasManualTransform {
		t.Fatalf("expected HasManualTransform=true for a_flc.fits")
	}
	if math.Abs(rec.ManualTransform.A-affine.A) > 1e-9 || math.Abs(rec.ManualTransform.D-affine.D) > 1e-9 {
		t.Fatalf("affine mismatch: got %+v, want %+v", rec.ManualTransform, affine)
	}
	rec2 := records["b_flc.fits"]
	if rec2.HasManualTransform {
		t.Fatalf("expected HasManualTransform=false for b_flc.fits")
	}
}

func TestAutoLoadOffsetsAppliesMatchingFilterFile(t *testing.T) {
	dir := t.TempDir()
	inputs := []Input{
		{Path: filepath.Join(dir, "a_flc.fits"), PrimaryHeader: fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'"}}},
		{Path: filepath.Join(dir, "b_flc.fits"), PrimaryHeader: fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'"}}},
	}
	content := "F502N\na_flc.fits\t2.000000\t-1.000000\nb_flc.fits\t3.500000\t4.500000\n"
	if err := os.WriteFile(filepath.Join(dir, "F502N_offsets.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
	applied, messages := AutoLoadOffsets(inputs)
	if applied != 2 {
		t.Fatalf("applied = %d, want 2", applied)
	}
	if len(messages) == 0 {
		t.Fatalf("expected load message")
	}
	if inputs[0].OffsetX != 2 || inputs[0].OffsetY != -1 {
		t.Fatalf("first input offsets = (%v,%v)", inputs[0].OffsetX, inputs[0].OffsetY)
	}
	if inputs[1].OffsetX != 3.5 || inputs[1].OffsetY != 4.5 {
		t.Fatalf("second input offsets = (%v,%v)", inputs[1].OffsetX, inputs[1].OffsetY)
	}
}
