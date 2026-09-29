package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	fynetest "fyne.io/fyne/v2/test"

	"gofitsv3/internal/processing"
)

func TestDiagnosticOverlaySkippedHoverCircle(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	vp := newViewport()
	vp.origW, vp.origH = 200, 200
	vp.overlay.Resize(fyne.NewSize(200, 200))
	points := []processing.StarTreatmentDiagnostic{
		{X: 50, Y: 50, Usable: true},
		{X: 50, Y: 50, Usable: false},
		{X: 100, Y: 100, Usable: false},
	}
	vp.setDiagnosticOverlayWithForce(points, []bool{false, false, true})
	layer := vp.overlay
	if layer.markerCircles[1].Visible() {
		t.Fatal("skipped circle visible without hover")
	}
	layer.MouseIn(&desktop.MouseEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(50, 50)}})
	if !layer.markerCircles[1].Visible() || layer.forceGlyph.Visible() {
		t.Fatal("skipped hover should show a circle without a force glyph")
	}
	if got := layer.markerCircles[1].StrokeColor; got != (color.RGBA{R: 230, G: 40, B: 40, A: 230}) {
		t.Fatalf("skipped circle color = %v, want red", got)
	}
	if idx, ok := layer.nearestMarker(fyne.NewPos(50, 50)); !ok || idx != 1 {
		t.Fatalf("click selected (%d, %t), want hovered skipped star 1", idx, ok)
	}
	layer.MouseMoved(&desktop.MouseEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(100, 100)}})
	if layer.markerCircles[1].Visible() || layer.markerCircles[2].Visible() || !layer.forceGlyph.Visible() {
		t.Fatal("moving to force-eligible star should replace red circle with plus")
	}
	layer.MouseOut()
	if layer.forceGlyph.Visible() || layer.markerCircles[1].Visible() {
		t.Fatal("hover decoration remains after mouse out")
	}
	layer.updateMarkerHover(fyne.NewPos(50, 50))
	vp.setDiagnosticOverlay(nil)
	if layer.markerCircles[1].Visible() || layer.hoverMarker != -1 {
		t.Fatal("clearing diagnostics retained skipped hover")
	}
}

func TestDiagnosticOverlayToggleRefreshesWithoutResize(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	vp := newViewport()
	vp.overlay.Resize(fyne.NewSize(200, 200))
	renderer := fynetest.WidgetRenderer(vp.overlay)
	content := renderer.Objects()[0].(*fyne.Container)
	visibleCircles := func() int {
		n := 0
		for _, obj := range content.Objects {
			if _, ok := obj.(*canvas.Circle); ok && obj.Visible() {
				n++
			}
		}
		return n
	}
	points := []processing.StarTreatmentDiagnostic{
		{X: 40, Y: 40, Usable: true},
		{X: 80, Y: 80, Usable: false},
	}
	for _, step := range []struct {
		name   string
		points []processing.StarTreatmentDiagnostic
		want   int
	}{
		{"enable", points, 1},
		{"disable", nil, 0},
		{"enable again", points, 1},
	} {
		t.Run(step.name, func(t *testing.T) {
			vp.setDiagnosticOverlay(step.points)
			if got := visibleCircles(); got != step.want {
				t.Fatalf("visible diagnostic circles = %d, want %d without resizing", got, step.want)
			}
		})
	}
}

func TestDiagnosticOverlayFollowsLayoutSize(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	vp := newViewport()
	vp.origW, vp.origH = 400, 200
	vp.zoom = 0.5
	points := []processing.StarTreatmentDiagnostic{{X: 100, Y: 50, Usable: true}}
	// Compose refreshes its diagnostics from this callback on Max/Restore.
	vp.overlay.onResized = func(fyne.Size) { vp.setDiagnosticOverlay(points) }
	for _, step := range []struct {
		name string
		size fyne.Size
		want fyne.Position
	}{
		{"channel", fyne.NewSize(200, 100), fyne.NewPos(50, 25)},
		{"max wide", fyne.NewSize(800, 300), fyne.NewPos(250, 75)},
		{"max tall", fyne.NewSize(600, 600), fyne.NewPos(150, 225)},
		{"restore", fyne.NewSize(200, 100), fyne.NewPos(50, 25)},
	} {
		t.Run(step.name, func(t *testing.T) {
			vp.overlay.Resize(step.size)
			circle := vp.overlay.markerCircles[0]
			center := circle.Position().Add(fyne.NewPos(circle.Size().Width/2, circle.Size().Height/2))
			if center != step.want {
				t.Fatalf("ring center = %v, want %v immediately after layout", center, step.want)
			}
		})
	}
}

func TestDiagnosticOverlayDrawsOnlyProcessedAndShowsForceGlyph(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	layer := newViewerInteractionLayer()
	layer.Resize(fyne.NewSize(200, 200))
	renderer := fynetest.WidgetRenderer(layer)
	content := renderer.Objects()[0].(*fyne.Container)
	layer.setMarkers([]viewerMarker{
		{Pos: fyne.NewPos(40, 40), Radius: 6, Draw: true},
		{Pos: fyne.NewPos(80, 80), Radius: 6, ForceEligible: true},
	})
	visibleCircles := 0
	for _, obj := range content.Objects {
		if c, ok := obj.(*canvas.Circle); ok && c.Visible() {
			visibleCircles++
		}
	}
	if visibleCircles != 1 {
		t.Fatalf("visible diagnostic circles = %d, want 1", visibleCircles)
	}
	layer.hoverMarker = 1
	layer.Refresh()
	if !layer.forceGlyph.Visible() {
		t.Fatal("force glyph is hidden for a hovered eligible marker")
	}
	if idx, ok := layer.forceMarkerAt(fyne.NewPos(80, 80)); !ok || idx != 1 {
		t.Fatalf("force marker hit = (%d, %t), want (1, true)", idx, ok)
	}
}

func TestDiagnosticOverlayHoverPrefersForceEligibleOverlap(t *testing.T) {
	layer := newViewerInteractionLayer()
	layer.setMarkers([]viewerMarker{
		{Pos: fyne.NewPos(50, 50), Radius: 6, Draw: true},
		{Pos: fyne.NewPos(50, 50), Radius: 6, ForceEligible: true},
	})
	layer.updateMarkerHover(fyne.NewPos(50, 50))
	if layer.hoverMarker != 1 {
		t.Fatalf("hovered marker = %d, want overlapping force-eligible marker 1", layer.hoverMarker)
	}
}
