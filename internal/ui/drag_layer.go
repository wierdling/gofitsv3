package ui

import (
	"image/color"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
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
	pickerActive  bool
	lastDragPos   fyne.Position
	start         *fyne.Position
	end           *fyne.Position

	// markers are extra colored dots unrelated to the measurement tool (e.g.
	// the star-treatment diagnostics overlay), at fixed canvas positions
	// computed by the caller (viewport.setDiagnosticOverlay). onResized fires
	// whenever this layer's actual on-screen size changes, so the caller can
	// recompute those positions against the resize that just happened; wiring
	// it is optional, so existing users of this layer are unaffected.
	markers       []viewerMarker
	markerCircles []*canvas.Circle
	forceGlyph    *canvas.Text
	hoverMarker   int
	onResized     func(fyne.Size)
}

// viewerMarker is one colored dot drawn at a fixed canvas position.
type viewerMarker struct {
	Pos             fyne.Position
	Color           color.Color
	Radius          float32
	Draw            bool
	ForceEligible   bool
	HoverDiagnostic bool
}

func newViewerInteractionLayer() *viewerInteractionLayer {
	layer := &viewerInteractionLayer{
		background:  canvas.NewRectangle(color.Transparent),
		line:        canvas.NewLine(color.NRGBA{R: 255, G: 214, B: 10, A: 255}),
		startMarker: canvas.NewCircle(color.NRGBA{R: 255, G: 214, B: 10, A: 255}),
		endMarker:   canvas.NewCircle(color.NRGBA{R: 255, G: 99, B: 71, A: 255}),
		hoverMarker: -1,
	}
	layer.forceGlyph = canvas.NewText("+", color.White)
	layer.forceGlyph.TextStyle = fyne.TextStyle{Bold: true}
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

// setMarkers replaces the extra colored-dot overlay (e.g. star-treatment
// diagnostics) with the given points, given as fixed canvas positions. Pass
// nil to clear it.
func (d *viewerInteractionLayer) setMarkers(markers []viewerMarker) {
	d.markers = markers
	d.hoverMarker = -1
	// Refresh the widget renderer so its circle pool and visibility reflect
	// the new markers immediately; a canvas repaint alone leaves them stale.
	d.Refresh()
}

func (d *viewerInteractionLayer) updateMarkerHover(pos fyne.Position) {
	idx, ok := d.nearestForceMarker(pos)
	if !ok {
		var bestDist float64
		for i, m := range d.markers {
			if !m.HoverDiagnostic {
				continue
			}
			dist := math.Hypot(float64(m.Pos.X-pos.X), float64(m.Pos.Y-pos.Y))
			if dist <= float64(m.Radius+6) && (!ok || dist < bestDist) {
				idx, ok, bestDist = i, true, dist
			}
		}
	}
	if !ok {
		idx = -1
	}
	if idx == d.hoverMarker {
		return
	}
	d.hoverMarker = idx
	d.Refresh()
}

func (d *viewerInteractionLayer) nearestForceMarker(pos fyne.Position) (int, bool) {
	best := -1
	var bestDist float32
	for i, m := range d.markers {
		if !m.ForceEligible {
			continue
		}
		dx, dy := m.Pos.X-pos.X, m.Pos.Y-pos.Y
		dist := float32(math.Hypot(float64(dx), float64(dy)))
		if dist <= m.Radius+6 && (best < 0 || dist < bestDist) {
			best, bestDist = i, dist
		}
	}
	return best, best >= 0
}

func (d *viewerInteractionLayer) forceMarkerAt(pos fyne.Position) (int, bool) {
	idx := d.hoverMarker
	if idx < 0 || idx >= len(d.markers) || !d.markers[idx].ForceEligible {
		return 0, false
	}
	m := d.markers[idx]
	if float32(math.Hypot(float64(m.Pos.X-pos.X), float64(m.Pos.Y-pos.Y))) > m.Radius+6 {
		return 0, false
	}
	return idx, true
}

// Resize overrides widget.BaseWidget.Resize to additionally notify onResized
// when this layer's actual on-screen size changes, so a caller with markers
// or a measurement baked to fixed canvas positions can recompute them against
// the box Fyne just laid the sibling image out at (SetMinSize alone does not
// synchronously resize this layer; the real size change happens later, on
// Fyne's own layout pass, which this hook observes directly).
func (d *viewerInteractionLayer) Resize(size fyne.Size) {
	prev := d.Size()
	d.BaseWidget.Resize(size)
	if size != prev && d.onResized != nil {
		d.onResized(size)
	}
}

// nearestMarker returns the index of the marker closest to pos, when within
// its own radius (plus a small tap-tolerance margin) of pos.
func (d *viewerInteractionLayer) nearestMarker(pos fyne.Position) (int, bool) {
	// Keep clicks attached to the marker visibly highlighted on hover,
	// even when another source has a star at the same coordinates.
	if i := d.hoverMarker; i >= 0 && i < len(d.markers) {
		m := d.markers[i]
		if math.Hypot(float64(m.Pos.X-pos.X), float64(m.Pos.Y-pos.Y)) <= float64(m.Radius+6) {
			return i, true
		}
	}
	best := -1
	bestDist := float32(0)
	for i, m := range d.markers {
		dx, dy := m.Pos.X-pos.X, m.Pos.Y-pos.Y
		dist := float32(math.Hypot(float64(dx), float64(dy)))
		tolerance := m.Radius + 6
		if dist <= tolerance && (best < 0 || dist < bestDist) {
			best, bestDist = i, dist
		}
	}
	return best, best >= 0
}

func (d *viewerInteractionLayer) Dragged(e *fyne.DragEvent) {
	// While picking levels we don't pan. Fyne classifies any movement of
	// 2px or more as a drag and then suppresses the Tapped event, so a normal
	// click would otherwise neither pan nor pick. Remember where the pointer
	// ended so DragEnd can complete the pick.
	if d.pickerActive {
		d.lastDragPos = e.Position
		return
	}
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

func (d *viewerInteractionLayer) DragEnd() {
	// A drag that started while picking is treated as a tap at the release
	// position, so small pointer jitter during a click still registers a pick.
	if d.pickerActive && d.onTapped != nil {
		d.onTapped(d.lastDragPos)
	}
}

func (d *viewerInteractionLayer) Tapped(e *fyne.PointEvent) {
	if d.onTapped != nil {
		d.onTapped(e.Position)
	}
}

func (d *viewerInteractionLayer) TappedSecondary(*fyne.PointEvent) {}

func (d *viewerInteractionLayer) MouseIn(e *desktop.MouseEvent) {
	d.updateMarkerHover(e.Position)
	if d.onPointerMove != nil {
		d.onPointerMove(e.Position)
	}
}

func (d *viewerInteractionLayer) MouseMoved(e *desktop.MouseEvent) {
	d.updateMarkerHover(e.Position)
	if d.onPointerMove != nil {
		d.onPointerMove(e.Position)
	}
}

func (d *viewerInteractionLayer) MouseOut() {
	d.hoverMarker = -1
	d.Refresh()
	if d.onPointerOut != nil {
		d.onPointerOut()
	}
}

func (d *viewerInteractionLayer) Cursor() desktop.Cursor {
	if d.pickerActive {
		return desktop.CrosshairCursor
	}
	return desktop.PointerCursor
}

func (d *viewerInteractionLayer) CreateRenderer() fyne.WidgetRenderer {
	content := container.NewWithoutLayout(d.syncMarkerCircles()...)
	return &viewerInteractionRenderer{layer: d, content: content}
}

// syncMarkerCircles grows d.markerCircles to a pool at least as large as
// d.markers (never shrinking it) and returns the full object list (fixed
// objects plus every pooled circle) for the content container. A circle
// beyond len(d.markers) is Hidden rather than dropped from the object list,
// matching how startMarker/endMarker are toggled elsewhere in this file:
// removing an object from the container's list on the same Refresh pass that
// clears d.markers is not reliably enough to stop it drawing, so visibility
// is the authoritative signal instead.
func (d *viewerInteractionLayer) syncMarkerCircles() []fyne.CanvasObject {
	for len(d.markerCircles) < len(d.markers) {
		d.markerCircles = append(d.markerCircles, canvas.NewCircle(color.Transparent))
	}
	objs := []fyne.CanvasObject{d.background, d.line, d.startMarker, d.endMarker}
	for i, c := range d.markerCircles {
		if i >= len(d.markers) {
			c.Hide()
			objs = append(objs, c)
			continue
		}
		m := d.markers[i]
		c.FillColor = color.Transparent
		c.StrokeColor = m.Color
		c.StrokeWidth = 2
		r := m.Radius
		if r <= 0 {
			r = 5
		}
		c.Resize(fyne.NewSize(r*2, r*2))
		c.Move(fyne.NewPos(m.Pos.X-r, m.Pos.Y-r))
		if m.Draw || (i == d.hoverMarker && m.HoverDiagnostic && !m.ForceEligible) {
			c.Show()
		} else {
			c.Hide()
		}
		c.Refresh()
		objs = append(objs, c)
	}
	if d.hoverMarker >= 0 && d.hoverMarker < len(d.markers) && d.markers[d.hoverMarker].ForceEligible {
		m := d.markers[d.hoverMarker]
		d.forceGlyph.TextSize = theme.TextSize() + 4
		s := d.forceGlyph.MinSize()
		d.forceGlyph.Move(fyne.NewPos(m.Pos.X-s.Width/2, m.Pos.Y-s.Height/2))
		d.forceGlyph.Resize(s)
		d.forceGlyph.Show()
	} else {
		d.forceGlyph.Hide()
	}
	objs = append(objs, d.forceGlyph)
	return objs
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
	r.content.Objects = r.layer.syncMarkerCircles()
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
	r.content.Objects = r.layer.syncMarkerCircles()
	r.layer.updateOverlay()
	canvas.Refresh(r.content)
}
