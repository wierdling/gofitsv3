package ui

import (
	"fmt"
	"image"
	"math"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

type imagePoint struct {
	X int
	Y int
}

type viewport struct {
	image         *canvas.Image
	histogram     *canvas.Raster
	zoomLabel     *widget.Select
	zoomOut       *widget.Button
	zoomIn        *widget.Button
	blackBox      *NumberEntry
	whiteBox      *NumberEntry
	container     fyne.CanvasObject
	zoom          float64
	origW         int
	origH         int
	scroll        *container.Scroll
	overlay       *viewerInteractionLayer
	bins          [256]int
	customZoom    string
	histColor     [4]uint8 // bar color; if zero, use default white-bg/gray-bar style
	StatsLabel    *widget.Label
	actionRow     *fyne.Container
	onViewChanged func()
}

var presetZoomOptions = []string{"fit in preview", "1%", "5%", "10%", "20%", "25%", "50%", "75%", "100%", "200%", "300%"}

func newViewport() *viewport {
	img := canvas.NewImageFromImage(blankImg())
	img.FillMode = canvas.ImageFillContain

	vp := &viewport{image: img, zoom: 1}
	vp.histogram = canvas.NewRaster(vp.drawHist)
	vp.histogram.SetMinSize(fyne.NewSize(200, 48))

	overlay := newViewerInteractionLayer()
	vp.overlay = overlay
	vp.scroll = container.NewScroll(container.NewMax(img, overlay))
	overlay.scroll = vp.scroll
	vp.scroll.SetMinSize(fyne.NewSize(260, 180))

	vp.blackBox = NewNumberEntry(0.001, 4)
	vp.whiteBox = NewNumberEntry(0.001, 4)
	vp.zoomLabel = widget.NewSelect([]string{"fit in preview", "1%", "5%", "10%", "20%", "25%", "50%", "75%", "100%", "200%", "300%"}, func(s string) {
		vp.setZoomFromSelect(s)
	})
	vp.zoomOut = widget.NewButton("-", func() { vp.stepZoom(0.95) })
	vp.zoomIn = widget.NewButton("+", func() { vp.stepZoom(1.05) })

	vp.StatsLabel = widget.NewLabel("Mean: -- | Std: --")
	vp.StatsLabel.TextStyle = fyne.TextStyle{Monospace: true}
	vp.StatsLabel.Alignment = fyne.TextAlignCenter

	vp.actionRow = container.NewHBox(layout.NewSpacer(), vp.StatsLabel, layout.NewSpacer())

	header := container.NewVBox(
		vp.histogram,
		vp.actionRow,
	)
	footer := container.NewHBox(
		layout.NewSpacer(),
		widget.NewLabel("Black"),
		vp.blackBox,
		vp.zoomOut,
		vp.zoomLabel,
		vp.zoomIn,
		widget.NewLabel("White"),
		vp.whiteBox,
		layout.NewSpacer(),
	)
	vp.container = container.NewBorder(header, footer, nil, nil, vp.scroll)

	vp.zoomLabel.SetSelected("fit in preview")

	return vp
}

func (vp *viewport) SetCenterAction(label string, fn func()) {
	btn := widget.NewButton(label, fn)
	vp.actionRow.Objects = []fyne.CanvasObject{layout.NewSpacer(), btn, layout.NewSpacer()}
	vp.actionRow.Refresh()
}

func (vp *viewport) SetLoadSave(loadFn, saveFn func()) {
	loadBtn := widget.NewButton("Load", loadFn)
	saveBtn := widget.NewButton("Save", saveFn)
	vp.actionRow.Objects = []fyne.CanvasObject{
		loadBtn, layout.NewSpacer(), vp.StatsLabel, layout.NewSpacer(), saveBtn,
	}
	vp.actionRow.Refresh()
}

func isPresetZoom(option string) bool {
	for _, o := range presetZoomOptions {
		if o == option {
			return true
		}
	}
	return false
}

func (vp *viewport) setZoomLabelValue(option string) {
	if !isPresetZoom(option) {
		if vp.customZoom != "" {
			var opts []string
			for _, o := range vp.zoomLabel.Options {
				if o != vp.customZoom {
					opts = append(opts, o)
				}
			}
			vp.zoomLabel.Options = opts
		}
		vp.customZoom = option
		vp.zoomLabel.Options = append(vp.zoomLabel.Options, option)
	}
	vp.zoomLabel.SetSelected(option)
}

func (vp *viewport) setZoomFromSelect(sel string) {
	switch sel {
	case "fit in preview":
		vp.zoom = vp.fitZoom()
	default:
		sel = strings.TrimSuffix(sel, "%")
		if val, err := strconv.ParseFloat(sel, 64); err == nil {
			vp.zoom = val / 100.0
		}
	}
	vp.applyZoom()
}

func (vp *viewport) stepZoom(factor float64) {
	if vp.zoomLabel.Selected == "fit in preview" {
		vp.zoom = vp.fitZoom()
	}
	vp.zoom *= factor
	vp.setZoomLabelValue(fmt.Sprintf("%d%%", int(math.Round(vp.zoom*100))))
	vp.applyZoom()
}

func (vp *viewport) applyZoom() {
	if vp == nil || vp.scroll == nil || vp.image == nil {
		return
	}
	if vp.zoom <= 0 {
		vp.zoom = 1
	}
	avail := vp.scroll.Size()
	if avail.Width <= 1 || avail.Height <= 1 {
		avail = fyne.NewSize(300, 300)
	}
	if vp.origW == 0 || vp.origH == 0 {
		vp.image.SetMinSize(avail)
		vp.image.Refresh()
		if vp.overlay != nil {
			vp.overlay.Refresh()
		}
		if vp.onViewChanged != nil {
			vp.onViewChanged()
		}
		return
	}
	w := float32(vp.origW) * float32(vp.zoom)
	h := float32(vp.origH) * float32(vp.zoom)
	vp.image.SetMinSize(fyne.NewSize(w, h))
	vp.image.Refresh()
	if vp.overlay != nil {
		vp.overlay.Refresh()
	}
	if vp.onViewChanged != nil {
		vp.onViewChanged()
	}
}

func (vp *viewport) fitZoom() float64 {
	if vp.origW == 0 || vp.origH == 0 {
		return 1
	}
	sz := vp.scroll.Size()
	if sz.Width <= 1 || sz.Height <= 1 {
		sz = fyne.NewSize(300, 300)
	}
	return math.Min(float64(sz.Width)/float64(vp.origW), float64(sz.Height)/float64(vp.origH))
}

func (vp *viewport) drawHist(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	colored := vp.histColor[3] > 0
	if colored {
		// black background — img.Pix is already zero (transparent), set alpha
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	} else {
		// white background
		for i := range img.Pix {
			img.Pix[i] = 255
		}
	}

	maxCount := 0
	for _, c := range vp.bins {
		if c > maxCount {
			maxCount = c
		}
	}
	if maxCount == 0 {
		return img
	}

	var r, g, b uint8
	if colored {
		r, g, b = vp.histColor[0], vp.histColor[1], vp.histColor[2]
	} else {
		r, g, b = 80, 80, 80
	}

	for i, c := range vp.bins {
		x := i * w / len(vp.bins)
		barH := int(float64(c) / float64(maxCount) * float64(h))
		for y := h - 1; y >= h-barH; y-- {
			idx := y*img.Stride + x*4
			img.Pix[idx] = r
			img.Pix[idx+1] = g
			img.Pix[idx+2] = b
			img.Pix[idx+3] = 255
		}
	}
	return img
}

func blankImg() *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, 10, 10))
}

func (vp *viewport) imagePointAtPosition(pos fyne.Position, flipped bool) (imagePoint, bool) {
	return mapViewportPositionToImage(pos, fyne.NewPos(0, 0), vp.zoom, vp.origW, vp.origH, flipped)
}

func (vp *viewport) setMeasurementOverlay(first *imagePoint, second *imagePoint, flipped bool) {
	if vp == nil || vp.overlay == nil {
		return
	}
	var start *fyne.Position
	var end *fyne.Position
	if first != nil {
		pos := imagePointToCanvasPosition(*first, vp.zoom, vp.origH, flipped)
		start = &pos
	}
	if second != nil {
		pos := imagePointToCanvasPosition(*second, vp.zoom, vp.origH, flipped)
		end = &pos
	}
	vp.overlay.setMeasurement(start, end)
}
