package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

func visitComposeObjects(obj fyne.CanvasObject, visit func(fyne.CanvasObject)) {
	visit(obj)
	if c, ok := obj.(*fyne.Container); ok {
		for _, child := range c.Objects {
			visitComposeObjects(child, visit)
		}
	}
}

func triggerComposeControl(t *testing.T, control *models.ChannelControl, label string) {
	t.Helper()
	var found bool
	visitComposeObjects(control.Content, func(obj fyne.CanvasObject) {
		if found {
			return
		}
		switch v := obj.(type) {
		case *widget.Button:
			if v.Text == label {
				found = true
				v.Tapped(nil)
			}
		case *widget.Select:
			if label == "mode" {
				found = true
				v.SetSelected("Log")
			}
		case *Toggle:
			if label == "show clip" {
				found = true
				v.Tapped(nil)
			}
		}
	})
	if !found {
		t.Fatalf("control %q not found", label)
	}
}

func composeGaiaInvalidationTestImages() []*models.LoadedImage {
	imgs := make([]*models.LoadedImage, 3)
	for i := range imgs {
		pixels := make([]float32, 16)
		for p := range pixels {
			pixels[p] = float32(p + i + 1)
		}
		imgs[i] = &models.LoadedImage{Path: "channel.fits", HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 4, Height: 4, Pixels: pixels}}}
	}
	return imgs
}

func TestChannel2NormalControlsInvalidateGaiaRefinement(t *testing.T) {
	previous := globalComposeGaiaRefinementInvalidate
	defer func() { globalComposeGaiaRefinementInvalidate = previous }()
	var generation int
	globalComposeGaiaRefinementInvalidate = func() { generation++ }

	imgs := composeGaiaInvalidationTestImages()
	views := []*viewport{newViewport(), newViewport(), newViewport()}
	preset := widget.NewSelect([]string{"Balanced"}, nil)
	control := channelControls("Channel 2", color.RGBA{G: 255, A: 255}, 1, imgs, &[][]float32{}, views, func() {}, preset, true)
	for _, operation := range []string{"mode", "show clip", "Apply", "Auto scaling", "Auto MTF", "Magic", "Apply Offset", "Rotate 90°"} {
		before := generation
		triggerComposeControl(t, control, operation)
		if generation <= before {
			t.Fatalf("Channel 2 %s did not advance invalidation (generation %d -> %d)", operation, before, generation)
		}
	}
}

func TestNonChannel2NormalControlsDoNotInvalidateGaiaRefinement(t *testing.T) {
	previous := globalComposeGaiaRefinementInvalidate
	defer func() { globalComposeGaiaRefinementInvalidate = previous }()
	var generation int
	globalComposeGaiaRefinementInvalidate = func() { generation++ }

	imgs := composeGaiaInvalidationTestImages()
	views := []*viewport{newViewport(), newViewport(), newViewport()}
	preset := widget.NewSelect([]string{"Balanced"}, nil)
	control := channelControls("Channel 1", color.RGBA{B: 255, A: 255}, 0, imgs, &[][]float32{}, views, func() {}, preset, true)
	for _, operation := range []string{"mode", "show clip", "Apply", "Auto scaling", "Auto MTF", "Magic", "Apply Offset", "Rotate 90°"} {
		triggerComposeControl(t, control, operation)
	}
	if generation != 0 {
		t.Fatalf("non-Channel-2 controls advanced invalidation by %d", generation)
	}
}
