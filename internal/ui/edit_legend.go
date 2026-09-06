package ui

import (
	"image"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

type editLegendData struct {
	entries  []legendEntry
	position image.Point
	scale    float64
}

type editLegendLayer struct {
	widget.BaseWidget
	image          *canvas.Image
	data           *editLegendData
	zoom           float64
	onMove         func(image.Point)
	dragging       bool
	boundW, boundH int
}

func newEditLegendLayer() *editLegendLayer {
	l := &editLegendLayer{image: canvas.NewImageFromImage(nil), zoom: 1}
	l.image.Hide()
	l.ExtendBaseWidget(l)
	return l
}

func (l *editLegendLayer) SetLegend(data *editLegendData) {
	l.data = data
	if data == nil || len(data.entries) == 0 {
		l.image.Hide()
	} else {
		l.image.Image = renderScaledLegend(data.entries, data.scale)
		l.image.Show()
	}
	l.layout()
	canvas.Refresh(l)
}
func (l *editLegendLayer) SetZoom(z float64) { l.zoom = z; l.layout(); canvas.Refresh(l) }
func (l *editLegendLayer) Dragged(e *fyne.DragEvent) {
	if l.data == nil || l.zoom <= 0 {
		return
	}
	if !l.dragging {
		p := l.data.position
		b := l.image.Image.Bounds()
		if e.Position.X < float32(p.X)*float32(l.zoom) || e.Position.X >= float32(p.X+b.Dx())*float32(l.zoom) || e.Position.Y < float32(p.Y)*float32(l.zoom) || e.Position.Y >= float32(p.Y+b.Dy())*float32(l.zoom) {
			return
		}
		l.dragging = true
	}
	if l.boundW > 0 && l.boundH > 0 {
		b := l.image.Image.Bounds()
		l.data.position.X = maxEditInt(0, minEditInt(l.boundW-b.Dx(), l.data.position.X+int(math.Round(float64(e.Dragged.DX)/l.zoom))))
		l.data.position.Y = maxEditInt(0, minEditInt(l.boundH-b.Dy(), l.data.position.Y+int(math.Round(float64(e.Dragged.DY)/l.zoom))))
	} else {
		l.data.position.X += int(math.Round(float64(e.Dragged.DX) / l.zoom))
		l.data.position.Y += int(math.Round(float64(e.Dragged.DY) / l.zoom))
	}
	if l.onMove != nil {
		l.onMove(l.data.position)
	}
	l.layout()
	canvas.Refresh(l)
}
func (l *editLegendLayer) DragEnd()                            { l.dragging = false }
func (l *editLegendLayer) Tapped(*fyne.PointEvent)             {}
func (l *editLegendLayer) CreateRenderer() fyne.WidgetRenderer { return &editLegendRenderer{l: l} }
func (l *editLegendLayer) layout() {
	if l.data == nil || l.image.Image == nil {
		return
	}
	p := l.data.position
	l.image.Move(fyne.NewPos(float32(p.X)*float32(l.zoom), float32(p.Y)*float32(l.zoom)))
	b := l.image.Image.Bounds()
	l.image.Resize(fyne.NewSize(float32(b.Dx())*float32(l.zoom), float32(b.Dy())*float32(l.zoom)))
}

type editLegendRenderer struct{ l *editLegendLayer }

func (r *editLegendRenderer) Destroy()                     {}
func (r *editLegendRenderer) Layout(size fyne.Size)        { r.l.layout() }
func (r *editLegendRenderer) MinSize() fyne.Size           { return fyne.NewSize(1, 1) }
func (r *editLegendRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.l.image} }
func (r *editLegendRenderer) Refresh()                     { r.l.layout(); canvas.Refresh(r.l.image) }

func renderScaledLegend(entries []legendEntry, scale float64) image.Image {
	if scale <= 0 {
		scale = 2
	}
	return renderColorLegendScale(entries, scale)
}
func maxEditInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (es *editWorkspaceState) setLegend(entries []legendEntry) {
	if len(entries) == 0 {
		es.legend = nil
		if es.legendLayer != nil {
			es.legendLayer.SetLegend(nil)
		}
		return
	}
	c := append([]legendEntry(nil), entries...)
	d := &editLegendData{entries: c, scale: 2}
	// Bottom-right default; clamp after dimensions are known.
	img := renderScaledLegend(c, d.scale)
	d.position = image.Pt(maxEditInt(0, es.origW-img.Bounds().Dx()-16), maxEditInt(0, es.origH-img.Bounds().Dy()-16))
	es.legend = d
	if es.legendLayer != nil {
		es.legendLayer.boundW, es.legendLayer.boundH = es.origW, es.origH
	}
	if es.legendSizeSlider != nil {
		es.legendSizeSlider.SetValue(200)
	}
	if es.legendLayer != nil {
		es.legendLayer.SetLegend(d)
		es.legendLayer.SetZoom(es.zoom)
	}
}

func (es *editWorkspaceState) setLegendScale(v float64) {
	if es.legend == nil {
		return
	}
	es.legend.scale = math.Max(1, math.Min(6, v/100))
	es.legendLayer.SetLegend(es.legend)
}

func (es *editWorkspaceState) translateLegendForCrop(origin image.Point, width, height int) {
	if es.legend == nil {
		return
	}
	es.legend.position.X -= origin.X
	es.legend.position.Y -= origin.Y
	img := renderScaledLegend(es.legend.entries, es.legend.scale)
	es.legend.position.X = maxEditInt(0, minEditInt(width-img.Bounds().Dx(), es.legend.position.X))
	es.legend.position.Y = maxEditInt(0, minEditInt(height-img.Bounds().Dy(), es.legend.position.Y))
	if es.legendLayer != nil {
		es.legendLayer.boundW, es.legendLayer.boundH = width, height
		es.legendLayer.SetLegend(es.legend)
	}
}
