package ui

import (
	"image/color"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

type viewerInteractionLayer struct {
	widget.BaseWidget
	scroll        *container.Scroll
	background    *canvas.Rectangle
	line          *canvas.Line
	startMarker   *canvas.Circle
	endMarker     *canvas.Circle
	onPointerMove func(fyne.Position)
	onPointerOut  func()
	onTapped      func(fyne.Position)
	start         *fyne.Position
	end           *fyne.Position
}

func newViewerInteractionLayer() *viewerInteractionLayer {
	layer := &viewerInteractionLayer{
		background:  canvas.NewRectangle(color.Transparent),
		line:        canvas.NewLine(color.NRGBA{R: 255, G: 214, B: 10, A: 255}),
		startMarker: canvas.NewCircle(color.NRGBA{R: 255, G: 214, B: 10, A: 255}),
		endMarker:   canvas.NewCircle(color.NRGBA{R: 255, G: 99, B: 71, A: 255}),
	}
	layer.line.StrokeWidth = 2
	layer.startMarker.StrokeColor = color.NRGBA{R: 32, G: 32, B: 32, A: 255}
	layer.startMarker.StrokeWidth = 1
	layer.endMarker.StrokeColor = color.NRGBA{R: 32, G: 32, B: 32, A: 255}
	layer.endMarker.StrokeWidth = 1
	layer.ExtendBaseWidget(layer)
	layer.updateOverlay()
	return layer
}

func (d *viewerInteractionLayer) setMeasurement(start *fyne.Position, end *fyne.Position) {
	d.start = start
	d.end = end
	d.updateOverlay()
	canvas.Refresh(d)
}

func (d *viewerInteractionLayer) Dragged(e *fyne.DragEvent) {
	if d.scroll == nil {
		return
	}
	sz := d.Size()
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

func (d *viewerInteractionLayer) DragEnd() {}

func (d *viewerInteractionLayer) Tapped(e *fyne.PointEvent) {
	if d.onTapped != nil {
		d.onTapped(e.Position)
	}
}

func (d *viewerInteractionLayer) TappedSecondary(*fyne.PointEvent) {}

func (d *viewerInteractionLayer) MouseIn(e *desktop.MouseEvent) {
	if d.onPointerMove != nil {
		d.onPointerMove(e.Position)
	}
}

func (d *viewerInteractionLayer) MouseMoved(e *desktop.MouseEvent) {
	if d.onPointerMove != nil {
		d.onPointerMove(e.Position)
	}
}

func (d *viewerInteractionLayer) MouseOut() {
	if d.onPointerOut != nil {
		d.onPointerOut()
	}
}

func (d *viewerInteractionLayer) Cursor() desktop.Cursor {
	return desktop.PointerCursor
}

func (d *viewerInteractionLayer) CreateRenderer() fyne.WidgetRenderer {
	content := container.NewWithoutLayout(d.background, d.line, d.startMarker, d.endMarker)
	return &viewerInteractionRenderer{layer: d, content: content}
}

func (d *viewerInteractionLayer) MinSize() fyne.Size {
	return fyne.NewSize(10, 10)
}

func (d *viewerInteractionLayer) updateOverlay() {
	const markerDiameter = float32(10)
	d.background.Resize(d.Size())
	if d.start != nil {
		d.startMarker.Move(fyne.NewPos(d.start.X-markerDiameter/2, d.start.Y-markerDiameter/2))
		d.startMarker.Resize(fyne.NewSize(markerDiameter, markerDiameter))
		d.startMarker.Show()
	} else {
		d.startMarker.Hide()
	}
	if d.end != nil {
		d.endMarker.Move(fyne.NewPos(d.end.X-markerDiameter/2, d.end.Y-markerDiameter/2))
		d.endMarker.Resize(fyne.NewSize(markerDiameter, markerDiameter))
		d.endMarker.Show()
	} else {
		d.endMarker.Hide()
	}
	if d.start != nil && d.end != nil {
		d.line.Position1 = *d.start
		d.line.Position2 = *d.end
		d.line.Show()
	} else {
		d.line.Hide()
	}
}

type viewerInteractionRenderer struct {
	layer   *viewerInteractionLayer
	content *fyne.Container
}

func (r *viewerInteractionRenderer) Destroy() {}

func (r *viewerInteractionRenderer) Layout(size fyne.Size) {
	r.content.Resize(size)
	r.layer.updateOverlay()
}

func (r *viewerInteractionRenderer) MinSize() fyne.Size {
	return fyne.NewSize(10, 10)
}

func (r *viewerInteractionRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.content}
}

func (r *viewerInteractionRenderer) Refresh() {
	r.layer.updateOverlay()
	canvas.Refresh(r.content)
}
