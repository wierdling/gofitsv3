package processing

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
)

func readDiskPixel(t *testing.T, path string, w, y int) []float32 {
	t.Helper()
	a, err := fitsio.OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	row := make([]float32, w)
	if err := a.ReadRow(y, row); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestCropDiskCopiesBoundedRegionAndDimensions(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	for c := range src {
		src[c] = filepath.Join(d, "src", string(rune('0'+c)))
		out[c] = filepath.Join(d, "out", string(rune('0'+c)))
		makeEditArtifact(t, src[c], []float32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, 4, 3)
	}
	w, h, err := CropDisk(context.Background(), src, out, 4, 3, image.Rect(1, 1, 3, 3))
	if err != nil {
		t.Fatal(err)
	}
	if w != 2 || h != 2 {
		t.Fatalf("dimensions %dx%d", w, h)
	}
	if got := readDiskPixel(t, out[0], 2, 0); got[0] != 5 || got[1] != 6 {
		t.Fatalf("cropped row: %v", got)
	}
}

func TestHealDiskUsesOriginalSourceForOverlappingDestinations(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	vals := make([]float32, 25)
	for i := range vals {
		vals[i] = float32(i)
	}
	for c := range src {
		src[c] = filepath.Join(d, "src", string(rune('0'+c)))
		out[c] = filepath.Join(d, "out", string(rune('0'+c)))
		makeEditArtifact(t, src[c], vals, 5, 5)
	}
	err := HealDisk(context.Background(), src, out, 5, 5, DiskHealStroke{Source: image.Pt(0, 0), Destinations: []image.Point{image.Pt(2, 2), image.Pt(3, 2)}, Radius: 1})
	if err != nil {
		t.Fatal(err)
	}
	row := readDiskPixel(t, out[0], 5, 2)
	if row[2] != 0 || row[3] != 0 {
		t.Fatalf("overlap should sample source, got %v", row)
	}
	if got := readDiskPixel(t, src[0], 5, 2); got[2] != 12 {
		t.Fatalf("source mutated: %v", got)
	}
}

func TestCropDiskLaterCommitFailureRemovesEarlierPlanes(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	for c := range src {
		src[c] = filepath.Join(d, "src", string(rune('0'+c)))
		out[c] = filepath.Join(d, "out", string(rune('0'+c)))
		makeEditArtifact(t, src[c], []float32{1, 2, 3, 4}, 2, 2)
	}
	inst := &fitsio.ArtifactInstrumentation{}
	inst.FailCommitAt.Store(2)
	fitsio.SetArtifactInstrumentation(inst)
	defer fitsio.SetArtifactInstrumentation(nil)
	if _, _, err := CropDisk(context.Background(), src, out, 2, 2, image.Rect(0, 0, 1, 1)); err == nil {
		t.Fatal("expected injected commit failure")
	}
	for _, p := range out {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("partial crop output remains: %s", p)
		}
	}
}

func TestHealDiskLaterCommitFailureRemovesEarlierPlanes(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	for c := range src {
		src[c] = filepath.Join(d, "src", string(rune('0'+c)))
		out[c] = filepath.Join(d, "out", string(rune('0'+c)))
		makeEditArtifact(t, src[c], []float32{1, 2, 3, 4}, 2, 2)
	}
	inst := &fitsio.ArtifactInstrumentation{}
	inst.FailCommitAt.Store(2)
	fitsio.SetArtifactInstrumentation(inst)
	defer fitsio.SetArtifactInstrumentation(nil)
	if err := HealDisk(context.Background(), src, out, 2, 2, DiskHealStroke{Source: image.Pt(0, 0), Destinations: []image.Point{{X: 1, Y: 1}}, Radius: 1}); err == nil {
		t.Fatal("expected injected commit failure")
	}
	for _, p := range out {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("partial heal output remains: %s", p)
		}
	}
}

func TestSpatialTransformsRejectPreexistingOutputsWithoutDeletingThem(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	for c := range src {
		src[c] = filepath.Join(d, "src", string(rune('0'+c)))
		out[c] = filepath.Join(d, "out", string(rune('0'+c)))
		makeEditArtifact(t, src[c], []float32{1, 2, 3, 4}, 2, 2)
	}
	if err := os.MkdirAll(filepath.Dir(out[1]), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out[1], []byte("caller-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CropDisk(context.Background(), src, out, 2, 2, image.Rect(0, 0, 1, 1)); err == nil {
		t.Fatal("expected existing crop output rejection")
	}
	if got, err := os.ReadFile(out[1]); err != nil || string(got) != "caller-owned" {
		t.Fatalf("crop changed caller output: %q %v", got, err)
	}
	if err := HealDisk(context.Background(), src, out, 2, 2, DiskHealStroke{Source: image.Pt(0, 0), Destinations: []image.Point{{X: 1, Y: 1}}, Radius: 1}); err == nil {
		t.Fatal("expected existing heal output rejection")
	}
	if got, err := os.ReadFile(out[1]); err != nil || string(got) != "caller-owned" {
		t.Fatalf("heal changed caller output: %q %v", got, err)
	}
}
