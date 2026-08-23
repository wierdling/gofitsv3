package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
	"testing"
)

func TestSidePaddedLayoutClampsSmallContainers(t *testing.T) {
	obj := widget.NewLabel("")
	(&sidePaddedLayout{pad: 20}).Layout([]fyne.CanvasObject{obj}, fyne.NewSize(10, -4))
	if got := obj.Size(); got.Width != 0 || got.Height != 0 {
		t.Fatalf("size = %v, want 0x0", got)
	}
}
