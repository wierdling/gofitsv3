package ui

import (
	"context"
	"image"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

func pickerTestImage() *models.LoadedImage {
	return &models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 100, Height: 80}}}
}

func TestValidateComposeGaiaPickerGeometryUsesNativeReferenceFrame(t *testing.T) {
	img := pickerTestImage()
	if err := validateComposeGaiaPickerGeometry(img); err != nil {
		t.Fatalf("valid image rejected: %v", err)
	}
	img.Rotation90 = 1
	if err := validateComposeGaiaPickerGeometry(img); err == nil {
		t.Fatal("rotated reference should be rejected")
	}
	img.Rotation90 = 0
	img.HasAlignTransform = true
	if err := validateComposeGaiaPickerGeometry(img); err == nil {
		t.Fatal("fitted reference should be rejected")
	}
}

func TestValidateComposeGaiaPickerStarsRequiresSixInBounds(t *testing.T) {
	stars := make([]processing.Star, composeGaiaPickerMinStars-1)
	if err := validateComposeGaiaPickerStars(stars, 100, 80); err == nil {
		t.Fatal("expected six-star minimum")
	}
	stars = make([]processing.Star, composeGaiaPickerMinStars)
	for i := range stars {
		stars[i] = processing.Star{X: float64(i*10 + 1), Y: float64(i*5 + 2)}
	}
	if err := validateComposeGaiaPickerStars(stars, 100, 80); err != nil {
		t.Fatalf("valid stars rejected: %v", err)
	}
	stars[0].X = 100
	if err := validateComposeGaiaPickerStars(stars, 100, 80); err == nil {
		t.Fatal("out-of-bounds star accepted")
	}
	stars[0].X = stars[1].X + 1
	stars[0].Y = stars[1].Y + 1
	if err := validateComposeGaiaPickerStars(stars, 100, 80); err == nil {
		t.Fatal("near-duplicate stars accepted")
	}
	stars[0] = stars[1]
	if err := validateComposeGaiaPickerStars(stars, 100, 80); err == nil {
		t.Fatal("exact duplicate stars accepted")
	}
}

func TestComposeGaiaPickerCompletionSuppressedAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	if !composeGaiaPickerCompletionAllowed(ctx) {
		t.Fatal("fresh context unexpectedly rejected")
	}
	cancel()
	if composeGaiaPickerCompletionAllowed(ctx) {
		t.Fatal("cancelled context allowed completion publication")
	}
}

func TestComposeGaiaPickerCancelClearsBusyState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	running := true
	jobCancel := context.CancelFunc(cancel)
	if !composeGaiaPickerCancel(&running, &jobCancel) {
		t.Fatal("expected active job cancellation")
	}
	if running || jobCancel != nil || ctx.Err() == nil {
		t.Fatalf("cancel did not clear state: running=%t cancelNil=%t ctxErr=%v", running, jobCancel == nil, ctx.Err())
	}
	if composeGaiaPickerCancel(&running, &jobCancel) {
		t.Fatal("inactive job cancellation reported success")
	}
}

func TestComposeGaiaPickerImageUsesNativePixels(t *testing.T) {
	img := pickerTestImage()
	img.HDU.Data.Pixels = make([]float32, img.HDU.Data.Width*img.HDU.Data.Height)
	preview := image.NewRGBA(image.Rect(0, 0, 8, 8))
	got := composeGaiaPickerImage(img, preview)
	if got.Bounds().Dx() != img.HDU.Data.Width || got.Bounds().Dy() != img.HDU.Data.Height {
		t.Fatalf("native picker image bounds=%v, want %dx%d", got.Bounds(), img.HDU.Data.Width, img.HDU.Data.Height)
	}
}

func TestComposeGaiaPickerImageFallsBackToBoundedPreview(t *testing.T) {
	img := pickerTestImage()
	preview := image.NewRGBA(image.Rect(0, 0, 8, 8))
	if got := composeGaiaPickerImage(img, preview); got != preview {
		t.Fatal("disk-backed picker should retain bounded preview")
	}
}
