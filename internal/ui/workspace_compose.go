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
	Path       string
	HDU        fitsio.HDU
	Mode       stretch.Mode
	Black      float64
	White      float64
	Background float64
	Peak       float64
	ScaledPeak float64
	ShowClip   bool
}

type viewport struct {
	image     *canvas.Image
	histogram *canvas.Raster
	zoomLabel *widget.Select
	zoomOut   *widget.Button
	zoomIn    *widget.Button
	blackBox  *widget.Entry
	whiteBox  *widget.Entry
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
		// start from the current fitted zoom so increments are relative to the initial fit
		vp.zoom = vp.fitZoom()
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

	flipCheck := widget.NewCheck("Flip image vertically", func(bool) {})
	flipCheck.SetChecked(true)

	refresh := func() { updatePreviews(imgs, viewports, flipCheck.Checked) }

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
			imgs[idx] = &loadedImage{Path: path, HDU: hdu, Mode: stretch.Linear, Black: minV, White: maxV, Background: minV, Peak: maxV, ScaledPeak: maxV, ShowClip: true}
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			refresh()
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		if last := app.Preferences().String("lastDir"); last != "" {
			uri := storage.NewFileURI(last)
			if l, err := storage.ListerForURI(uri); err == nil {
				fd.SetLocation(l)
			}
		}
		fd.Show()
	}

	fileMenu := fyne.NewMenu("File",
		fyne.NewMenuItem("Load Channel 1", func() { loadChannel(0) }),
		fyne.NewMenuItem("Load Channel 2", func() { loadChannel(1) }),
		fyne.NewMenuItem("Load Channel 3", func() { loadChannel(2) }),
	)
	win.SetMainMenu(fyne.NewMainMenu(fileMenu))

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
		widget.NewLabel("Options"),
		flipCheck,
		widget.NewSeparator(),
		widget.NewLabel("Per-channel controls"),
		channelControls("Channel 1", 0, imgs, viewports, refresh, flipCheck),
		channelControls("Channel 2", 1, imgs, viewports, refresh, flipCheck),
		channelControls("Channel 3", 2, imgs, viewports, refresh, flipCheck),
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

func channelControls(label string, idx int, imgs []*loadedImage, views []*viewport, refresh func(), flipCheck *widget.Check) fyne.CanvasObject {
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

	backgroundEntry := widget.NewEntry()
	peakEntry := widget.NewEntry()
	scaledPeakEntry := widget.NewEntry()

	backgroundEntry.SetText("0")
	peakEntry.SetText("1")
	scaledPeakEntry.SetText("1")

	showClip := widget.NewCheck("Show clipped (blue/green/red)", func(v bool) {
		if imgs[idx] == nil {
			return
		}
		imgs[idx].ShowClip = v
		refresh()
	})
	showClip.SetChecked(true)

	apply := widget.NewButton("Apply values", func() {
		if imgs[idx] == nil {
			return
		}
		if v, err := parseFloat(backgroundEntry.Text); err == nil {
			imgs[idx].Background = v
		}
		if v, err := parseFloat(peakEntry.Text); err == nil {
			imgs[idx].Peak = v
		}
		if v, err := parseFloat(scaledPeakEntry.Text); err == nil {
			imgs[idx].ScaledPeak = v
		}
		if v, err := parseFloat(views[idx].blackBox.Text); err == nil {
			imgs[idx].Black = v
		}
		if v, err := parseFloat(views[idx].whiteBox.Text); err == nil {
			imgs[idx].White = v
		}
		refresh()
	})

	auto := widget.NewButton("Auto scaling", func() {
		if imgs[idx] == nil {
			return
		}
		blackVal := imgs[idx].Black
		if v, err := parseFloat(views[idx].blackBox.Text); err == nil {
			blackVal = v
		}
		whiteVal := imgs[idx].White
		if v, err := parseFloat(views[idx].whiteBox.Text); err == nil {
			whiteVal = v
		} else {
			_, whiteVal = autoLevels(imgs[idx].HDU.Data.Pixels)
		}
		imgs[idx].Background = blackVal
		imgs[idx].Peak = whiteVal
		imgs[idx].ScaledPeak = 10
		imgs[idx].White = whiteVal
		imgs[idx].Black = 0
		views[idx].blackBox.SetText("0")
		views[idx].whiteBox.SetText(fmt.Sprintf("%.2f", whiteVal))
		backgroundEntry.SetText(fmt.Sprintf("%.2f", blackVal))
		peakEntry.SetText(fmt.Sprintf("%.2f", whiteVal))
		scaledPeakEntry.SetText("10")
		refresh()
	})

	return container.NewVBox(
		widget.NewLabel(label),
		selectBox,
		widget.NewForm(
			widget.NewFormItem("Background level", backgroundEntry),
			widget.NewFormItem("Peak level", peakEntry),
			widget.NewFormItem("Scaled peak level", scaledPeakEntry),
		),
		showClip,
		container.NewHBox(auto, apply),
		widget.NewSeparator(),
	)
}

func updatePreviews(imgs []*loadedImage, views []*viewport, flip bool) {
	for i := 0; i < 3; i++ {
		if imgs[i] == nil {
			views[i].image.Image = blankImg()
			views[i].bins = [256]int{}
			views[i].blackBox.SetText("--")
			views[i].whiteBox.SetText("--")
			views[i].histogram.Refresh()
			views[i].image.Refresh()
			continue
		}
		stretched, mask := applyStretch(imgs[i])
		if flip {
			stretched = flipImageData(stretched)
			mask = flipMask(mask, stretched.Width, stretched.Height)
		}
		views[i].image.Image = toGrayRGBA(stretched, mask)
		views[i].origW, views[i].origH = stretched.Width, stretched.Height
		views[i].bins, _, _ = histogram(stretched.Pixels)
		views[i].blackBox.SetText(fmt.Sprintf("%.3f", imgs[i].Black))
		views[i].whiteBox.SetText(fmt.Sprintf("%.3f", imgs[i].White))
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
		views[3].blackBox.SetText("--")
		views[3].whiteBox.SetText("--")
		views[3].histogram.Refresh()
		views[3].image.Refresh()
		return
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if flip {
		buf = flipRGBA(buf, w, h)
	}
	copy(img.Pix, buf)
	views[3].image.Image = img
	views[3].origW, views[3].origH = w, h
	views[3].bins = [256]int{}
	views[3].blackBox.SetText("--")
	views[3].whiteBox.SetText("--")
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
	data := img.HDU.Data // raw pixels
	pixels := make([]float64, len(data.Pixels))
	mask := make([]byte, len(data.Pixels)) // 1=black,2=white,3=nan

	denom := img.Peak - img.Background
	if denom == 0 {
		denom = 1
	}
	if img.ScaledPeak <= 0 {
		img.ScaledPeak = 1
	}
	stretchMul := img.ScaledPeak / denom

	for i, v := range data.Pixels {
		if math.IsNaN(v) {
			if img.ShowClip {
				mask[i] = 3
			}
			pixels[i] = 0
			continue
		}
		if v < img.Black {
			if img.ShowClip {
				mask[i] = 1
			}
			v = img.Black
		}
		if v > img.White {
			if img.ShowClip {
				mask[i] = 2
			}
			v = img.White
		}

		val := (v - img.Background) * stretchMul
		if val < 0 {
			val = 0
		}
		switch img.Mode {
		case stretch.Log:
			val = math.Log1p(val) / math.Log1p(img.ScaledPeak)
		case stretch.Asinh:
			val = math.Asinh(val) / math.Asinh(img.ScaledPeak)
		case stretch.Sqrt:
			val = math.Sqrt(val) / math.Sqrt(img.ScaledPeak)
		case stretch.HistEq:
			val = clamp01(val / img.ScaledPeak)
		case stretch.Linear:
			val = val / img.ScaledPeak
		}

		pixels[i] = clamp01(val)
	}

	if img.Mode == stretch.HistEq {
		stretched := stretch.Apply(pixels, img.Mode)
		return fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: stretched}, mask
	}

	return fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: pixels}, mask
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

func flipImageData(data fitsio.ImageData) fitsio.ImageData {
	w, h := data.Width, data.Height
	out := make([]float64, len(data.Pixels))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			srcIdx := (h-1-y)*w + x
			dstIdx := y*w + x
			out[dstIdx] = data.Pixels[srcIdx]
		}
	}
	return fitsio.ImageData{Width: w, Height: h, Pixels: out}
}

func flipMask(mask []byte, w, h int) []byte {
	out := make([]byte, len(mask))
	for y := 0; y < h; y++ {
		copy(out[y*w:(y+1)*w], mask[(h-1-y)*w:(h-y)*w])
	}
	return out
}

func flipRGBA(buf []byte, w, h int) []byte {
	row := w * 4
	out := make([]byte, len(buf))
	for y := 0; y < h; y++ {
		copy(out[y*row:(y+1)*row], buf[(h-1-y)*row:(h-y)*row])
	}
	return out
}
