package ui

import (
	"math"
	"testing"

	"gofitsv3/internal/models"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

func TestAppendHealStrokeSamplesInterpolatesLargePointerJumps(t *testing.T) {
	var samples []fyne.Position
	if !appendHealStrokeSamples(&samples, fyne.NewPos(0, 0), 5) {
		t.Fatal("expected initial sample")
	}
	if !appendHealStrokeSamples(&samples, fyne.NewPos(12, 0), 5) {
		t.Fatal("expected interpolated samples")
	}
	if len(samples) != 4 || samples[1].X != 4 || samples[2].X != 8 || samples[3].X != 12 {
		t.Fatalf("samples = %#v, want four evenly spaced points", samples)
	}
}

func TestCurvesDrawHandlesDegenerateSize(t *testing.T) {
	cw := newCurvesWidget(nil)
	for _, size := range [][2]int{{0, 0}, {1, 1}, {1, 20}, {20, 1}} {
		img := cw.draw(size[0], size[1])
		if img == nil {
			t.Fatalf("draw(%d,%d) returned nil", size[0], size[1])
		}
	}
}

func TestRGBLevelInputRequiresFiniteValues(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if isFiniteLevel(value) {
			t.Fatalf("isFiniteLevel(%v) = true", value)
		}
	}
	if !isFiniteLevel(12.5) {
		t.Fatal("finite level rejected")
	}
}

func TestRGBLevelsRepairKeepsBounds(t *testing.T) {
	levels := &models.RgbLevels{}
	w := &rgbLevelsWindow{levels: levels}
	for i := 0; i < 3; i++ {
		w.minEntries[i] = widget.NewEntry()
		w.maxEntries[i] = widget.NewEntry()
		w.minEntries[i].SetText("255")
		w.maxEntries[i].SetText("255")
	}
	w.applyLevels(nil)
	for i := 0; i < 3; i++ {
		if levels.Min[i] != 254 || levels.Max[i] != 255 {
			t.Fatalf("equal upper levels[%d] = (%v,%v), want (254,255)", i, levels.Min[i], levels.Max[i])
		}
		w.minEntries[i].SetText("200")
		w.maxEntries[i].SetText("100")
	}
	w.applyLevels(nil)
	for i := 0; i < 3; i++ {
		if levels.Min[i] < 0 || levels.Min[i] >= levels.Max[i] || levels.Max[i] > 255 {
			t.Fatalf("reversed levels[%d] = (%v,%v), outside bounded range", i, levels.Min[i], levels.Max[i])
		}
	}
}

func TestViewportRetainsRenderStatusWhenActionChanges(t *testing.T) {
	vp := newViewport()
	vp.SetCenterAction("R", "R", nil, "Edit", func() {})
	for _, obj := range vp.actionRow.Objects {
		if obj == vp.renderStatus {
			return
		}
	}
	t.Fatal("render status was dropped when action row changed")
}

func TestViewportLetterboxMeasurementMapping(t *testing.T) {
	vp := newViewport()
	vp.origW, vp.origH, vp.zoom = 100, 50, 1
	vp.overlay.Resize(fyne.NewSize(200, 200))
	point, ok := vp.imagePointAtPosition(fyne.NewPos(100, 100), false)
	if !ok || point != (imagePoint{X: 50, Y: 25}) {
		t.Fatalf("center letterbox maps to %#v, %v", point, ok)
	}
	vp.setMeasurementOverlay(&imagePoint{X: 0, Y: 0}, nil, false)
	if got := *vp.overlay.start; got.X != 50.5 || got.Y != 75.5 {
		t.Fatalf("measurement marker = %#v, want (50.5,75.5)", got)
	}
}
