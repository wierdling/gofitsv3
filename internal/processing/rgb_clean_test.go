package processing

import (
	"image"
	"image/color"
	"testing"
)

func TestCleanColorSpecksRGBARepairsSmallPureColorBlob(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 9, 9))
	fillRGBA(img, color.RGBA{R: 20, G: 20, B: 20, A: 255})
	for y := 3; y <= 4; y++ {
		for x := 3; x <= 4; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}

	cleaned, repaired := CleanColorSpecksRGBA(img, DefaultColorSpeckCleanConfig())
	if repaired != 4 {
		t.Fatalf("repaired = %d, want 4", repaired)
	}

	for y := 3; y <= 4; y++ {
		for x := 3; x <= 4; x++ {
			got := cleaned.RGBAAt(x, y)
			want := (color.RGBA{R: 20, G: 20, B: 20, A: 255})
			if got != want {
				t.Fatalf("pixel (%d,%d) = %#v, want %#v", x, y, got, want)
			}
		}
	}
}

func TestCleanColorSpecksRGBAPreservesLargeStructures(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 12, 12))
	fillRGBA(img, color.RGBA{R: 24, G: 24, B: 24, A: 255})
	for y := 3; y <= 8; y++ {
		for x := 3; x <= 8; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, G: 16, B: 16, A: 255})
		}
	}

	cleaned, repaired := CleanColorSpecksRGBA(img, DefaultColorSpeckCleanConfig())
	if repaired != 0 {
		t.Fatalf("repaired = %d, want 0", repaired)
	}

	for y := 3; y <= 8; y++ {
		for x := 3; x <= 8; x++ {
			got := cleaned.RGBAAt(x, y)
			want := (color.RGBA{R: 255, G: 16, B: 16, A: 255})
			if got != want {
				t.Fatalf("pixel (%d,%d) = %#v, want %#v", x, y, got, want)
			}
		}
	}
}

func TestColorSpeckCleanConfigFromSettingsScalesAggressively(t *testing.T) {
	low := ColorSpeckCleanConfigFromSettings(9, 0)
	high := ColorSpeckCleanConfigFromSettings(49, 100)

	if low.MaxBlobPixels != 9 {
		t.Fatalf("low.MaxBlobPixels = %d, want 9", low.MaxBlobPixels)
	}
	if high.MaxBlobPixels != 49 {
		t.Fatalf("high.MaxBlobPixels = %d, want 49", high.MaxBlobPixels)
	}
	if !(high.MinDominanceRatio < low.MinDominanceRatio) {
		t.Fatalf("dominance ratio did not get more aggressive: low=%v high=%v", low.MinDominanceRatio, high.MinDominanceRatio)
	}
	if !(high.MinDominanceDelta < low.MinDominanceDelta) {
		t.Fatalf("dominance delta did not get more aggressive: low=%v high=%v", low.MinDominanceDelta, high.MinDominanceDelta)
	}
	if !(high.MinDominantValue < low.MinDominantValue) {
		t.Fatalf("dominant value did not get more aggressive: low=%v high=%v", low.MinDominantValue, high.MinDominantValue)
	}
	if !(high.MinLocalExcess < low.MinLocalExcess) {
		t.Fatalf("local excess did not get more aggressive: low=%v high=%v", low.MinLocalExcess, high.MinLocalExcess)
	}
}

func TestCleanColorSpecksRGBAHighIntensityCatchesFainterBlob(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 9, 9))
	fillRGBA(img, color.RGBA{R: 20, G: 20, B: 20, A: 255})
	for y := 3; y <= 4; y++ {
		for x := 3; x <= 4; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 100, G: 20, B: 20, A: 255})
		}
	}

	_, repairedLow := CleanColorSpecksRGBA(img, ColorSpeckCleanConfigFromSettings(25, 0))
	_, repairedHigh := CleanColorSpecksRGBA(img, ColorSpeckCleanConfigFromSettings(25, 100))

	if repairedLow != 0 {
		t.Fatalf("repairedLow = %d, want 0", repairedLow)
	}
	if repairedHigh != 4 {
		t.Fatalf("repairedHigh = %d, want 4", repairedHigh)
	}
}

func fillRGBA(img *image.RGBA, c color.RGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}
