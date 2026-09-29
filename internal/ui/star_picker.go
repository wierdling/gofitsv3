package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/processing"
)

// starPickerWidget displays a reference image and lets the user tap to select
// up to MaxStars positions. Left-click adds a star; right-click removes the nearest.
type starPickerWidget struct {
	widget.BaseWidget

	refImage image.Image // normalized/stretched reference image for display
	imageW   int         // original image pixel width
	imageH   int         // original image pixel height
	zoom     float64     // display zoom factor (1.0 = default min-size)

	Stars      []processing.Star
	MaxStars   int
	OnChanged  func()
	CentroidFn func(x, y float64) (float64, float64, bool) // optional; refines click to star centroid

	// ExistingStars, when set, are drawn as red circles (e.g. stars already
	// present in a saved star map) so they are visually distinct from Stars
	// and are not re-picked. Unused by callers that don't set it.
	ExistingStars []processing.StarMapSource

	// OnExistingTapped, when set, is called with the index into ExistingStars
	// nearest a right-click, letting the caller show that star's info (and
	// offer to remove it). When set, right-click prefers an existing star over
	// a pending one.
	OnExistingTapped func(index int)

	// BoxSelectMode switches left-click-drag to rectangle selection instead of
	// picking a single star; OnBoxSelect receives the dragged rectangle in
	// image pixel coordinates (x0<=x1, y0<=y1) once the drag ends.
	BoxSelectMode bool
	OnBoxSelect   func(x0, y0, x1, y1 float64)

	dragging    bool
	dragStart   fyne.Position
	dragCurrent fyne.Position
}

func newStarPickerWidget(img image.Image, imageW, imageH int) *starPickerWidget {
	w := &starPickerWidget{
		refImage: img,
		imageW:   imageW,
		imageH:   imageH,
		MaxStars: 50,
	}
	w.ExtendBaseWidget(w)
	return w
}

// SetImage replaces the displayed reference image without changing stars.
func (w *starPickerWidget) SetImage(img image.Image) {
	w.refImage = img
	w.Refresh()
}

// SetZoom updates the display zoom factor and triggers a resize/refresh.
func (w *starPickerWidget) SetZoom(z float64) {
	w.zoom = z
	w.Refresh()
}

func (w *starPickerWidget) Tapped(ev *fyne.PointEvent) {
	if w.BoxSelectMode {
		return
	}
	if len(w.Stars) >= w.MaxStars {
		return
	}
	imgX, imgY, ok := w.widgetToImage(ev.Position)
	if !ok {
		return
	}
	if w.CentroidFn != nil {
		if cx, cy, centOk := w.CentroidFn(imgX, imgY); centOk {
			imgX, imgY = cx, cy
		}
	}
	w.Stars = append(w.Stars, processing.Star{X: imgX, Y: imgY})
	if w.OnChanged != nil {
		w.OnChanged()
	}
	w.Refresh()
}

func (w *starPickerWidget) TappedSecondary(ev *fyne.PointEvent) {
	imgX, imgY, ok := w.widgetToImage(ev.Position)
	if !ok {
		return
	}
	if len(w.Stars) == 0 && w.OnExistingTapped != nil && len(w.ExistingStars) > 0 {
		minDist := math.MaxFloat64
		minIdx := -1
		for i, s := range w.ExistingStars {
			dx := s.X - imgX
			dy := s.Y - imgY
			d := dx*dx + dy*dy
			if d < minDist {
				minDist = d
				minIdx = i
			}
		}
		radius := 10.0
		if minIdx >= 0 && w.ExistingStars[minIdx].Radius > radius {
			radius = w.ExistingStars[minIdx].Radius * 2
		}
		if minIdx >= 0 && minDist <= radius*radius {
			w.OnExistingTapped(minIdx)
		}
		return
	}
	if len(w.Stars) == 0 {
		return
	}
	minDist := math.MaxFloat64
	minIdx := -1
	for i, s := range w.Stars {
		dx := s.X - imgX
		dy := s.Y - imgY
		d := dx*dx + dy*dy
		if d < minDist {
			minDist = d
			minIdx = i
		}
	}
	if minIdx >= 0 {
		w.Stars = append(w.Stars[:minIdx], w.Stars[minIdx+1:]...)
		if w.OnChanged != nil {
			w.OnChanged()
		}
		w.Refresh()
	}
}

// Dragged implements fyne.Draggable. Only active in BoxSelectMode, where a
// left-click drag defines a rectangle selection instead of moving anything.
func (w *starPickerWidget) Dragged(ev *fyne.DragEvent) {
	if !w.BoxSelectMode {
		return
	}
	if !w.dragging {
		w.dragging = true
		w.dragStart = fyne.NewPos(ev.Position.X-ev.Dragged.DX, ev.Position.Y-ev.Dragged.DY)
	}
	w.dragCurrent = ev.Position
	w.Refresh()
}

// DragEnd implements fyne.Draggable, finalizing a box selection.
func (w *starPickerWidget) DragEnd() {
	if !w.BoxSelectMode || !w.dragging {
		w.dragging = false
		return
	}
	w.dragging = false
	x0, y0 := w.widgetToImageClamped(w.dragStart)
	x1, y1 := w.widgetToImageClamped(w.dragCurrent)
	w.Refresh()
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	if w.OnBoxSelect != nil && x1 > x0 && y1 > y0 {
		w.OnBoxSelect(x0, y0, x1, y1)
	}
}

// widgetToImageClamped is like widgetToImage but clamps to the image bounds
// instead of rejecting points outside the rendered image area, for drag
// gestures that can end past the image edge.
func (w *starPickerWidget) widgetToImageClamped(pos fyne.Position) (imgX, imgY float64) {
	offsetX, offsetY, scale := w.letterboxParams()
	if scale == 0 {
		return 0, 0
	}
	imgX = (float64(pos.X) - offsetX) / scale
	imgY = (float64(pos.Y) - offsetY) / scale
	if imgX < 0 {
		imgX = 0
	}
	if imgX > float64(w.imageW-1) {
		imgX = float64(w.imageW - 1)
	}
	if imgY < 0 {
		imgY = 0
	}
	if imgY > float64(w.imageH-1) {
		imgY = float64(w.imageH - 1)
	}
	return
}

func (w *starPickerWidget) ClearStars() {
	w.Stars = nil
	if w.OnChanged != nil {
		w.OnChanged()
	}
	w.Refresh()
}

func (w *starPickerWidget) MinSize() fyne.Size {
	z := w.zoom
	if z <= 0 {
		z = 1
	}
	ww := float32(w.imageW)
	hh := float32(w.imageH)
	if ww <= 0 {
		ww = 600
	}
	if hh <= 0 {
		hh = 500
	}
	return fyne.NewSize(ww*float32(z), hh*float32(z))
}

// letterboxParams returns the x/y offset (in widget points) and uniform scale
// that ImageFillContain uses to render the image within the widget bounds.
func (w *starPickerWidget) letterboxParams() (offsetX, offsetY, scale float64) {
	sz := w.Size()
	wW := float64(sz.Width)
	wH := float64(sz.Height)
	if wW <= 0 || wH <= 0 || w.imageW <= 0 || w.imageH <= 0 {
		return 0, 0, 1
	}
	if wW/wH > float64(w.imageW)/float64(w.imageH) {
		scale = wH / float64(w.imageH)
		offsetX = (wW - float64(w.imageW)*scale) / 2
	} else {
		scale = wW / float64(w.imageW)
		offsetY = (wH - float64(w.imageH)*scale) / 2
	}
	return
}

// widgetToImage converts a widget-local point to image pixel coordinates.
// Returns ok=false if the point is outside the rendered image area.
func (w *starPickerWidget) widgetToImage(pos fyne.Position) (imgX, imgY float64, ok bool) {
	offsetX, offsetY, scale := w.letterboxParams()
	if scale == 0 {
		return 0, 0, false
	}
	imgX = (float64(pos.X) - offsetX) / scale
	imgY = (float64(pos.Y) - offsetY) / scale
	if imgX < 0 || imgX >= float64(w.imageW) || imgY < 0 || imgY >= float64(w.imageH) {
		return 0, 0, false
	}
	return imgX, imgY, true
}

func (w *starPickerWidget) CreateRenderer() fyne.WidgetRenderer {
	img := canvas.NewImageFromImage(w.refImage)
	img.FillMode = canvas.ImageFillContain
	r := &starPickerRenderer{w: w, img: img}
	r.updateOverlay()
	return r
}

// starPickerRenderer draws the reference image with crosshair overlays for each selected star.
type starPickerRenderer struct {
	w       *starPickerWidget
	img     *canvas.Image
	objects []fyne.CanvasObject
}

func (r *starPickerRenderer) Layout(size fyne.Size) {
	r.img.Resize(size)
	r.img.Move(fyne.NewPos(0, 0))
	r.updateOverlay()
}

func (r *starPickerRenderer) MinSize() fyne.Size {
	return r.w.MinSize()
}

func (r *starPickerRenderer) Refresh() {
	r.img.Image = r.w.refImage
	r.updateOverlay()
}

func (r *starPickerRenderer) Destroy() {}

func (r *starPickerRenderer) Objects() []fyne.CanvasObject {
	return r.objects
}

func (r *starPickerRenderer) updateOverlay() {
	offsetX, offsetY, scale := r.w.letterboxParams()
	objs := []fyne.CanvasObject{r.img}

	existingColor := color.RGBA{R: 230, G: 30, B: 30, A: 230}
	for _, s := range r.w.ExistingStars {
		radius := s.Radius
		if radius <= 0 {
			radius = 5
		}
		wx := float32(s.X*scale + offsetX)
		wy := float32(s.Y*scale + offsetY)
		wr := float32(radius * scale)

		c := canvas.NewCircle(color.Transparent)
		c.StrokeColor = existingColor
		c.StrokeWidth = 2
		c.Resize(fyne.NewSize(wr*2, wr*2))
		c.Move(fyne.NewPos(wx-wr, wy-wr))
		objs = append(objs, c)
	}

	if r.w.dragging {
		x0, y0 := r.w.dragStart.X, r.w.dragStart.Y
		x1, y1 := r.w.dragCurrent.X, r.w.dragCurrent.Y
		if x0 > x1 {
			x0, x1 = x1, x0
		}
		if y0 > y1 {
			y0, y1 = y1, y0
		}
		box := canvas.NewRectangle(color.NRGBA{R: 80, G: 160, B: 255, A: 50})
		box.StrokeColor = color.RGBA{R: 80, G: 160, B: 255, A: 230}
		box.StrokeWidth = 2
		box.Resize(fyne.NewSize(x1-x0, y1-y0))
		box.Move(fyne.NewPos(x0, y0))
		objs = append(objs, box)
	}

	const arm = float32(9)
	markerColor := color.RGBA{R: 255, G: 100, B: 0, A: 230}

	for i, s := range r.w.Stars {
		wx := float32(s.X*scale + offsetX)
		wy := float32(s.Y*scale + offsetY)

		h := canvas.NewLine(markerColor)
		h.Position1 = fyne.NewPos(wx-arm, wy)
		h.Position2 = fyne.NewPos(wx+arm, wy)
		h.StrokeWidth = 2

		v := canvas.NewLine(markerColor)
		v.Position1 = fyne.NewPos(wx, wy-arm)
		v.Position2 = fyne.NewPos(wx, wy+arm)
		v.StrokeWidth = 2

		lbl := canvas.NewText(fmt.Sprintf("%d", i+1), markerColor)
		lbl.TextSize = 11
		lbl.Move(fyne.NewPos(wx+arm+2, wy-6))

		objs = append(objs, h, v, lbl)
	}
	r.objects = objs
}
