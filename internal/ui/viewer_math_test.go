package ui

import (
	"math"
	"testing"

	"fyne.io/fyne/v2"
)

func TestMapViewportPositionToImage(t *testing.T) {
	point, ok := mapViewportPositionToImage(fyne.NewPos(25, 35), fyne.NewPos(0, 0), 10, 20, 20, false)
	if !ok {
		t.Fatal("expected position to map inside image")
	}
	if point.X != 2 || point.Y != 3 {
		t.Fatalf("got (%d,%d), want (2,3)", point.X, point.Y)
	}
}

func TestMapViewportPositionToImageWithScroll(t *testing.T) {
	point, ok := mapViewportPositionToImage(fyne.NewPos(5, 15), fyne.NewPos(20, 30), 10, 20, 20, false)
	if !ok {
		t.Fatal("expected scrolled position to map inside image")
	}
	if point.X != 2 || point.Y != 4 {
		t.Fatalf("got (%d,%d), want (2,4)", point.X, point.Y)
	}
}

func TestMapViewportPositionToImageFlipped(t *testing.T) {
	point, ok := mapViewportPositionToImage(fyne.NewPos(12, 15), fyne.NewPos(0, 0), 10, 5, 5, true)
	if !ok {
		t.Fatal("expected flipped position to map inside image")
	}
	if point.X != 1 || point.Y != 3 {
		t.Fatalf("got (%d,%d), want (1,3)", point.X, point.Y)
	}
}

func TestImagePointToCanvasPositionFlipped(t *testing.T) {
	pos := imagePointToCanvasPosition(imagePoint{X: 2, Y: 1}, 10, 5, true)
	if pos.X != 25 || pos.Y != 35 {
		t.Fatalf("got (%.0f,%.0f), want (25,35)", pos.X, pos.Y)
	}
}

func TestMeasurePoints(t *testing.T) {
	measurement := measurePoints(imagePoint{X: 4, Y: 7}, imagePoint{X: 10, Y: 3})
	if measurement.DX != 6 || measurement.DY != -4 {
		t.Fatalf("got dx=%d dy=%d, want dx=6 dy=-4", measurement.DX, measurement.DY)
	}
	if math.Abs(measurement.Distance-math.Hypot(6, -4)) > 1e-9 {
		t.Fatalf("got distance=%f", measurement.Distance)
	}
}
