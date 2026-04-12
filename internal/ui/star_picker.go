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
	if len(w.Stars) == 0 {
		return
	}
	imgX, imgY, ok := w.widgetToImage(ev.Position)
	if !ok {
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
	return fyne.NewSize(float32(600*z), float32(500*z))
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
