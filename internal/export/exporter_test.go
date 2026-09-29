package export

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"
)

func TestFromRGBABytesWritesPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.png")
	buf := []byte{
		255, 0, 0, 255,
		0, 255, 0, 255,
	}

	if err := FromRGBABytes(path, buf, 2, 1, PNG, Options{}); err != nil {
		t.Fatalf("FromRGBABytes error: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("png.Decode error: %v", err)
	}
	if got, want := img.Bounds().Dx(), 2; got != want {
		t.Fatalf("width = %d, want %d", got, want)
	}
	if got, want := img.Bounds().Dy(), 1; got != want {
		t.Fatalf("height = %d, want %d", got, want)
	}
}

func TestFromRGBABytesRejectsMismatchedBufferLength(t *testing.T) {
	err := FromRGBABytes(filepath.Join(t.TempDir(), "bad.png"), []byte{1, 2, 3}, 1, 1, PNG, Options{})
	if err == nil {
		t.Fatal("expected buffer length mismatch error")
	}
	if got, want := err.Error(), "buffer length mismatch"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestFromImageWritesRequestedFormats(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 12, G: 34, B: 56, A: 255})
	img.Set(1, 0, color.RGBA{R: 78, G: 90, B: 123, A: 255})

	tests := []struct {
		name   string
		format Format
		ext    string
		opt    Options
		decode func(string) (image.Image, error)
	}{
		{
			name:   "jpeg-default-quality",
			format: JPEG,
			ext:    ".jpg",
			opt:    Options{},
			decode: func(path string) (image.Image, error) {
				f, err := os.Open(path)
				if err != nil {
					return nil, err
				}
				defer f.Close()
				return jpeg.Decode(f)
			},
		},
		{
			name:   "tiff",
			format: TIFF,
			ext:    ".tiff",
			opt:    Options{Quality: 80},
			decode: func(path string) (image.Image, error) {
				f, err := os.Open(path)
				if err != nil {
					return nil, err
				}
				defer f.Close()
				return tiff.Decode(f)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "out"+tt.ext)
			if err := FromImage(path, img, tt.format, tt.opt); err != nil {
				t.Fatalf("FromImage error: %v", err)
			}

			decoded, err := tt.decode(path)
			if err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if got, want := decoded.Bounds().Dx(), 2; got != want {
				t.Fatalf("width = %d, want %d", got, want)
			}
			if got, want := decoded.Bounds().Dy(), 1; got != want {
				t.Fatalf("height = %d, want %d", got, want)
			}
		})
	}
}

func TestOverlayImageFlattensAlphaOverlay(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 2, 1))
	base.SetRGBA(0, 0, color.RGBA{B: 255, A: 255})
	base.SetRGBA(1, 0, color.RGBA{B: 255, A: 255})
	over := image.NewRGBA(image.Rect(0, 0, 1, 1))
	over.SetRGBA(0, 0, color.RGBA{R: 255, A: 128})
	got := OverlayImage(base, []Overlay{{Image: over, X: 1, Y: 0}}).(*image.RGBA)
	if got.RGBAAt(0, 0) != base.RGBAAt(0, 0) {
		t.Fatal("overlay changed pixels outside its bounds")
	}
	if got.RGBAAt(1, 0).R < 120 || got.RGBAAt(1, 0).B < 120 {
		t.Fatalf("unexpected blended pixel: %#v", got.RGBAAt(1, 0))
	}
}

func TestFromImageOverlayIsFlattenedForAllSupportedFormats(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			base.SetRGBA(x, y, color.RGBA{B: 255, A: 255})
		}
	}
	over := image.NewRGBA(image.Rect(0, 0, 4, 4))
	over.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			over.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}

	decode := map[Format]func(string) (image.Image, error){
		PNG: func(path string) (image.Image, error) {
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			return png.Decode(f)
		},
		JPEG: func(path string) (image.Image, error) {
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			return jpeg.Decode(f)
		},
		TIFF: func(path string) (image.Image, error) {
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			return tiff.Decode(f)
		},
		WEBP: func(path string) (image.Image, error) {
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			return webp.Decode(f)
		},
	}
	for format, read := range decode {
		t.Run(string(format), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "overlay."+string(format))
			if err := FromImage(path, base, format, Options{Quality: 100, Overlays: []Overlay{{Image: over, X: 8, Y: 6}}}); err != nil {
				t.Fatal(err)
			}
			img, err := read(path)
			if err != nil {
				t.Fatal(err)
			}
			r, g, b, _ := img.At(9, 7).RGBA()
			if r < 0xc000 || g > 0x9000 || b > 0x9000 {
				t.Fatalf("overlay pixel = %#x/%#x/%#x, want opaque red", r, g, b)
			}
		})
	}
}

func TestFromImageRejectsUnsupportedFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.bin")
	err := FromImage(path, image.NewRGBA(image.Rect(0, 0, 1, 1)), Format("gif"), Options{})
	if err == nil {
		t.Fatal("expected unsupported format error")
	}
	if got, want := err.Error(), "unsupported format"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestFromImageUnsupportedFormatPreservesExistingDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.bin")
	sentinel := []byte("existing output must survive format validation")
	if err := os.WriteFile(path, sentinel, 0o600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	err := FromImage(path, image.NewRGBA(image.Rect(0, 0, 1, 1)), Format("gif"), Options{})
	if err == nil {
		t.Fatal("expected unsupported format error")
	}
	if got, want := err.Error(), "unsupported format"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != string(sentinel) {
		t.Fatalf("destination changed: got %q, want %q", got, sentinel)
	}
}

func TestFromImagePropagatesCreateError(t *testing.T) {
	err := FromImage(t.TempDir(), image.NewRGBA(image.Rect(0, 0, 1, 1)), PNG, Options{})
	if err == nil {
		t.Fatal("expected create error")
	}
}
