package processing

import (
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

// makeNoisyImage builds a flat-background image with alternating +/-noise and a
// few bright pixels (so the star-clipped sigma reflects the noise level).
func makeNoisyImage(background float32, noise float32, peak float32) *models.LoadedImage {
	const n = 400
	pixels := make([]float32, n)
	for i := range pixels {
		if i%2 == 0 {
			pixels[i] = background - noise
		} else {
			pixels[i] = background + noise
		}
	}
	for i := 0; i < 4; i++ {
		pixels[i*97] = peak // sparse bright pixels, clipped out of the sigma estimate
	}
	return &models.LoadedImage{
		HDU:        fitsio.HDU{Data: fitsio.ImageData{Width: n, Height: 1, Pixels: pixels}},
		Background: float64(background) - 10,
		Peak:       float64(peak),
		ScaledPeak: 100,
	}
}

func TestAutoMTFMidtoneSetsModeAndBoundedMidtone(t *testing.T) {
	img := makeNoisyImage(100, 2, 5000)
	AutoMTFMidtone(img)

	if img.Mode != stretch.MTF {
		t.Fatalf("Mode = %v, want MTF", img.Mode)
	}
	if img.MTFMidtone <= 0 || img.MTFMidtone > 0.5 {
		t.Fatalf("MTFMidtone = %v, want in (0, 0.5]", img.MTFMidtone)
	}
}

func TestAutoMTFMidtoneNoisierImageGetsGentlerMidtone(t *testing.T) {
	low := makeNoisyImage(100, 2, 5000)
	high := makeNoisyImage(100, 6, 5000)
	AutoMTFMidtone(low)
	AutoMTFMidtone(high)

	// More noise => larger reference level => larger (gentler) midtone.
	if !(high.MTFMidtone > low.MTFMidtone) {
		t.Fatalf("expected noisier image midtone %v > quieter %v", high.MTFMidtone, low.MTFMidtone)
	}
}

func TestAutoMTFMidtoneAutoScalesWhenLevelsUnset(t *testing.T) {
	img := makeNoisyImage(100, 2, 5000)
	img.Background = 0
	img.Peak = 0 // Peak <= Background => AutoMTFMidtone should establish sane levels

	AutoMTFMidtone(img)

	if img.Peak <= img.Background {
		t.Fatalf("Peak (%v) should exceed Background (%v) after auto scaling", img.Peak, img.Background)
	}
}
