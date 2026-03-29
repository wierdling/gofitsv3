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

type viewport struct {
	image      *canvas.Image
	histogram  *canvas.Raster
	zoomLabel  *widget.Select
	zoomOut    *widget.Button
	zoomIn     *widget.Button
	blackBox   *widget.Entry
	whiteBox   *widget.Entry
	container  fyne.CanvasObject
	zoom       float64
	origW      int
	origH      int
	scroll     *container.Scroll
	bins       [256]int
	customZoom string
}

var presetZoomOptions = []string{"fit in preview", "1%", "5%", "10%", "20%", "25%", "50%", "75%", "100%", "200%", "300%"}

func newViewport() *viewport {
	img := canvas.NewImageFromImage(blankImg())
	img.FillMode = canvas.ImageFillContain

	vp := &viewport{image: img, zoom: 1}
	vp.histogram = canvas.NewRaster(vp.drawHist)
	vp.histogram.SetMinSize(fyne.NewSize(200, 48))

	drag := newDragLayer(nil, img)
	vp.scroll = container.NewScroll(container.NewMax(img, drag))
	drag.scroll = vp.scroll
	vp.scroll.SetMinSize(fyne.NewSize(260, 180))

	vp.blackBox = widget.NewEntry()
	vp.blackBox.SetPlaceHolder("000000")
	vp.blackBox.SetText("--")
	vp.whiteBox = widget.NewEntry()
	vp.whiteBox.SetPlaceHolder("000000")
	vp.whiteBox.SetText("--")
	vp.zoomLabel = widget.NewSelect([]string{"fit in preview", "1%", "5%", "10%", "20%", "25%", "50%", "75%", "100%", "200%", "300%"}, func(s string) {
		vp.setZoomFromSelect(s)
	})
	vp.zoomOut = widget.NewButton("-", func() { vp.stepZoom(0.95) })
	vp.zoomIn = widget.NewButton("+", func() { vp.stepZoom(1.05) })

	header := container.NewVBox(
		vp.histogram,
		container.NewHBox(
			layout.NewSpacer(),
			widget.NewLabel("Black"),
			container.New(layout.NewGridWrapLayout(fyne.NewSize(110, vp.blackBox.MinSize().Height)), vp.blackBox),
			vp.zoomOut,
			vp.zoomLabel,
			vp.zoomIn,
			widget.NewLabel("White"),
			container.New(layout.NewGridWrapLayout(fyne.NewSize(110, vp.whiteBox.MinSize().Height)), vp.whiteBox),
			layout.NewSpacer(),
		),
	)
	vp.container = container.NewBorder(header, nil, nil, nil, vp.scroll)

	vp.zoomLabel.SetSelected("fit in preview")
	return vp
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
		return
	}
	w := float32(vp.origW) * float32(vp.zoom)
	h := float32(vp.origH) * float32(vp.zoom)
	vp.image.SetMinSize(fyne.NewSize(w, h))
	vp.image.Refresh()
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
	for i := range img.Pix {
		img.Pix[i] = 255
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
	for i, c := range vp.bins {
		x := i * w / len(vp.bins)
		barH := int(float64(c) / float64(maxCount) * float64(h))
		for y := h - 1; y >= h-barH; y-- {
			idx := (y*img.Stride + x*4)
			img.Pix[idx] = 80
			img.Pix[idx+1] = 80
			img.Pix[idx+2] = 80
			img.Pix[idx+3] = 255
		}
	}
	return img
}

func blankImg() *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, 10, 10))
}
