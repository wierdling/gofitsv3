package ui

import (
	"image"
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
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

func TestLegendScaleRendersFreshSemanticLayout(t *testing.T) {
	entries := []legendEntry{{name: "Blue", color: color.RGBA{B: 255, A: 255}}}
	small := renderColorLegendScale(entries, 1)
	large := renderColorLegendScale(entries, 3)
	if large.Bounds().Dx() <= small.Bounds().Dx()*2 || large.Bounds().Dy() <= small.Bounds().Dy()*2 {
		t.Fatalf("scaled legend dimensions = %v, want substantially larger than %v", large.Bounds(), small.Bounds())
	}
	// The label glyphs use the scalable face too, so a large legend must grow
	// beyond geometry-only scaling caused by fixed-size text metrics.
	if large.Bounds().Dx() < small.Bounds().Dx()*3-10 || large.Bounds().Dy() < small.Bounds().Dy()*3-10 {
		t.Fatalf("scaled glyph layout did not track scale: small=%v large=%v", small.Bounds(), large.Bounds())
	}
	f, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face1, _ := opentype.NewFace(f, &opentype.FaceOptions{Size: 13, DPI: 72})
	face3, _ := opentype.NewFace(f, &opentype.FaceOptions{Size: 39, DPI: 72})
	if font.MeasureString(face3, "Legend").Ceil() < font.MeasureString(face1, "Legend").Ceil()*2 {
		t.Fatal("scalable legend font metrics did not grow")
	}
}

func TestLegendCropTranslationClampsToNewImage(t *testing.T) {
	es := &editWorkspaceState{origW: 100, origH: 100, legend: &editLegendData{entries: []legendEntry{{name: "x"}}, position: image.Pt(80, 80), scale: 1}}
	es.translateLegendForCrop(image.Pt(50, 50), 20, 20)
	b := renderScaledLegend(es.legend.entries, es.legend.scale).Bounds()
	if es.legend.position.X != maxEditInt(0, 20-b.Dx()) || es.legend.position.Y != maxEditInt(0, 20-b.Dy()) {
		t.Fatalf("legend position after crop = %v, want clamped within 20x20 for %v", es.legend.position, b)
	}

	es.legend.position = image.Pt(52, 54)
	es.translateLegendForCrop(image.Pt(50, 50), 200, 200)
	if es.legend.position != (image.Point{X: 2, Y: 4}) {
		t.Fatalf("legend crop translation = %v, want (2,4)", es.legend.position)
	}
}

func TestLegendDragRequiresHitAndClampsPosition(t *testing.T) {
	l := newEditLegendLayer()
	l.zoom = 1
	l.boundW, l.boundH = 20, 20
	l.data = &editLegendData{entries: []legendEntry{{name: "x"}}, position: image.Pt(10, 10), scale: 1}
	l.image.Image = renderScaledLegend(l.data.entries, 1)
	l.layout()
	start := l.data.position
	l.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(0, 0)}, Dragged: fyne.Delta{DX: 5, DY: 5}})
	if l.data.position != start {
		t.Fatal("drag outside legend changed position")
	}
	l.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(11, 11)}, Dragged: fyne.Delta{DX: 100, DY: 100}})
	b := l.image.Image.Bounds()
	if l.data.position.X != maxEditInt(0, 20-b.Dx()) || l.data.position.Y != maxEditInt(0, 20-b.Dy()) {
		t.Fatalf("position not clamped: %v", l.data.position)
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

func TestEditControlsSplitUsesCompactResizableLayout(t *testing.T) {
	split := newEditControlsSplit(widget.NewLabel("controls"), widget.NewLabel("preview"))

	if split.Offset != editControlsSplitOffset {
		t.Fatalf("edit split offset = %v, want %v", split.Offset, editControlsSplitOffset)
	}
	controlsScroll, ok := split.Leading.(*container.Scroll)
	if !ok {
		t.Fatalf("leading edit control is %T, want *container.Scroll", split.Leading)
	}
	if got := controlsScroll.MinSize(); got.Width != editControlsMinWidth || got.Height != 200 {
		t.Fatalf("edit controls minimum size = %v, want %vx200", got, editControlsMinWidth)
	}
}

func TestEditExplanatoryLabelsWrapToAvailableWidth(t *testing.T) {
	for _, text := range []string{
		"Targets small red, green, or blue cosmic-ray leftovers and near-black dropout dots in the composed RGB image.",
		"Turn the tool on, drag a rectangle over the image, then apply. The cropped result becomes the new edit base.",
		"Run this after cross-channel clean to remove tiny color specks and black dropout dots.",
		"Step 2: click/drag destination to heal",
		"Drag a rectangle over the image, then Apply Crop.",
	} {
		label := newWrappedEditLabel(text)
		if label.Wrapping != fyne.TextWrapWord {
			t.Fatalf("label wrapping = %v, want word wrapping", label.Wrapping)
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
