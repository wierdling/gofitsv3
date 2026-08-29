package ui

import (
	"image"
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func TestEditWorkingImageResetAndCommitUseDetachedCopies(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 1, 1))
	base.SetRGBA(0, 0, color.RGBA{R: 10, A: 255})
	es := &editWorkspaceState{base: base}

	if !es.resetWorkingToBase() {
		t.Fatal("resetWorkingToBase returned false")
	}
	es.working.SetRGBA(0, 0, color.RGBA{R: 20, A: 255})
	if got := es.base.RGBAAt(0, 0).R; got != 10 {
		t.Fatalf("base mutated through working image: got %d, want 10", got)
	}

	es.commitWorkingToBase()
	es.working.SetRGBA(0, 0, color.RGBA{R: 30, A: 255})
	if got := es.base.RGBAAt(0, 0).R; got != 20 {
		t.Fatalf("committed base mutated through working image: got %d, want 20", got)
	}

	if !es.resetWorkingToBase() {
		t.Fatal("second resetWorkingToBase returned false")
	}
	if got := es.working.RGBAAt(0, 0).R; got != 20 {
		t.Fatalf("reset working pixel = %d, want 20", got)
	}
}

func TestSetDiskEditTabAvailabilityEnablesAllDiskTools(t *testing.T) {
	tabs := container.NewAppTabs(
		container.NewTabItem("Levels", widget.NewLabel("levels")),
		container.NewTabItem("Curves", widget.NewLabel("curves")),
		container.NewTabItem("Sharpen", widget.NewLabel("sharpen")),
		container.NewTabItem("Clean", widget.NewLabel("clean")),
		container.NewTabItem("Heal", widget.NewLabel("heal")),
		container.NewTabItem("Crop", widget.NewLabel("crop")),
	)
	setDiskEditTabAvailability(tabs)
	for i := 0; i < 3; i++ {
		if tabs.Items[i].Disabled() {
			t.Fatalf("tab %d unexpectedly disabled", i)
		}
	}
	if tabs.Items[3].Disabled() {
		t.Fatal("clean tab unexpectedly disabled")
	}
	for i := 4; i < len(tabs.Items); i++ {
		if tabs.Items[i].Disabled() {
			t.Fatalf("disk tool tab %d unexpectedly disabled", i)
		}
	}
}

func TestSetDiskSourceInitializesFitViewportForCoordinateMapping(t *testing.T) {
	store, planes := newSpatialStoreForTest(t)
	disk := &editDiskSource{planes: planes, width: 2, height: 2, store: store}
	defer disk.cleanup()
	img := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 2, 2)))
	scroll := container.NewScroll(img)
	scroll.Resize(fyne.NewSize(400, 300))
	es := &editWorkspaceState{
		canvasImg:  img,
		imgScroll:  scroll,
		rMinSlider: widget.NewSlider(0, 255),
		rMaxSlider: widget.NewSlider(0, 255),
		gMinSlider: widget.NewSlider(0, 255),
		gMaxSlider: widget.NewSlider(0, 255),
		bMinSlider: widget.NewSlider(0, 255),
		bMaxSlider: widget.NewSlider(0, 255),
	}
	if err := es.setDiskSource(disk); err != nil {
		t.Fatal(err)
	}
	if es.zoom != 150 {
		t.Fatalf("disk source fit zoom = %v, want 150", es.zoom)
	}
	if got := es.canvasImg.MinSize(); got.Width != 300 || got.Height != 300 {
		t.Fatalf("disk source viewport min size = %v, want 300x300", got)
	}
}

func TestDiskViewportCoordinateMappingAndCropReset(t *testing.T) {
	img := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 100, 50)))
	img.Resize(fyne.NewSize(300, 200))
	es := &editWorkspaceState{canvasImg: img, origW: 100, origH: 50, zoom: 2}
	got, ok := es.screenToImagePt(fyne.NewPos(150, 100))
	if !ok || got != (image.Point{X: 50, Y: 25}) {
		t.Fatalf("viewport center mapped to %v, %v; want (50,25), true", got, ok)
	}

	es.cropOverlay = newCropLayer()
	es.healOverlay = newHealLayer()
	es.cropActive = true
	es.healActive = true
	es.cropHasSel = true
	es.healUndo = image.NewRGBA(image.Rect(0, 0, 1, 1))
	es.cropStatusLabel = widget.NewLabel("stale crop")
	es.healStatusLabel = widget.NewLabel("stale heal")
	es.resetInteractionStateAfterCrop()
	if es.cropHasSel || es.healUndo != nil || es.cropOverlay.hasSel || es.healOverlay.srcPos != nil {
		t.Fatal("crop did not clear stale crop/heal interaction state")
	}
	if es.cropStatusLabel.Text != "Drag a rectangle over the image, then Apply Crop." || es.healStatusLabel.Text != "Step 1: click source (sample area)" {
		t.Fatalf("crop reset statuses: crop=%q heal=%q", es.cropStatusLabel.Text, es.healStatusLabel.Text)
	}
}
