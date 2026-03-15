package ui

import (
	"fmt"
	"image"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/export"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/render"
	"gofitsv3/internal/stretch"
)

type loadedImage struct {
	Path     string
	HDU      fitsio.HDU
	Mode     stretch.Mode
	Black    float64
	White    float64
	Scale    float64
	ShowClip bool
}

type viewport struct {
	image     *canvas.Image
	histogram *canvas.Raster
	zoomLabel *widget.Select
	zoomOut   *widget.Button
	zoomIn    *widget.Button
	container fyne.CanvasObject
	zoom      float64
	origW     int
	origH     int
	scroll    *container.Scroll
	bins      [256]int
}

func newViewport() *viewport {
	img := canvas.NewImageFromImage(blankImg())
	img.FillMode = canvas.ImageFillContain

	vp := &viewport{image: img, zoom: 1}
	vp.histogram = canvas.NewRaster(vp.drawHist)
	vp.histogram.SetMinSize(fyne.NewSize(200, 80))

	vp.scroll = container.NewScroll(img)
	vp.scroll.SetMinSize(fyne.NewSize(500, 500))

	vp.zoomLabel = widget.NewSelect([]string{"fit in preview", "1%", "5%", "10%", "20%", "25%", "50%", "75%", "100%", "200%", "300%"}, func(s string) {
		vp.setZoomFromSelect(s)
	})
	vp.zoomOut = widget.NewButton("-", func() { vp.stepZoom(0.8) })
	vp.zoomIn = widget.NewButton("+", func() { vp.stepZoom(1.25) })

	header := container.NewVBox(
		vp.histogram,
		container.NewHBox(layout.NewSpacer(), vp.zoomOut, vp.zoomLabel, vp.zoomIn),
	)
	vp.container = container.NewBorder(header, nil, nil, nil, vp.scroll)

	vp.zoomLabel.SetSelected("fit in preview")
	return vp
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
		vp.zoomLabel.SetSelected("100%")
	}
	vp.zoom *= factor
	vp.zoomLabel.SetSelected(fmt.Sprintf("%d%%", int(math.Round(vp.zoom*100))))
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

func newComposeWorkspace(app fyne.App, win fyne.Window) fyne.CanvasObject {
	imgs := make([]*loadedImage, 3)
	viewports := []*viewport{newViewport(), newViewport(), newViewport(), newViewport()}

	refresh := func() { updatePreviews(imgs, viewports) }

	loadChannel := func(idx int) {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			file, err := fitsio.LoadFile(path)
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			sci := file.SelectSCI()
			hdu := file.HDUs[0]
			if len(sci) == 1 {
				hdu = sci[0]
			} else if len(sci) > 1 {
				hdu = sci[0]
			}
			minV, maxV := autoLevels(hdu.Data.Pixels)
			imgs[idx] = &loadedImage{Path: path, HDU: hdu, Mode: stretch.Linear, Black: minV, White: maxV, Scale: 1}
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			refresh()
		}, win)
		if last := app.Preferences().String("lastDir"); last != "" {
			uri := storage.NewFileURI(last)
			if l, err := storage.ListerForURI(uri); err == nil {
				fd.SetLocation(l)
			}
		}
		fd.Show()
	}

	exportBtn := widget.NewButton("Export RGB", func() {
		buf, w, h := composeRGB(imgs)
		if buf == nil {
			dialog.ShowInformation("Missing", "Load three FITS first", win)
			return
		}
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			format := export.PNG
			if len(path) >= 4 {
				switch path[len(path)-4:] {
				case ".png":
					format = export.PNG
				case ".tif":
					format = export.TIFF
				case "tiff":
					format = export.TIFF
				case ".jpg":
					format = export.JPEG
				case "jpeg":
					format = export.JPEG
				}
			}
			_ = export.FromRGBABytes(path, buf, w, h, format, export.Options{Quality: 92})
		}, win)
		save.SetFileName("composite.png")
		save.Show()
	})

	controls := container.NewVBox(
		widget.NewButton("Load Channel 1", func() { loadChannel(0) }),
		widget.NewButton("Load Channel 2", func() { loadChannel(1) }),
		widget.NewButton("Load Channel 3", func() { loadChannel(2) }),
		widget.NewSeparator(),
		widget.NewLabel("Per-channel controls"),
		channelControls("Channel 1", 0, imgs, refresh),
		channelControls("Channel 2", 1, imgs, refresh),
		channelControls("Channel 3", 2, imgs, refresh),
		exportBtn,
	)

	grid := container.NewGridWithColumns(2,
		viewports[0].container, viewports[1].container,
		viewports[2].container, viewports[3].container,
	)

	split := container.NewHSplit(controls, grid)
	split.SetOffset(0.32)
	return split
}

func channelControls(label string, idx int, imgs []*loadedImage, refresh func()) fyne.CanvasObject {
	selectBox := widget.NewSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq"}, func(value string) {
		if imgs[idx] == nil {
			return
		}
		switch value {
		case "Linear":
			imgs[idx].Mode = stretch.Linear
		case "Log":
			imgs[idx].Mode = stretch.Log
		case "Asinh":
			imgs[idx].Mode = stretch.Asinh
		case "Sqrt":
			imgs[idx].Mode = stretch.Sqrt
		case "HistEq":
			imgs[idx].Mode = stretch.HistEq
		}
		refresh()
	})
	selectBox.SetSelected("Linear")

	blackEntry := widget.NewEntry()
	whiteEntry := widget.NewEntry()
	scaleEntry := widget.NewEntry()
	blackEntry.SetText("0")
	whiteEntry.SetText("1")
	scaleEntry.SetText("1")
	showClip := widget.NewCheck("Show clipped (blue/green/red)", func(v bool) {
		if imgs[idx] == nil {
			return
		}
		imgs[idx].ShowClip = v
		refresh()
	})

	apply := widget.NewButton("Apply values", func() {
		if imgs[idx] == nil {
			return
		}
		if v, err := parseFloat(blackEntry.Text); err == nil {
			imgs[idx].Black = v
		}
		if v, err := parseFloat(whiteEntry.Text); err == nil {
			imgs[idx].White = v
		}
		if v, err := parseFloat(scaleEntry.Text); err == nil {
			imgs[idx].Scale = v
		}
		refresh()
	})

	auto := widget.NewButton("Auto scaling", func() {
		if imgs[idx] == nil {
			return
		}
		minV, maxV := autoLevels(imgs[idx].HDU.Data.Pixels)
		imgs[idx].Black = minV
		imgs[idx].White = maxV
		imgs[idx].Scale = 1
		blackEntry.SetText(fmt.Sprintf("%.2f", minV))
		whiteEntry.SetText(fmt.Sprintf("%.2f", maxV))
		scaleEntry.SetText("1")
		refresh()
	})

	return container.NewVBox(
		widget.NewLabel(label),
		selectBox,
		widget.NewForm(
			widget.NewFormItem("Black level", blackEntry),
			widget.NewFormItem("White level", whiteEntry),
			widget.NewFormItem("Scaled peak", scaleEntry),
		),
		showClip,
		container.NewHBox(auto, apply),
		widget.NewSeparator(),
	)
}

func updatePreviews(imgs []*loadedImage, views []*viewport) {
	for i := 0; i < 3; i++ {
		if imgs[i] == nil {
			views[i].image.Image = blankImg()
			views[i].bins = [256]int{}
			views[i].histogram.Refresh()
			views[i].image.Refresh()
			continue
		}
		stretched, mask := applyStretch(imgs[i])
		views[i].image.Image = toGrayRGBA(stretched, mask)
		views[i].origW, views[i].origH = stretched.Width, stretched.Height
		views[i].bins, _, _ = histogram(stretched.Pixels)
		views[i].histogram.Refresh()
		if views[i].zoomLabel.Selected == "fit in preview" {
			views[i].zoom = views[i].fitZoom()
		}
		views[i].applyZoom()
		views[i].image.Refresh()
	}

	buf, w, h := composeRGB(imgs)
	if buf == nil {
		views[3].image.Image = blankImg()
		views[3].bins = [256]int{}
		views[3].histogram.Refresh()
		views[3].image.Refresh()
		return
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	copy(img.Pix, buf)
	views[3].image.Image = img
	views[3].origW, views[3].origH = w, h
	views[3].bins = [256]int{}
	views[3].histogram.Refresh()
	if views[3].zoomLabel.Selected == "fit in preview" {
		views[3].zoom = views[3].fitZoom()
	}
	views[3].applyZoom()
	views[3].image.Refresh()
}

func composeRGB(imgs []*loadedImage) ([]byte, int, int) {
	if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
		return nil, 0, 0
	}
	w := imgs[0].HDU.Data.Width
	h := imgs[0].HDU.Data.Height
	rData, _ := applyStretch(imgs[0])
	gData, _ := applyStretch(imgs[1])
	bData, _ := applyStretch(imgs[2])
	buf := render.ComposeRGB(rData.Pixels, gData.Pixels, bData.Pixels, w, h, imgs[0].Mode, imgs[1].Mode, imgs[2].Mode)
	return buf, w, h
}

func applyStretch(img *loadedImage) (fitsio.ImageData, []byte) {
	norm := img.HDU.Data.Normalize()
	pixels := make([]float64, len(norm.Pixels))
	mask := make([]byte, len(norm.Pixels)) // 1=black,2=white,3=nan
	for i, v := range norm.Pixels {
		if math.IsNaN(v) {
			mask[i] = 3
			v = 0
		}
		if v < img.Black {
			v = img.Black
			mask[i] = 1
		}
		if v > img.White {
			v = img.White
			mask[i] = 2
		}
		if img.White != img.Black {
			v = (v - img.Black) / (img.White - img.Black)
		}
		pixels[i] = clamp01(v)
	}
	stretched := stretch.Apply(pixels, img.Mode)
	for i, v := range stretched {
		stretched[i] = clamp01(v * img.Scale)
	}
	return fitsio.ImageData{Width: norm.Width, Height: norm.Height, Pixels: stretched}, mask
}

func toGrayRGBA(data fitsio.ImageData, mask []byte) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, data.Width, data.Height))
	for i, v := range data.Pixels {
		idx := i * 4
		switch mask[i] {
		case 1:
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = 0, 0, 255
		case 2:
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = 0, 255, 0
		case 3:
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = 255, 0, 0
		default:
			b := byte(clamp01(v) * 255)
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = b, b, b
		}
		img.Pix[idx+3] = 255
	}
	return img
}

func histogram(pixels []float64) ([256]int, float64, float64) {
	var bins [256]int
	if len(pixels) == 0 {
		return bins, 0, 0
	}
	min, max := pixels[0], pixels[0]
	for _, v := range pixels {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		bin := int(clamp01(v) * 255)
		bins[bin]++
	}
	return bins, min, max
}

func blankImg() *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, 10, 10))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func autoLevels(pixels []float64) (float64, float64) {
	if len(pixels) == 0 {
		return 0, 1
	}
	min, max := pixels[0], pixels[0]
	for _, v := range pixels {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	if min == max {
		max = min + 1
	}
	return min, max
}
