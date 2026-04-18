package ui

import (
	"image"
	"image/color"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// healLayer is a transparent overlay widget placed on top of the canvas image.
// It drives a two-step heal interaction:
//
//	Step 1 (healModeSetSource): first click sets the source sample point.
//	Step 2 (healModeSetDest):   second click/drag traces the destination stroke,
//	                            then fires onHealStroke with all sampled points.
type healLayer struct {
	widget.BaseWidget
	mode        healMode
	srcPos      *fyne.Position
	strokeDsts  []fyne.Position // all sampled positions during the dest stroke
	brushRadius float32
	srcCircle   *canvas.Circle
	dstCircle   *canvas.Circle  // tracks the current drag tip
	trailRaster *canvas.Raster  // redraws the full stroke trail
	background  *canvas.Rectangle
	onHealStroke func(src fyne.Position, dsts []fyne.Position)
	onSourceSet  func()
}

type healMode int

const (
	healModeSetSource healMode = iota
	healModeSetDest
)

func newHealLayer() *healLayer {
	h := &healLayer{
		brushRadius: 20,
		srcCircle:   canvas.NewCircle(color.Transparent),
		dstCircle:   canvas.NewCircle(color.Transparent),
		background:  canvas.NewRectangle(color.Transparent),
	}
	h.srcCircle.StrokeColor = color.NRGBA{R: 255, G: 200, B: 0, A: 220}
	h.srcCircle.StrokeWidth = 2
	h.srcCircle.Hide()
	h.dstCircle.StrokeColor = color.NRGBA{R: 80, G: 200, B: 255, A: 220}
	h.dstCircle.StrokeWidth = 2
	h.dstCircle.Hide()
	h.trailRaster = canvas.NewRaster(h.drawTrail)
	h.trailRaster.Hide()
	h.ExtendBaseWidget(h)
	return h
}

// drawTrail renders all accumulated stroke circles into a raster image.
func (h *healLayer) drawTrail(w, h2 int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h2))
	r := int(math.Round(float64(h.brushRadius)))
	if r < 1 {
		r = 1
	}
	stroke := color.NRGBA{R: 80, G: 200, B: 255, A: 160}
	for _, pos := range h.strokeDsts {
		cx, cy := int(pos.X), int(pos.Y)
		// Draw a circle outline at each sampled position.
		for deg := 0; deg < 360; deg++ {
			rad := float64(deg) * math.Pi / 180
			px := cx + int(math.Round(float64(r)*math.Cos(rad)))
			py := cy + int(math.Round(float64(r)*math.Sin(rad)))
			if px >= 0 && px < w && py >= 0 && py < h2 {
				idx := py*img.Stride + px*4
				img.Pix[idx] = stroke.R
				img.Pix[idx+1] = stroke.G
				img.Pix[idx+2] = stroke.B
				img.Pix[idx+3] = stroke.A
			}
		}
	}
	return img
}

func (h *healLayer) SetBrushRadius(r float32) {
	h.brushRadius = r
	if h.srcPos != nil {
		h.positionCircle(h.srcCircle, *h.srcPos)
	}
	canvas.Refresh(h)
}

func (h *healLayer) Reset() {
	h.mode = healModeSetSource
	h.srcPos = nil
	h.strokeDsts = nil
	h.srcCircle.Hide()
	h.dstCircle.Hide()
	h.trailRaster.Hide()
	canvas.Refresh(h)
}

func (h *healLayer) positionCircle(c *canvas.Circle, pos fyne.Position) {
	r := h.brushRadius
	c.Move(fyne.NewPos(pos.X-r, pos.Y-r))
	c.Resize(fyne.NewSize(r*2, r*2))
	c.Show()
	c.Refresh()
}

// Tapped handles a single click (no drag).
func (h *healLayer) Tapped(e *fyne.PointEvent) {
	if h.mode == healModeSetSource {
		h.beginSource(e.Position)
	} else {
		// Single-click destination — treat as a one-point stroke.
		h.strokeDsts = append(h.strokeDsts[:0], e.Position)
		h.commitStroke()
	}
}

func (h *healLayer) TappedSecondary(*fyne.PointEvent) {}

// Dragged samples the stroke path during the destination drag.
func (h *healLayer) Dragged(e *fyne.DragEvent) {
	if h.mode != healModeSetDest {
		return
	}
	pos := e.Position
	// Only add a sample if we've moved far enough to avoid excessive density.
	minGap := h.brushRadius / 2
	if minGap < 1 {
		minGap = 1
	}
	if len(h.strokeDsts) == 0 {
		h.strokeDsts = append(h.strokeDsts, pos)
		h.trailRaster.Show()
	} else {
		last := h.strokeDsts[len(h.strokeDsts)-1]
		dx := pos.X - last.X
		dy := pos.Y - last.Y
		if dx*dx+dy*dy >= minGap*minGap {
			h.strokeDsts = append(h.strokeDsts, pos)
		}
	}
	h.positionCircle(h.dstCircle, pos)
	h.trailRaster.Refresh()
	canvas.Refresh(h)
}

// DragEnd commits the stroke when the mouse is released.
func (h *healLayer) DragEnd() {
	if h.mode == healModeSetDest && len(h.strokeDsts) > 0 {
		h.commitStroke()
	}
}

func (h *healLayer) beginSource(pos fyne.Position) {
	h.srcPos = &pos
	h.strokeDsts = nil
	h.positionCircle(h.srcCircle, pos)
	h.dstCircle.StrokeColor = color.NRGBA{R: 80, G: 200, B: 255, A: 220}
	h.dstCircle.Hide()
	h.trailRaster.Hide()
	h.mode = healModeSetDest
	canvas.Refresh(h)
	if h.onSourceSet != nil {
		h.onSourceSet()
	}
}

func (h *healLayer) commitStroke() {
	if h.srcPos == nil || len(h.strokeDsts) == 0 {
		h.mode = healModeSetSource
		canvas.Refresh(h)
		return
	}
	dsts := make([]fyne.Position, len(h.strokeDsts))
	copy(dsts, h.strokeDsts)
	src := *h.srcPos

	if h.onHealStroke != nil {
		h.onHealStroke(src, dsts)
	}

	h.srcPos = nil
	h.strokeDsts = nil
	h.srcCircle.Hide()
	h.dstCircle.Hide()
	h.trailRaster.Hide()

	// Show a brief green "done" indicator at the last destination point.
	if len(dsts) > 0 {
		last := dsts[len(dsts)-1]
		h.dstCircle.StrokeColor = color.NRGBA{R: 80, G: 255, B: 120, A: 200}
		h.positionCircle(h.dstCircle, last)
	}

	h.mode = healModeSetSource
	canvas.Refresh(h)
}

func (h *healLayer) CreateRenderer() fyne.WidgetRenderer {
	h.background.Resize(h.Size())
	content := container.NewWithoutLayout(h.background, h.trailRaster, h.srcCircle, h.dstCircle)
	return &healLayerRenderer{layer: h, content: content}
}

func (h *healLayer) MinSize() fyne.Size { return fyne.NewSize(10, 10) }

type healLayerRenderer struct {
	layer   *healLayer
	content *fyne.Container
}

func (r *healLayerRenderer) Layout(size fyne.Size) {
	r.layer.background.Resize(size)
	r.layer.trailRaster.Resize(size)
	r.content.Resize(size)
}
func (r *healLayerRenderer) MinSize() fyne.Size           { return fyne.NewSize(10, 10) }
func (r *healLayerRenderer) Refresh()                     { canvas.Refresh(r.content) }
func (r *healLayerRenderer) Destroy()                     {}
func (r *healLayerRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.content} }

// applyHealStroke applies the heal operation for every destination point in the stroke.
// All source reads come from the original img so circles don't compound each other.
func applyHealStroke(img *image.RGBA, srcCenter image.Point, dstCenters []image.Point, radius int) *image.RGBA {
	bounds := img.Bounds()
	result := image.NewRGBA(bounds)
	copy(result.Pix, img.Pix)

	feather := radius / 4
	if feather < 1 {
		feather = 1
	}
	innerR := float64(radius - feather)

	for _, dstCenter := range dstCenters {
		for dy := -radius; dy <= radius; dy++ {
			for dx := -radius; dx <= radius; dx++ {
				dist := math.Sqrt(float64(dx*dx + dy*dy))
				if dist > float64(radius) {
					continue
				}

				dstX, dstY := dstCenter.X+dx, dstCenter.Y+dy
				srcX, srcY := srcCenter.X+dx, srcCenter.Y+dy

				if !image.Pt(dstX, dstY).In(bounds) || !image.Pt(srcX, srcY).In(bounds) {
					continue
				}

				var w float64
				if dist <= innerR {
					w = 1.0
				} else {
					t := (dist - innerR) / float64(feather)
					w = 0.5 * (1 + math.Cos(t*math.Pi))
				}

				si := img.PixOffset(srcX, srcY)
				di := result.PixOffset(dstX, dstY)
				for c := 0; c < 3; c++ {
					sv := float64(img.Pix[si+c])
					dv := float64(result.Pix[di+c])
					result.Pix[di+c] = uint8(sv*w + dv*(1-w))
				}
				result.Pix[di+3] = img.Pix[si+3]
			}
		}
	}
	return result
}
