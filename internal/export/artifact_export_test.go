package export

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"
)

func TestFullArtifactJPEGEstimateMatchesSavedOutputWithOverlay(t *testing.T) {
	d := t.TempDir()
	var paths [3]string
	for c := range paths {
		paths[c] = filepath.Join(d, fmt.Sprintf("p%d.bin", c))
		a, err := fitsio.CreateFloat32Artifact(paths[c], 4, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err = a.WriteRow(0, []float32{0, .25, .5, 1}); err != nil {
			t.Fatal(err)
		}
		if err = a.WriteRow(1, []float32{1, .5, .25, 0}); err != nil {
			t.Fatal(err)
		}
		_ = a.Close()
	}
	over := image.NewRGBA(image.Rect(0, 0, 2, 1))
	over.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	over.SetRGBA(1, 0, color.RGBA{G: 255, A: 255})
	opt := Options{Quality: 73, Overlays: []Overlay{{Image: over, X: 1, Y: 1}}}
	out := filepath.Join(d, "out.jpg")
	if err := FromFloat32Artifacts(context.Background(), out, paths, 4, 2, JPEG, opt); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	n, err := EstimateFloat32ArtifactsJPEG(context.Background(), paths, 4, 2, opt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != info.Size() {
		t.Fatalf("estimate=%d saved=%d", n, info.Size())
	}
}

func TestEstimateFloat32ArtifactsJPEGHonorsCancellation(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "plane.bin")
	a, err := fitsio.CreateFloat32Artifact(path, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(0, []float32{0.5}); err != nil {
		_ = a.Close()
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EstimateFloat32ArtifactsJPEG(ctx, [3]string{path, path, path}, 1, 1, Options{Quality: 80}, nil); err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestFromFloat32ArtifactsPNGBitDepths(t *testing.T) {
	d := t.TempDir()
	paths := [3]string{}
	for c := range paths {
		paths[c] = filepath.Join(d, string(rune('a'+c))+".bin")
		a, err := fitsio.CreateFloat32Artifact(paths[c], 3, 2)
		if err != nil {
			t.Fatal(err)
		}
		for y := 0; y < 2; y++ {
			if err := a.WriteRow(y, []float32{float32(c) / 2, .5, 1}); err != nil {
				t.Fatal(err)
			}
		}
		_ = a.Close()
	}
	for _, tc := range []struct {
		name  string
		depth int
		want  int
	}{{"8", 8, 8}, {"16", 16, 16}} {
		out := filepath.Join(d, tc.name+".png")
		if err := FromFloat32Artifacts(context.Background(), out, paths, 3, 2, PNG, Options{BitDepth: tc.depth}); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := png.DecodeConfig(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Width != 3 || cfg.Height != 2 {
			t.Fatalf("dimensions %dx%d", cfg.Width, cfg.Height)
		}
		f2, err := os.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(f2)
		_ = f2.Close()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) < 25 || string(raw[12:16]) != "IHDR" || int(raw[24]) != tc.want {
			t.Fatalf("png IHDR bit depth=%d want %d", raw[24], tc.want)
		}
		r, _, _, _ := img.At(1, 0).RGBA()
		if tc.want == 16 {
			// The source midpoint must survive as a genuine 16-bit sample,
			// not merely an 8-bit value expanded by the decoder.
			if r != 0x7fff {
				t.Fatalf("16-bit midpoint=%#x want %#x", r, uint32(0x7fff))
			}
		} else if r != 0x7f7f {
			t.Fatalf("8-bit midpoint=%#x want %#x", r, uint32(0x7f7f))
		}
	}
}

func TestFromFloat32ArtifactStretchParity(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "gray.bin")
	vals := []float32{0, .1, .35, .8, 1}
	a, err := fitsio.CreateFloat32Artifact(path, len(vals), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(0, vals); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	for i, mode := range []stretch.Mode{stretch.Linear, stretch.Log, stretch.Sqrt, stretch.Asinh, stretch.MTF} {
		meta := &models.LoadedImage{Mode: mode, Background: 0, Peak: 1, ScaledPeak: 1}
		out := filepath.Join(d, fmt.Sprintf("stretch-%d.png", i))
		if err := FromFloat32Artifact(context.Background(), out, path, len(vals), 1, PNG, Options{}, meta); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		for x, v := range vals {
			want := uint32(float64(processing.DiskStretchPreviewValue(v, *meta)) * 255)
			got, _, _, _ := img.At(x, 0).RGBA()
			got >>= 8
			if got != want {
				t.Fatalf("mode %v x=%d got %d want %d", mode, x, got, want)
			}
		}
	}
}

func TestFromFloat32ArtifactHistEqParity(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "gray.bin")
	vals := []float32{0, .1, .2, .5, .9, 1}
	a, err := fitsio.CreateFloat32Artifact(path, len(vals), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(0, vals); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	meta := &models.LoadedImage{Mode: stretch.HistEq, Background: 0, Peak: 1, ScaledPeak: 1, HDU: fitsio.HDU{Data: fitsio.ImageData{Width: len(vals), Height: 1, Pixels: append([]float32(nil), vals...)}}}
	wantData, _ := processing.ApplyStretchParallel(meta)
	out := filepath.Join(d, "histeq.png")
	if err := FromFloat32Artifact(context.Background(), out, path, len(vals), 1, PNG, Options{}, meta); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	for x, v := range wantData.Pixels {
		want := uint32(float64(v) * 255)
		got, _, _, _ := img.At(x, 0).RGBA()
		got >>= 8
		if got != want {
			t.Fatalf("x=%d got %d want %d", x, got, want)
		}
	}
}

func TestFromFloat32ArtifactsCancellationLeavesNoOutput(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "x.bin")
	a, err := fitsio.CreateFloat32Artifact(p, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	_ = a.WriteRow(0, []float32{0, 1})
	_ = a.WriteRow(1, []float32{1, 0})
	_ = a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := filepath.Join(d, "out.png")
	if err := FromFloat32Artifacts(ctx, out, [3]string{p, p, p}, 2, 2, PNG, Options{}); err == nil {
		t.Fatal("expected cancellation")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("output exists after cancellation: %v", err)
	}
}

func TestFromFloat32ArtifactsPreservesExistingBackup(t *testing.T) {
	d := t.TempDir()
	artifact := filepath.Join(d, "source.bin")
	a, err := fitsio.CreateFloat32Artifact(artifact, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(0, []float32{0.5}); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	out := filepath.Join(d, "out.png")
	if err := os.WriteFile(out, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	legacyBackup := out + ".export-backup"
	if err := os.WriteFile(legacyBackup, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := FromFloat32Artifacts(context.Background(), out, [3]string{artifact, artifact, artifact}, 1, 1, PNG, Options{}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(legacyBackup); err != nil || string(got) != "keep" {
		t.Fatalf("legacy backup changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(out); err != nil || string(got) == "old" {
		t.Fatalf("destination was not replaced: %q, %v", got, err)
	}
}

func TestFromFloat32ArtifactsRejectsNonRegularDestination(t *testing.T) {
	d := t.TempDir()
	artifact := filepath.Join(d, "source.bin")
	a, err := fitsio.CreateFloat32Artifact(artifact, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(0, []float32{0.5}); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	out := filepath.Join(d, "out.png")
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	if err := FromFloat32Artifacts(context.Background(), out, [3]string{artifact, artifact, artifact}, 1, 1, PNG, Options{}); err == nil {
		t.Fatal("expected non-regular destination error")
	}
	if info, err := os.Stat(out); err != nil || !info.IsDir() {
		t.Fatalf("destination changed: info=%v err=%v", info, err)
	}
}

func TestFromFloat32ArtifactsAllExportFormatsUseEditedPixels(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "source.bin")
	a, err := fitsio.CreateFloat32Artifact(path, 16, 16)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 16; y++ {
		if err := a.WriteRow(y, append([]float32{0.125, 0.875}, make([]float32, 14)...)); err != nil {
			_ = a.Close()
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	formats := []struct {
		name   string
		format Format
		check  func(*os.File) (uint32, error)
	}{
		{"png", PNG, func(f *os.File) (uint32, error) {
			img, err := png.Decode(f)
			if err != nil {
				return 0, err
			}
			r, _, _, _ := img.At(1, 0).RGBA()
			return r, nil
		}},
		{"jpg", JPEG, func(f *os.File) (uint32, error) {
			img, err := jpeg.Decode(f)
			if err != nil {
				return 0, err
			}
			r, _, _, _ := img.At(1, 0).RGBA()
			return r, nil
		}},
		{"tiff", TIFF, func(f *os.File) (uint32, error) {
			img, err := tiff.Decode(f)
			if err != nil {
				return 0, err
			}
			r, _, _, _ := img.At(1, 0).RGBA()
			return r, nil
		}},
		{"webp", WEBP, func(f *os.File) (uint32, error) {
			img, err := webp.Decode(f)
			if err != nil {
				return 0, err
			}
			r, _, _, _ := img.At(1, 0).RGBA()
			return r, nil
		}},
	}
	overlay := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			overlay.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	for _, tc := range formats {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(d, "edited."+tc.name)
			if err := FromFloat32Artifacts(context.Background(), out, [3]string{path, path, path}, 16, 16, tc.format, Options{Quality: 100, Overlays: []Overlay{{Image: overlay, X: 4, Y: 8}}}); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(out)
			if err != nil {
				t.Fatal(err)
			}
			got, err := tc.check(f)
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			// The shared decoder above intentionally checks the edited source
			// pixel; verify the overlay independently through the decoded image.
			f3, err := os.Open(out)
			if err != nil {
				t.Fatal(err)
			}
			var overlayPixel color.Color
			switch tc.format {
			case PNG:
				decoded, decodeErr := png.Decode(f3)
				err = decodeErr
				if err == nil {
					overlayPixel = decoded.At(5, 9)
				}
			case JPEG:
				decoded, decodeErr := jpeg.Decode(f3)
				err = decodeErr
				if err == nil {
					overlayPixel = decoded.At(5, 9)
				}
			case TIFF:
				decoded, decodeErr := tiff.Decode(f3)
				err = decodeErr
				if err == nil {
					overlayPixel = decoded.At(5, 9)
				}
			case WEBP:
				decoded, decodeErr := webp.Decode(f3)
				err = decodeErr
				if err == nil {
					overlayPixel = decoded.At(5, 9)
				}
			}
			_ = f3.Close()
			if err != nil {
				t.Fatal(err)
			}
			r, g, b, _ := overlayPixel.RGBA()
			if r < 0xc000 || g > 0x9000 || b > 0x9000 {
				t.Fatalf("overlay pixel = %#x/%#x/%#x, want opaque red", r, g, b)
			}
			if got < 0xD000 || got > 0xE500 {
				t.Fatalf("edited bright pixel=%#x, want approximately 0.875", got)
			}
		})
	}
}
