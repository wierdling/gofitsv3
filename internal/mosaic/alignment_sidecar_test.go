package mosaic

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

func TestAlignmentSidecarPath(t *testing.T) {
	input := Input{Path: filepath.Join("work", "exposure.fits")}
	if got, want := AlignmentSidecarPath(input), filepath.Join("work", "exposure_alignment.json"); got != want {
		t.Fatalf("AlignmentSidecarPath = %q, want %q", got, want)
	}
}

func TestAlignmentSidecarMergeSaveAndLoadValidated(t *testing.T) {
	dir := t.TempDir()
	targetPath := writeAlignmentTestFile(t, dir, "target.fits", "target")
	refPath := writeAlignmentTestFile(t, dir, "reference.fits", "reference")
	target1 := Input{Path: targetPath, SCIExt: 1, HDU: testAlignmentHDU(10, 20)}
	target2 := Input{Path: targetPath, SCIExt: 2, HDU: testAlignmentHDU(30, 40)}
	reference := Input{Path: refPath, SCIExt: 1, HDU: testAlignmentHDU(50, 60)}

	first := StarAlignmentResult{Applied: true, OffsetX: 2, OffsetY: -3, HasManualTransform: true, ManualTransform: processing.AffineTransform{A: 1, E: 1, C: 2, F: -3}, MatchedStars: 12, RMS: 0.4}
	if err := MergeSaveAlignmentSidecar(target1, reference, first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	second := StarAlignmentResult{Applied: true, OffsetX: 4, OffsetY: 5}
	if err := MergeSaveAlignmentSidecar(target2, reference, second); err != nil {
		t.Fatalf("second save: %v", err)
	}

	loaded := LoadValidatedAlignmentSidecar(target1, reference)
	if loaded.Status != AlignmentSidecarLoaded || loaded.Entry == nil {
		t.Fatalf("load = %+v, want loaded entry", loaded)
	}
	if loaded.Entry.OffsetX != 2 || loaded.Entry.OffsetY != -3 || loaded.Entry.Diagnostics.MatchedStars != 12 {
		t.Fatalf("unexpected saved entry: %+v", loaded.Entry)
	}
	if got, want := loaded.SidecarPath, filepath.Join(dir, "target_alignment.json"); got != want {
		t.Fatalf("sidecar path = %q, want %q", got, want)
	}
	if got := LoadValidatedAlignmentSidecar(target2, reference); got.Status != AlignmentSidecarLoaded || got.Entry.OffsetX != 4 {
		t.Fatalf("second SCI entry = %+v", got)
	}
}

func TestAlignmentSidecarRejectsChangedTargetOrReference(t *testing.T) {
	dir := t.TempDir()
	targetPath := writeAlignmentTestFile(t, dir, "target.fits", "target")
	refPath := writeAlignmentTestFile(t, dir, "reference.fits", "reference")
	target := Input{Path: targetPath, HDU: testAlignmentHDU(10, 20)}
	reference := Input{Path: refPath, HDU: testAlignmentHDU(30, 40)}
	if err := MergeSaveAlignmentSidecar(target, reference, StarAlignmentResult{Applied: true}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := os.WriteFile(refPath, []byte("changed reference content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadValidatedAlignmentSidecar(target, reference); got.Status != AlignmentSidecarReferenceChanged {
		t.Fatalf("reference status = %+v, want reference changed", got)
	}

	// Restore the saved reference identity by saving again, then change target.
	if err := MergeSaveAlignmentSidecar(target, reference, StarAlignmentResult{Applied: true}); err != nil {
		t.Fatalf("resave: %v", err)
	}
	time.Sleep(time.Millisecond) // ensure the modification time changes on coarse filesystems
	if err := os.WriteFile(targetPath, []byte("changed target content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadValidatedAlignmentSidecar(target, reference); got.Status != AlignmentSidecarTargetChanged {
		t.Fatalf("target status = %+v, want target changed", got)
	}
}

func TestDeleteAlignmentEntriesForReference(t *testing.T) {
	dir := t.TempDir()
	refPath := writeAlignmentTestFile(t, dir, "reference.fits", "reference")
	otherRefPath := writeAlignmentTestFile(t, dir, "other.fits", "other")
	targetPath := writeAlignmentTestFile(t, dir, "target.fits", "target")
	target := Input{Path: targetPath, SCIExt: 1, HDU: testAlignmentHDU(10, 10)}
	target2 := Input{Path: targetPath, SCIExt: 2, HDU: testAlignmentHDU(10, 10)}
	reference := Input{Path: refPath, HDU: testAlignmentHDU(10, 10)}
	otherReference := Input{Path: otherRefPath, HDU: testAlignmentHDU(10, 10)}
	if err := MergeSaveAlignmentSidecar(target, reference, StarAlignmentResult{Applied: true}); err != nil {
		t.Fatal(err)
	}
	if err := MergeSaveAlignmentSidecar(target2, otherReference, StarAlignmentResult{Applied: true}); err != nil {
		t.Fatal(err)
	}

	deleted, err := DeleteAlignmentEntriesForReference(dir, reference)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteAlignmentEntriesForReference = (%d, %v), want (1, nil)", deleted, err)
	}
	if got := LoadValidatedAlignmentSidecar(target, reference); got.Status != AlignmentSidecarNotFound {
		t.Fatalf("deleted entry status = %+v", got)
	}
	if got := LoadValidatedAlignmentSidecar(target2, otherReference); got.Status != AlignmentSidecarLoaded {
		t.Fatalf("unrelated entry status = %+v", got)
	}
}

func writeAlignmentTestFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func testAlignmentHDU(width, height int) fitsio.HDU {
	return fitsio.HDU{Data: fitsio.ImageData{Width: width, Height: height}}
}
