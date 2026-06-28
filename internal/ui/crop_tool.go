package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// cropLayer is a transparent overlay placed on top of the canvas image.
// It drives a rubber-band rectangle selection: press and drag to draw the
// selection, release to commit. A single click (no drag) clears the selection.
// On every change it reports the current selection rectangle in widget/screen
// coordinates via onChange; the caller converts those to image pixels.
type cropLayer struct {
	widget.BaseWidget
	dragging bool
	hasSel   bool
	start    fyne.Position
	cur      fyne.Position

	background *canvas.Rectangle
	selRect    *canvas.Rectangle

	// onChange fires whenever the selection changes. active is false when the
	// selection has been cleared.
	onChange func(min, max fyne.Position, active bool)
}

func newCropLayer() *cropLayer {
	c := &cropLayer{
		background: canvas.NewRectangle(color.Transparent),
		selRect:    canvas.NewRectangle(color.Transparent),
	}
	c.selRect.StrokeColor = color.NRGBA{R: 80, G: 200, B: 255, A: 230}
	c.selRect.StrokeWidth = 2
	c.selRect.FillColor = color.NRGBA{R: 80, G: 200, B: 255, A: 40}
	c.selRect.Hide()
	c.ExtendBaseWidget(c)
	return c
}

// Reset clears any active selection.
func (c *cropLayer) Reset() {
	c.dragging = false
	c.hasSel = false
	c.selRect.Hide()
	canvas.Refresh(c)
}

// clampToBounds keeps a position inside the widget rectangle.
func (c *cropLayer) clampToBounds(p fyne.Position) fyne.Position {
	sz := c.Size()
	if p.X < 0 {
		p.X = 0
	}
	if p.Y < 0 {
		p.Y = 0
	}
	if p.X > sz.Width {
		p.X = sz.Width
	}
	if p.Y > sz.Height {
		p.Y = sz.Height
	}
	return p
}

// selectionCorners returns the top-left and bottom-right of the current drag.
func (c *cropLayer) selectionCorners() (fyne.Position, fyne.Position) {
	minX, maxX := c.start.X, c.cur.X
	if minX > maxX {
		minX, maxX = maxX, minX
	}
	minY, maxY := c.start.Y, c.cur.Y
	if minY > maxY {
		minY, maxY = maxY, minY
	}
	return fyne.NewPos(minX, minY), fyne.NewPos(maxX, maxY)
}

func (c *cropLayer) updateSelRect() {
	min, max := c.selectionCorners()
	c.selRect.Move(min)
	c.selRect.Resize(fyne.NewSize(max.X-min.X, max.Y-min.Y))
	c.selRect.Show()
	c.selRect.Refresh()
}

// Tapped clears the selection on a plain click.
func (c *cropLayer) Tapped(*fyne.PointEvent) {
	if c.hasSel {
		c.Reset()
		if c.onChange != nil {
			c.onChange(fyne.Position{}, fyne.Position{}, false)
		}
	}
}

func (c *cropLayer) TappedSecondary(*fyne.PointEvent) {}

// Dragged extends the rubber-band rectangle.
func (c *cropLayer) Dragged(e *fyne.DragEvent) {
	if !c.dragging {
		c.dragging = true
		// The first drag event is already offset by its delta, so back it out
		// to recover the true anchor point.
		c.start = c.clampToBounds(fyne.NewPos(e.Position.X-e.Dragged.DX, e.Position.Y-e.Dragged.DY))
	}
	c.cur = c.clampToBounds(e.Position)
	c.hasSel = true
	c.updateSelRect()
	canvas.Refresh(c)
	if c.onChange != nil {
		min, max := c.selectionCorners()
		c.onChange(min, max, true)
	}
}

// DragEnd commits the selection.
func (c *cropLayer) DragEnd() {
	c.dragging = false
	if c.onChange != nil && c.hasSel {
		min, max := c.selectionCorners()
		c.onChange(min, max, true)
	}
}

func (c *cropLayer) CreateRenderer() fyne.WidgetRenderer {
	c.background.Resize(c.Size())
	content := container.NewWithoutLayout(c.background, c.selRect)
	return &cropLayerRenderer{layer: c, content: content}
}

func (c *cropLayer) MinSize() fyne.Size { return fyne.NewSize(10, 10) }

type cropLayerRenderer struct {
	layer   *cropLayer
	content *fyne.Container
}

func (r *cropLayerRenderer) Layout(size fyne.Size) {
	r.layer.background.Resize(size)
	r.content.Resize(size)
}
func (r *cropLayerRenderer) MinSize() fyne.Size           { return fyne.NewSize(10, 10) }
func (r *cropLayerRenderer) Refresh()                     { canvas.Refresh(r.content) }
func (r *cropLayerRenderer) Destroy()                     {}
func (r *cropLayerRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.content} }
