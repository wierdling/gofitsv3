package processing

import (
	"context"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func TestComposeRGBUsesGreenReferenceGrid(t *testing.T) {
	blue := makeLoadedImageForCompose(4, 4, 1, fitsio.Header{Cards: map[string]string{
		"CRPIX1": "3", "CRPIX2": "3", "CRVAL1": "100", "CRVAL2": "20", "CDELT1": "1", "CDELT2": "1",
		"PC1_1": "1", "PC1_2": "0", "PC2_1": "0", "PC2_2": "1",
	}})
	green := makeLoadedImageForCompose(6, 5, 2, fitsio.Header{Cards: map[string]string{
		"CRPIX1": "4", "CRPIX2": "3", "CRVAL1": "100", "CRVAL2": "20", "CDELT1": "1", "CDELT2": "1",
		"PC1_1": "1", "PC1_2": "0", "PC2_1": "0", "PC2_2": "1",
	}})
	red := makeLoadedImageForCompose(7, 6, 3, fitsio.Header{Cards: map[string]string{
		"CRPIX1": "5", "CRPIX2": "4", "CRVAL1": "100", "CRVAL2": "20", "CDELT1": "1", "CDELT2": "1",
		"PC1_1": "1", "PC1_2": "0", "PC2_1": "0", "PC2_2": "1",
	}})

	buf, w, h, _ := ComposeRGB(context.Background(), []*models.LoadedImage{blue, green, red})
	if w != 6 || h != 5 {
		t.Fatalf("ComposeRGB size = %dx%d, want 6x5", w, h)
	}
	if got, want := len(buf), 6*5*4; got != want {
		t.Fatalf("buffer length = %d, want %d", got, want)
	}
}

func TestComposeRGBWithOrangeScreensTintedLayerWithOpacity(t *testing.T) {
	header := fitsio.Header{Cards: map[string]string{"DRIZSCAL": "1", "ORIGOFFX": "0", "ORIGOFFY": "0"}}
	blue := makeLoadedImageForCompose(1, 1, 0, header)
	green := makeLoadedImageForCompose(1, 1, 0, header)
	red := makeLoadedImageForCompose(1, 1, 0, header)
	orange := makeLoadedImageForCompose(1, 1, 10, header)

	buf, w, h, _ := ComposeRGBWithOrange(context.Background(), []*models.LoadedImage{blue, green, red}, orange, models.OrangeLayerState{
		ColorR:  255,
		ColorG:  128,
		ColorB:  0,
		Opacity: 0.5,
	})
	if w != 1 || h != 1 {
		t.Fatalf("ComposeRGBWithOrange size = %dx%d, want 1x1", w, h)
	}
	if got, want := []byte{buf[0], buf[1], buf[2], buf[3]}, []byte{128, 64, 0, 255}; got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("pixel = %v, want %v", got, want)
	}
}

func TestComposeRGBWithOverlayHighlightProtectedAdditive(t *testing.T) {
	header := fitsio.Header{Cards: map[string]string{"DRIZSCAL": "1", "ORIGOFFX": "0", "ORIGOFFY": "0"}}
	// Base RGB channels stretch to 0.4 (value 4, White 10) -> byte 102 each.
	blue := makeLoadedImageForCompose(1, 1, 4, header)
	green := makeLoadedImageForCompose(1, 1, 4, header)
	red := makeLoadedImageForCompose(1, 1, 4, header)
	// Overlay stretches to 0.2 (value 2, White 10), tint red-only.
	overlay := makeLoadedImageForCompose(1, 1, 2, header)

	buf, _, _, _ := ComposeRGBWithOverlays(context.Background(), []*models.LoadedImage{blue, green, red}, []OverlayLayer{{
		Image: overlay,
		Settings: models.OrangeLayerState{
			ColorR:           255,
			ColorG:           0,
			ColorB:           0,
			Opacity:          1,
			HighlightProtect: 0.5,
		},
	}})

	// R: base 0.4 + layer 0.2 - 0.5*0.4*0.2 = 0.56 -> 143. G/B: tint 0, unchanged 102.
	if got, want := []byte{buf[0], buf[1], buf[2], buf[3]}, []byte{143, 102, 102, 255}; got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("pixel = %v, want %v", got, want)
	}
}

func TestComposeRGBArtisticThreeChannelRegression(t *testing.T) {
	header := fitsio.Header{Cards: map[string]string{"DRIZSCAL": "1", "ORIGOFFX": "0", "ORIGOFFY": "0"}}
	blue := makeLoadedImageForCompose(1, 1, 4, header)
	green := makeLoadedImageForCompose(1, 1, 6, header)
	red := makeLoadedImageForCompose(1, 1, 8, header)
	buf, _, _, _ := ComposeRGB(context.Background(), []*models.LoadedImage{blue, green, red})
	want := []byte{204, 153, 102, 255}
	for i, value := range want {
		if buf[i] != value {
			t.Fatalf("artistic pixel[%d] = %d, want %d", i, buf[i], value)
		}
	}
}

func TestFloat32RGBToRGBAKeepsFloatDomainUntilConversion(t *testing.T) {
	rgb := [3][]float32{{0.5019}, {0.1251}, {0.9999}}
	buf, err := Float32RGBToRGBA(rgb, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := buf[:4], []byte{128, 32, 255, 255}; string(got) != string(want) {
		t.Fatalf("converted pixel = %v, want %v", got, want)
	}
}

func TestStretchForReferenceGridSkipsRewarpForSharedDrizzleGrid(t *testing.T) {
	header := func(crpix1 string) fitsio.Header {
		return fitsio.Header{Cards: map[string]string{
			"CRPIX1":   crpix1,
			"CRPIX2":   "10",
			"CRVAL1":   "100",
			"CRVAL2":   "20",
			"CDELT1":   "1",
			"CDELT2":   "1",
			"PC1_1":    "1",
			"PC1_2":    "0",
			"PC2_1":    "0",
			"PC2_2":    "1",
			"DRIZSCAL": "2",
			"ORIGOFFX": "-123.5",
			"ORIGOFFY": "48.25",
		}}
	}
	ref := makeLoadedImageForCompose(3, 2, 0, header("10"))
	img := makeLoadedImageForCompose(3, 2, 0, header("60"))
	img.HDU.Data.Pixels = []float32{0, 0.25, 0.5, 0.75, 1, 0.4}
	img.Background = 0
	img.Peak = 1
	img.ScaledPeak = 1

	got := stretchForReferenceGrid(context.Background(), img, ref)
	want := img.HDU.Data.Pixels
	if got.Width != img.HDU.Data.Width || got.Height != img.HDU.Data.Height {
		t.Fatalf("stretchForReferenceGrid size = %dx%d, want %dx%d", got.Width, got.Height, img.HDU.Data.Width, img.HDU.Data.Height)
	}
	for i, v := range want {
		if got.Pixels[i] != v {
			t.Fatalf("pixel[%d] = %v, want %v", i, got.Pixels[i], v)
		}
	}
}

func TestStretchedImageDataForReferenceGridUsesDisplayStretch(t *testing.T) {
	header := fitsio.Header{Cards: map[string]string{
		"DRIZSCAL": "1",
		"ORIGOFFX": "0",
		"ORIGOFFY": "0",
	}}
	ref := makeLoadedImageForCompose(2, 1, 0, header)
	img := makeLoadedImageForCompose(2, 1, 0, header)
	img.HDU.Data.Pixels = []float32{0, 10}
	img.Background = 0
	img.Peak = 10
	img.ScaledPeak = 1
	img.Mode = stretch.Linear

	got := StretchedImageDataForReferenceGrid(img, ref)
	if got.Width != 2 || got.Height != 1 {
		t.Fatalf("size = %dx%d, want 2x1", got.Width, got.Height)
	}
	if got.Pixels[0] != 0 || got.Pixels[1] != 1 {
		t.Fatalf("pixels = %v, want [0 1]", got.Pixels)
	}
}

func TestImageDataForReferenceGridSkipsWCSForRotatedChannel(t *testing.T) {
	header := func(crpix1 string) fitsio.Header {
		return fitsio.Header{Cards: map[string]string{
			"CRPIX1": crpix1, "CRPIX2": "2", "CRVAL1": "100", "CRVAL2": "20",
			"CDELT1": "1", "CDELT2": "1", "PC1_1": "1", "PC1_2": "0", "PC2_1": "0", "PC2_2": "1",
		}}
	}
	img := makeLoadedImageForCompose(2, 2, 0, header("1"))
	img.HDU.Data.Pixels = []float32{1, 2, 3, 4}
	img.Rotation90 = 1
	ref := makeLoadedImageForCompose(2, 2, 0, header("2"))

	got := ImageDataForReferenceGrid(img, ref)
	want := []float32{1, 2, 3, 4}
	for i, value := range want {
		if got.Pixels[i] != value {
			t.Fatalf("Pixels[%d] = %v, want unwarped rotated data %v", i, got.Pixels[i], value)
		}
	}
}

func makeLoadedImageForCompose(w, h int, value float32, header fitsio.Header) *models.LoadedImage {
	pixels := make([]float32, w*h)
	for i := range pixels {
		pixels[i] = value
	}
	return &models.LoadedImage{
		HDU:        fitsio.HDU{Header: header, Data: fitsio.ImageData{Width: w, Height: h, Pixels: pixels}},
		Mode:       stretch.Linear,
		Black:      0,
		White:      10,
		Background: 0,
		Peak:       10,
		ScaledPeak: 10,
	}
}
