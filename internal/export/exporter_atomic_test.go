package export

import (
	"image"
	"os"
	"path/filepath"
	"testing"
)

func TestFromImageEncodeFailurePreservesDestination(t *testing.T) {
	d := t.TempDir()
	out := filepath.Join(d, "out.img")
	if err := os.WriteFile(out, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if err := FromImage(out, img, Format("bogus"), Options{}); err == nil {
		t.Fatal("expected unsupported format error")
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != "old" {
		t.Fatalf("destination = %q, %v; want old", got, err)
	}
	entries, _ := os.ReadDir(d)
	if len(entries) != 1 {
		t.Fatalf("unexpected leftovers: %v", entries)
	}
}
