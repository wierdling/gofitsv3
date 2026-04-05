package processing

import (
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

	buf, w, h, _ := ComposeRGB([]*models.LoadedImage{blue, green, red})
	if w != 6 || h != 5 {
		t.Fatalf("ComposeRGB size = %dx%d, want 6x5", w, h)
	}
	if got, want := len(buf), 6*5*4; got != want {
		t.Fatalf("buffer length = %d, want %d", got, want)
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
