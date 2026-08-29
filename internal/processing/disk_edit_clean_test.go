package processing

import (
	"context"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestCleanColorSpecksDiskMatchesMemoryAcrossTileSeam(t *testing.T) {
	const w, h = 263, 263
	in := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			in.SetRGBA(x, y, color.RGBA{R: 180, G: 180, B: 180, A: 255})
		}
	}
	// A single blemish straddles the 256-row tile boundary.
	for y := 254; y <= 257; y++ {
		for x := 3; x <= 4; x++ {
			in.SetRGBA(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	// A second blemish crosses the horizontal tile seam.
	for y := 3; y <= 4; y++ {
		for x := 254; x <= 257; x++ {
			in.SetRGBA(x, y, color.RGBA{R: 0, G: 255, B: 0, A: 255})
		}
	}
	// Oversized candidate components must remain untouched.
	for y := 100; y < 106; y++ {
		for x := 100; x < 106; x++ {
			in.SetRGBA(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	cfg := DefaultColorSpeckCleanConfig()
	want, repaired := CleanColorSpecksRGBA(in, cfg)
	if repaired == 0 {
		t.Fatal("memory cleaner did not find fixture speck")
	}
	d := t.TempDir()
	var src, out [3]string
	for c := range src {
		values := make([]float32, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				p := in.RGBAAt(x, y)
				values[y*w+x] = float32([]uint8{p.R, p.G, p.B}[c]) / 255
			}
		}
		src[c] = filepath.Join(d, "src", string(rune('0'+c))+".bin")
		out[c] = filepath.Join(d, "out", string(rune('0'+c))+".bin")
		makeEditArtifact(t, src[c], values, w, h)
	}
	got, err := CleanColorSpecksDisk(context.Background(), DiskColorSpeckCleanRequest{Source: src, Output: out, Width: w, Height: h, Config: cfg, PreviewMax: 1600})
	if err != nil {
		t.Fatal(err)
	}
	if got.Repaired != repaired {
		t.Fatalf("repaired=%d want %d", got.Repaired, repaired)
	}
	for c := range out {
		a, err := fitsio.OpenFloat32ArtifactReadOnly(out[c])
		if err != nil {
			t.Fatal(err)
		}
		row := make([]float32, w)
		for y := 0; y < h; y++ {
			if err := a.ReadRow(y, row); err != nil {
				_ = a.Close()
				t.Fatal(err)
			}
			for x, v := range row {
				p := want.RGBAAt(x, y)
				wantByte := []uint8{p.R, p.G, p.B}[c]
				if uint8(v*255+0.5) != wantByte {
					_ = a.Close()
					t.Fatalf("channel %d (%d,%d)=%d want %d", c, x, y, uint8(v*255+0.5), wantByte)
				}
			}
		}
		_ = a.Close()
	}
}

func TestCleanColorSpecksDiskRejectsOverlapAndCancellation(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	for c := range src {
		src[c] = filepath.Join(d, "src", string(rune('0'+c))+".bin")
		out[c] = filepath.Join(d, "out", string(rune('0'+c))+".bin")
		makeEditArtifact(t, src[c], []float32{.5}, 1, 1)
	}
	overlap := src
	if _, err := CleanColorSpecksDisk(context.Background(), DiskColorSpeckCleanRequest{Source: src, Output: overlap, Width: 1, Height: 1, Config: DefaultColorSpeckCleanConfig()}); err == nil {
		t.Fatal("source/output overlap accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CleanColorSpecksDisk(ctx, DiskColorSpeckCleanRequest{Source: src, Output: out, Width: 1, Height: 1, Config: DefaultColorSpeckCleanConfig()}); err == nil {
		t.Fatal("canceled clean succeeded")
	}
	for _, p := range out {
		if _, err := fitsio.OpenFloat32ArtifactReadOnly(p); err == nil {
			t.Fatalf("canceled clean left output %q", p)
		}
	}
}

func TestCleanColorSpecksDiskFailurePreservesPreexistingOutputs(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	for c := range src {
		src[c] = filepath.Join(d, "src", string(rune('0'+c))+".bin")
		out[c] = filepath.Join(d, "out", string(rune('0'+c))+".bin")
		makeEditArtifact(t, src[c], []float32{.5}, 1, 1)
		makeEditArtifact(t, out[c], []float32{.9}, 1, 1)
	}
	// Force a failure after the first source/output pair has been staged.
	makeEditArtifact(t, src[2], []float32{.5, .5}, 2, 1)
	if _, err := CleanColorSpecksDisk(context.Background(), DiskColorSpeckCleanRequest{Source: src, Output: out, Width: 1, Height: 1, Config: DefaultColorSpeckCleanConfig()}); err == nil {
		t.Fatal("dimension failure unexpectedly succeeded")
	}
	for _, p := range out {
		a, err := fitsio.OpenFloat32ArtifactReadOnly(p)
		if err != nil {
			t.Fatal(err)
		}
		row := make([]float32, 1)
		if err := a.ReadRow(0, row); err != nil {
			_ = a.Close()
			t.Fatal(err)
		}
		_ = a.Close()
		if row[0] != .9 {
			t.Fatalf("pre-existing output %q changed to %v", p, row[0])
		}
	}
}

func TestCleanColorSpecksDiskCommitFailureRemovesPublishedOutputs(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	for c := range src {
		src[c] = filepath.Join(d, "src", string(rune('0'+c))+".bin")
		out[c] = filepath.Join(d, "out", string(rune('0'+c))+".bin")
		makeEditArtifact(t, src[c], []float32{.5}, 1, 1)
	}
	diskColorSpeckCleanCommitHook = func(i int) error {
		if i == 1 {
			return context.Canceled
		}
		return nil
	}
	defer func() { diskColorSpeckCleanCommitHook = nil }()
	if _, err := CleanColorSpecksDisk(context.Background(), DiskColorSpeckCleanRequest{Source: src, Output: out, Width: 1, Height: 1, Config: DefaultColorSpeckCleanConfig()}); err == nil {
		t.Fatal("injected commit failure unexpectedly succeeded")
	}
	for _, p := range out {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("published output remains: %q (%v)", p, err)
		}
	}
}
