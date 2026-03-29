package ui

import (
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

type dragLayer struct {
	widget.BaseWidget
	scroll  *container.Scroll
	content fyne.CanvasObject
}

func newDragLayer(scroll *container.Scroll, content fyne.CanvasObject) *dragLayer {
	d := &dragLayer{scroll: scroll, content: content}
	d.ExtendBaseWidget(d)
	return d
}

func (d *dragLayer) Dragged(e *fyne.DragEvent) {
	if d.scroll == nil || d.content == nil {
		return
	}
	sz := d.content.Size()
	viewport := d.scroll.Size()
	maxX := float32(math.Max(0, float64(sz.Width-viewport.Width)))
	maxY := float32(math.Max(0, float64(sz.Height-viewport.Height)))
	nx := d.scroll.Offset.X - e.Dragged.DX
	ny := d.scroll.Offset.Y - e.Dragged.DY
	if nx < 0 {
		nx = 0
	}
	if ny < 0 {
		ny = 0
	}
	if nx > maxX {
		nx = maxX
	}
	if ny > maxY {
		ny = maxY
	}
	d.scroll.Offset = fyne.NewPos(nx, ny)
	d.scroll.Refresh()
}

func (d *dragLayer) DragEnd() {}

func (d *dragLayer) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(d.content)
}

func (d *dragLayer) Cursor() desktop.Cursor {
	return desktop.PointerCursor
}

func (d *dragLayer) MinSize() fyne.Size {
	if d.content == nil {
		return fyne.NewSize(10, 10)
	}
	return d.content.MinSize()
}
