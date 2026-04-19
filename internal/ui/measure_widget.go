package ui

import (
	"image"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

type measurePoint struct {
	X, Y float64
}

// measurePickerWidget lets the user click two points on an image to measure
// the distance between them. Left-click sets point A then point B. Escape cancels.
type measurePickerWidget struct {
	widget.BaseWidget

	refImage       image.Image
	imageW, imageH int
	zoom           float64

	pointA *measurePoint
	pointB *measurePoint

	OnComplete func(a, b measurePoint)
	OnCancel   func()
	OnStatus   func(msg string)
	// CentroidFn refines a click to the nearest point-source centroid in image pixel space.
	CentroidFn func(x, y float64) (float64, float64, bool)
}

func newMeasurePickerWidget(img image.Image, imageW, imageH int) *measurePickerWidget {
	w := &measurePickerWidget{
		refImage: img,
		imageW:   imageW,
		imageH:   imageH,
	}
	w.ExtendBaseWidget(w)
	return w
}

func (w *measurePickerWidget) SetImage(img image.Image) {
	w.refImage = img
	w.Refresh()
}

func (w *measurePickerWidget) SetZoom(z float64) {
	w.zoom = z
	w.Refresh()
}

func (w *measurePickerWidget) Reset() {
	w.pointA = nil
	w.pointB = nil
	if w.OnStatus != nil {
		w.OnStatus("Click point A on the image.")
	}
	w.Refresh()
}

func (w *measurePickerWidget) Tapped(ev *fyne.PointEvent) {
	imgX, imgY, ok := w.widgetToImage(ev.Position)
	if !ok {
		return
	}
	// Refine click to nearest point-source centroid if available.
	if w.CentroidFn != nil {
		if cx, cy, centOk := w.CentroidFn(imgX, imgY); centOk {
			imgX, imgY = cx, cy
		}
	}
	if w.pointA == nil {
		w.pointA = &measurePoint{X: imgX, Y: imgY}
		if w.OnStatus != nil {
			w.OnStatus("Point A set. Click point B.")
		}
		w.Refresh()
	} else if w.pointB == nil {
		w.pointB = &measurePoint{X: imgX, Y: imgY}
		w.Refresh()
		if w.OnComplete != nil {
			w.OnComplete(*w.pointA, *w.pointB)
		}
	}
}

func (w *measurePickerWidget) TappedSecondary(_ *fyne.PointEvent) {}

// Focusable implementation for Escape key handling.
func (w *measurePickerWidget) FocusGained() {}
func (w *measurePickerWidget) FocusLost()   {}
func (w *measurePickerWidget) TypedRune(_ rune) {}

func (w *measurePickerWidget) TypedKey(key *fyne.KeyEvent) {
	if key.Name == fyne.KeyEscape {
		if w.OnCancel != nil {
			w.OnCancel()
		}
	}
}

// Cursor returns a crosshair cursor while in measure mode.
func (w *measurePickerWidget) Cursor() desktop.Cursor {
	return desktop.CrosshairCursor
}

func (w *measurePickerWidget) MinSize() fyne.Size {
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

func (w *measurePickerWidget) letterboxParams() (offsetX, offsetY, scale float64) {
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

func (w *measurePickerWidget) widgetToImage(pos fyne.Position) (imgX, imgY float64, ok bool) {
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

func (w *measurePickerWidget) CreateRenderer() fyne.WidgetRenderer {
	img := canvas.NewImageFromImage(w.refImage)
	img.FillMode = canvas.ImageFillContain
	return &measurePickerRenderer{w: w, img: img}
}

type measurePickerRenderer struct {
	w       *measurePickerWidget
	img     *canvas.Image
	objects []fyne.CanvasObject
}

func (r *measurePickerRenderer) Layout(size fyne.Size) {
	r.img.Resize(size)
	r.img.Move(fyne.NewPos(0, 0))
	r.updateOverlay()
}

func (r *measurePickerRenderer) MinSize() fyne.Size { return r.w.MinSize() }
func (r *measurePickerRenderer) Destroy()           {}

func (r *measurePickerRenderer) Refresh() {
	r.img.Image = r.w.refImage
	r.updateOverlay()
}

func (r *measurePickerRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *measurePickerRenderer) updateOverlay() {
	offsetX, offsetY, scale := r.w.letterboxParams()
	objs := []fyne.CanvasObject{r.img}

	const arm = float32(12)
	colorA := color.RGBA{R: 255, G: 200, B: 0, A: 230}
	colorB := color.RGBA{R: 0, G: 200, B: 255, A: 230}
	lineColor := color.RGBA{R: 255, G: 255, B: 0, A: 180}

	if r.w.pointA != nil && r.w.pointB != nil {
		axW := float32(r.w.pointA.X*scale+offsetX)
		ayW := float32(r.w.pointA.Y*scale+offsetY)
		bxW := float32(r.w.pointB.X*scale+offsetX)
		byW := float32(r.w.pointB.Y*scale+offsetY)
		line := canvas.NewLine(lineColor)
		line.Position1 = fyne.NewPos(axW, ayW)
		line.Position2 = fyne.NewPos(bxW, byW)
		line.StrokeWidth = 1
		objs = append(objs, line)
	}

	drawCrosshair := func(pt measurePoint, c color.RGBA, label string) {
		wx := float32(pt.X*scale + offsetX)
		wy := float32(pt.Y*scale + offsetY)
		h := canvas.NewLine(c)
		h.Position1 = fyne.NewPos(wx-arm, wy)
		h.Position2 = fyne.NewPos(wx+arm, wy)
		h.StrokeWidth = 2
		v := canvas.NewLine(c)
		v.Position1 = fyne.NewPos(wx, wy-arm)
		v.Position2 = fyne.NewPos(wx, wy+arm)
		v.StrokeWidth = 2
		lbl := canvas.NewText(label, c)
		lbl.TextSize = 12
		lbl.Move(fyne.NewPos(wx+arm+2, wy-7))
		objs = append(objs, h, v, lbl)
	}

	if r.w.pointA != nil {
		drawCrosshair(*r.w.pointA, colorA, "A")
	}
	if r.w.pointB != nil {
		drawCrosshair(*r.w.pointB, colorB, "B")
	}
	r.objects = objs
}
