package ui

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/export"
	"gofitsv3/internal/processing"
)

// globalExportToEdit is set by app.go and called by the compose workspace.
var globalExportToEdit func(img image.Image)

var editZoomPresets = []string{"fit", "10%", "25%", "50%", "75%", "100%", "150%", "200%", "300%", "400%"}

// editWorkspaceState holds all mutable state for the edit tab.
type editWorkspaceState struct {
	win     fyne.Window
	source  *image.RGBA
	origW   int
	origH   int
	zoom    float64

	canvasImg *canvas.Image
	imgScroll *container.Scroll

	// per-channel histograms (drawn with min/max marker lines)
	rgbHists  [3]*canvas.Raster
	rgbBins   [3][256]int

	// zoom controls
	zoomSelect *widget.Select
	customZoom string

	// level sliders (min/max per channel, 0-255)
	rMinSlider *widget.Slider
	rMaxSlider *widget.Slider
	gMinSlider *widget.Slider
	gMaxSlider *widget.Slider
	bMinSlider *widget.Slider
	bMaxSlider *widget.Slider

	// curves widget and channel selector
	curves        *curvesWidget
	curvesChannel *widget.Select

	// sharpening sliders
	sharpSlider       *widget.Slider // strength 0-3
	sharpRadiusSlider *widget.Slider // radius 0.5-10

	loadedName string // base filename of the last loaded image

	// heal tool
	healOverlay    *healLayer
	healActive     bool
	healBrushSlider *widget.Slider
	healStatusLabel *widget.Label
	healUndo       *image.RGBA // single-level undo buffer
}

func (es *editWorkspaceState) applyEdits() {
	if es.source == nil {
		return
	}
	prog := dialog.NewCustom("Applying", "Please wait…", widget.NewProgressBarInfinite(), es.win)
	prog.Show()

	// Snapshot LUTs and slider values on the UI goroutine before handing off.
	rMin, rMax := es.rMinSlider.Value, es.rMaxSlider.Value
	gMin, gMax := es.gMinSlider.Value, es.gMaxSlider.Value
	bMin, bMax := es.bMinSlider.Value, es.bMaxSlider.Value
	rLUT, gLUT, bLUT := es.curves.ToLUT(0), es.curves.ToLUT(1), es.curves.ToLUT(2)
	strength, radius := es.sharpSlider.Value, es.sharpRadiusSlider.Value

	go func() {
		img := processing.ApplyEditLevels(es.source, rMin, rMax, gMin, gMax, bMin, bMax)
		img = processing.ApplyCurvesRGBA(img, rLUT, gLUT, bLUT)
		img = processing.SharpenRGBA(img, strength, radius)

		fyne.Do(func() {
			prog.Hide()
			es.canvasImg.Image = img
			es.canvasImg.Refresh()
		})
	}()
}

// updateHealBrushScreenRadius syncs the overlay's screen-space circle to the current brush size and zoom.
// The slider value is the brush diameter in image pixels, so screen radius = (diameter/2) * zoom.
func (es *editWorkspaceState) updateHealBrushScreenRadius() {
	if es.healOverlay == nil || es.healBrushSlider == nil {
		return
	}
	screenRadius := float32((es.healBrushSlider.Value / 2.0) * es.zoom)
	if screenRadius < 1 {
		screenRadius = 1
	}
	es.healOverlay.SetBrushRadius(screenRadius)
}

// screenToImagePt converts an overlay widget position to an image pixel coordinate.
func (es *editWorkspaceState) screenToImagePt(pos fyne.Position) (image.Point, bool) {
	if es.zoom <= 0 || es.origW == 0 || es.origH == 0 {
		return image.Point{}, false
	}
	pt, ok := mapViewportPositionToImage(pos, fyne.NewPos(0, 0), es.zoom, es.origW, es.origH, false)
	return image.Pt(pt.X, pt.Y), ok
}

// toRGBA returns the image as *image.RGBA, converting if necessary.
func toRGBA(img image.Image) *image.RGBA {
	if img == nil {
		return nil
	}
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	r := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r.Set(x, y, img.At(x, y))
		}
	}
	return r
}

// doHealStroke performs the heal for the full destination stroke.
func (es *editWorkspaceState) doHealStroke(srcScreen fyne.Position, dstScreens []fyne.Position) {
	rgba := toRGBA(es.canvasImg.Image)
	if rgba == nil {
		return
	}
	srcPt, srcOK := es.screenToImagePt(srcScreen)
	if !srcOK {
		return
	}
	dstPts := make([]image.Point, 0, len(dstScreens))
	for _, ds := range dstScreens {
		pt, ok := es.screenToImagePt(ds)
		if ok {
			dstPts = append(dstPts, pt)
		}
	}
	if len(dstPts) == 0 {
		return
	}
	radius := int(math.Round(es.healBrushSlider.Value / 2.0))
	if radius < 1 {
		radius = 1
	}

	// Save undo snapshot before modifying.
	undo := image.NewRGBA(rgba.Bounds())
	copy(undo.Pix, rgba.Pix)
	es.healUndo = undo

	healed := applyHealStroke(rgba, srcPt, dstPts, radius)
	es.canvasImg.Image = healed
	es.canvasImg.Refresh()
}

// undoHeal rolls back the last heal operation.
func (es *editWorkspaceState) undoHeal() {
	if es.healUndo == nil {
		return
	}
	es.canvasImg.Image = es.healUndo
	es.healUndo = nil
	es.canvasImg.Refresh()
}

func (es *editWorkspaceState) refreshHistograms(img *image.RGBA) {
	for c := 0; c < 3; c++ {
		es.rgbBins[c] = [256]int{}
	}
	if img != nil {
		pix := img.Pix
		for i := 0; i+3 < len(pix); i += 4 {
			es.rgbBins[0][pix[i]]++
			es.rgbBins[1][pix[i+1]]++
			es.rgbBins[2][pix[i+2]]++
		}
	}
	for _, h := range es.rgbHists {
		if h != nil {
			h.Refresh()
		}
	}
}

func (es *editWorkspaceState) setImage(img image.Image) {
	if img == nil {
		return
	}
	rgba, ok := img.(*image.RGBA)
	if !ok {
		bounds := img.Bounds()
		rgba = image.NewRGBA(bounds)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				rgba.Set(x, y, img.At(x, y))
			}
		}
	}
	es.source = rgba
	es.origW = rgba.Bounds().Dx()
	es.origH = rgba.Bounds().Dy()

	es.rMinSlider.SetValue(0)
	es.rMaxSlider.SetValue(255)
	es.gMinSlider.SetValue(0)
	es.gMaxSlider.SetValue(255)
	es.bMinSlider.SetValue(0)
	es.bMaxSlider.SetValue(255)
	es.curves.Reset()
	es.curvesChannel.SetSelected("All")
	es.sharpSlider.SetValue(0)
	es.sharpRadiusSlider.SetValue(1.0)

	es.refreshHistograms(rgba)
	es.canvasImg.Image = rgba

	es.zoom = 1.0
	if es.imgScroll != nil {
		sz := es.imgScroll.Size()
		if sz.Width > 1 && sz.Height > 1 && es.origW > 0 && es.origH > 0 {
			zw := float64(sz.Width) / float64(es.origW)
			zh := float64(sz.Height) / float64(es.origH)
			es.zoom = math.Min(zw, zh)
		}
	}
	es.setZoomSelectLabel("fit")
	es.applyZoom()
	es.canvasImg.Refresh()
}

func (es *editWorkspaceState) applyZoom() {
	if es.origW == 0 || es.zoom <= 0 {
		return
	}
	w := float32(es.origW) * float32(es.zoom)
	h := float32(es.origH) * float32(es.zoom)
	es.canvasImg.SetMinSize(fyne.NewSize(w, h))
	es.canvasImg.Refresh()
	es.updateHealBrushScreenRadius()
}

func (es *editWorkspaceState) stepZoom(factor float64) {
	if es.zoomSelect != nil && es.zoomSelect.Selected == "fit" {
		es.zoom = es.fitZoom()
	}
	es.zoom *= factor
	if es.zoom < 0.01 {
		es.zoom = 0.01
	}
	if es.zoom > 32 {
		es.zoom = 32
	}
	label := fmt.Sprintf("%d%%", int(math.Round(es.zoom*100)))
	es.setZoomSelectLabel(label)
	es.applyZoom()
}

func (es *editWorkspaceState) fitZoom() float64 {
	if es.origW == 0 || es.origH == 0 || es.imgScroll == nil {
		return 1
	}
	sz := es.imgScroll.Size()
	if sz.Width <= 1 || sz.Height <= 1 {
		return 1
	}
	return math.Min(float64(sz.Width)/float64(es.origW), float64(sz.Height)/float64(es.origH))
}

func (es *editWorkspaceState) setZoomFromSelect(sel string) {
	switch sel {
	case "fit":
		es.zoom = es.fitZoom()
	default:
		trimmed := strings.TrimSuffix(sel, "%")
		if val, err := strconv.ParseFloat(trimmed, 64); err == nil {
			es.zoom = val / 100.0
		}
	}
	es.applyZoom()
}

func (es *editWorkspaceState) setZoomSelectLabel(label string) {
	if es.zoomSelect == nil {
		return
	}
	isPreset := false
	for _, p := range editZoomPresets {
		if p == label {
			isPreset = true
			break
		}
	}
	if !isPreset {
		opts := make([]string, 0, len(editZoomPresets)+1)
		for _, o := range es.zoomSelect.Options {
			if o != es.customZoom {
				opts = append(opts, o)
			}
		}
		es.customZoom = label
		opts = append(opts, label)
		es.zoomSelect.Options = opts
	}
	es.zoomSelect.SetSelected(label)
}


// sliderRow creates a labelled slider with a live value readout.
// Optional extra callbacks are called in addition to the value label update.
func sliderRow(label string, s *widget.Slider, extra ...func(float64)) fyne.CanvasObject {
	valLabel := widget.NewLabel(fmt.Sprintf("%.0f", s.Value))
	s.OnChanged = func(v float64) {
		valLabel.SetText(fmt.Sprintf("%.2f", v))
		for _, fn := range extra {
			if fn != nil {
				fn(v)
			}
		}
	}
	return container.NewBorder(nil, nil,
		container.New(layout.NewGridWrapLayout(fyne.NewSize(90, 28)), widget.NewLabel(label)),
		container.New(layout.NewGridWrapLayout(fyne.NewSize(48, 28)), valLabel),
		s,
	)
}

// editHistRaster builds a canvas.Raster that draws the histogram for channel ch
// plus vertical marker lines at the current min and max slider positions.
func editHistRaster(es *editWorkspaceState, ch int, col [3]uint8, getMin, getMax func() float64) *canvas.Raster {
	r := canvas.NewRaster(func(w, h int) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, w, h))

		// Background
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i] = 28
			img.Pix[i+1] = 28
			img.Pix[i+2] = 28
			img.Pix[i+3] = 255
		}

		// Histogram bars
		maxCount := 0
		for _, c := range es.rgbBins[ch] {
			if c > maxCount {
				maxCount = c
			}
		}
		if maxCount > 0 {
			for i, c := range es.rgbBins[ch] {
				x := i * w / 256
				barH := int(float64(c) / float64(maxCount) * float64(h))
				for y := h - 1; y >= h-barH; y-- {
					idx := y*img.Stride + x*4
					img.Pix[idx] = col[0]
					img.Pix[idx+1] = col[1]
					img.Pix[idx+2] = col[2]
					img.Pix[idx+3] = 255
				}
			}
		}

		// Min marker (white vertical line)
		minX := int(getMin() / 255.0 * float64(w-1))
		for y := 0; y < h; y++ {
			idx := y*img.Stride + minX*4
			img.Pix[idx] = 255
			img.Pix[idx+1] = 255
			img.Pix[idx+2] = 255
			img.Pix[idx+3] = 255
		}

		// Max marker (white vertical line)
		maxX := int(getMax() / 255.0 * float64(w-1))
		for y := 0; y < h; y++ {
			idx := y*img.Stride + maxX*4
			img.Pix[idx] = 255
			img.Pix[idx+1] = 255
			img.Pix[idx+2] = 255
			img.Pix[idx+3] = 255
		}

		return img
	})
	r.SetMinSize(fyne.NewSize(200, 60))
	return r
}

func newEditWorkspace(app fyne.App, win fyne.Window) (fyne.CanvasObject, func(image.Image)) {
	es := &editWorkspaceState{zoom: 1.0, win: win}

	// Canvas image
	es.canvasImg = canvas.NewImageFromImage(blankImg())
	es.canvasImg.FillMode = canvas.ImageFillContain

	es.healOverlay = newHealLayer()
	es.healOverlay.Hide()
	es.imgScroll = container.NewScroll(container.NewMax(es.canvasImg, es.healOverlay))
	es.imgScroll.SetMinSize(fyne.NewSize(400, 300))

	// Zoom controls
	es.zoomSelect = widget.NewSelect(append([]string{}, editZoomPresets...), func(sel string) {
		es.setZoomFromSelect(sel)
	})
	es.zoomSelect.SetSelected("fit")

	zoomOutBtn := widget.NewButton("-", func() { es.stepZoom(1.0 / 1.15) })
	zoomInBtn := widget.NewButton("+", func() { es.stepZoom(1.15) })
	zoomBar := container.NewCenter(container.NewHBox(zoomOutBtn, es.zoomSelect, zoomInBtn))
	rightPanel := container.NewBorder(zoomBar, nil, nil, nil, es.imgScroll)

	// --- Level sliders (0–255) ---
	es.rMinSlider = widget.NewSlider(0, 255)
	es.rMaxSlider = widget.NewSlider(0, 255)
	es.gMinSlider = widget.NewSlider(0, 255)
	es.gMaxSlider = widget.NewSlider(0, 255)
	es.bMinSlider = widget.NewSlider(0, 255)
	es.bMaxSlider = widget.NewSlider(0, 255)
	es.rMaxSlider.SetValue(255)
	es.gMaxSlider.SetValue(255)
	es.bMaxSlider.SetValue(255)

	// Histograms – created after sliders so they can close over them
	histColors := [3][3]uint8{{200, 60, 60}, {60, 160, 60}, {60, 100, 200}}
	es.rgbHists[0] = editHistRaster(es, 0, histColors[0],
		func() float64 { return es.rMinSlider.Value },
		func() float64 { return es.rMaxSlider.Value },
	)
	es.rgbHists[1] = editHistRaster(es, 1, histColors[1],
		func() float64 { return es.gMinSlider.Value },
		func() float64 { return es.gMaxSlider.Value },
	)
	es.rgbHists[2] = editHistRaster(es, 2, histColors[2],
		func() float64 { return es.bMinSlider.Value },
		func() float64 { return es.bMaxSlider.Value },
	)

	// --- Curves widget ---
	es.curves = newCurvesWidget(nil) // onChange wired after applyBtn exists
	es.curvesChannel = widget.NewSelect(
		[]string{"All", "Red", "Green", "Blue"},
		func(sel string) {
			switch sel {
			case "Red":
				es.curves.SetActive(1)
			case "Green":
				es.curves.SetActive(2)
			case "Blue":
				es.curves.SetActive(3)
			default:
				es.curves.SetActive(0)
			}
		},
	)
	es.curvesChannel.SetSelected("All")

	// --- Sharpen sliders ---
	es.sharpSlider = widget.NewSlider(0, 3.0)
	es.sharpSlider.Step = 0.05
	es.sharpRadiusSlider = widget.NewSlider(0.5, 10.0)
	es.sharpRadiusSlider.SetValue(1.0)
	es.sharpRadiusSlider.Step = 0.5

	// --- Load button ---
	loadBtn := widget.NewButton("Load Image...", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			defer r.Close()
			img, _, err := image.Decode(r)
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			app.Preferences().SetString("editLastDir", r.URI().Path())
			es.loadedName = r.URI().Name()
			es.setImage(img)
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".png", ".jpg", ".jpeg", ".tif", ".tiff"}))
		if last := app.Preferences().String("editLastDir"); last != "" {
			if lister, err := storage.ListerForURI(storage.NewFileURI(last)); err == nil {
				fd.SetLocation(lister)
			}
		}
		fd.SetView(dialog.ListView)
		fd.Show()
	})

	applyBtn := widget.NewButton("Apply", func() { es.applyEdits() })
	es.curves.onDragEnd = func() { es.applyEdits() }
	resetBtn := widget.NewButton("Reset", func() {
		if es.source == nil {
			return
		}
		es.rMinSlider.SetValue(0)
		es.rMaxSlider.SetValue(255)
		es.gMinSlider.SetValue(0)
		es.gMaxSlider.SetValue(255)
		es.bMinSlider.SetValue(0)
		es.bMaxSlider.SetValue(255)
		es.curves.Reset()
		es.curvesChannel.SetSelected("All")
		es.sharpSlider.SetValue(0)
		es.sharpRadiusSlider.SetValue(1.0)
		es.canvasImg.Image = es.source
		es.canvasImg.Refresh()
	})

	saveBtn := widget.NewButton("Save Image...", func() {
		if es.canvasImg.Image == nil {
			dialog.ShowInformation("Nothing to save", "Load or compose an image first.", win)
			return
		}
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()

			rgba, ok := es.canvasImg.Image.(*image.RGBA)
			if !ok {
				bounds := es.canvasImg.Image.Bounds()
				rgba = image.NewRGBA(bounds)
				for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
					for x := bounds.Min.X; x < bounds.Max.X; x++ {
						rgba.Set(x, y, es.canvasImg.Image.At(x, y))
					}
				}
			}
			format := export.PNG
			lower := strings.ToLower(path)
			switch {
			case strings.HasSuffix(lower, ".webp"):
				format = export.WEBP
			case strings.HasSuffix(lower, ".tif"), strings.HasSuffix(lower, ".tiff"):
				format = export.TIFF
			case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
				format = export.JPEG
			}
			showExportOptionsDialog(format, win, func(opts export.Options) {
				if err := export.FromImage(path, rgba, format, opts); err != nil {
					dialog.ShowError(err, win)
				}
			})
		}, win)
		saveName := "edited.png"
		if es.loadedName != "" {
			saveName = es.loadedName
		}
		save.SetFileName(saveName)
		save.SetFilter(storage.NewExtensionFileFilter([]string{".png", ".webp", ".jpg", ".jpeg", ".tif", ".tiff"}))
		save.Show()
	})

	// --- Heal tool ---
	es.healBrushSlider = widget.NewSlider(1, 200)
	es.healBrushSlider.SetValue(20)
	es.healBrushSlider.Step = 1

	es.healBrushSlider.OnChanged = func(_ float64) {
		es.updateHealBrushScreenRadius()
	}

	es.healStatusLabel = widget.NewLabel("Click to set source point")
	es.healStatusLabel.TextStyle = fyne.TextStyle{Italic: true}

	healToggleBtn := widget.NewButton("Heal Tool: OFF", nil)
	healToggleBtn.OnTapped = func() {
		es.healActive = !es.healActive
		if es.healActive {
			healToggleBtn.SetText("Heal Tool: ON")
			es.healOverlay.Show()
			es.healOverlay.Reset()
			es.updateHealBrushScreenRadius()
			es.healStatusLabel.SetText("Step 1: click source (sample area)")
		} else {
			healToggleBtn.SetText("Heal Tool: OFF")
			es.healOverlay.Hide()
			es.healOverlay.Reset()
			es.healStatusLabel.SetText("")
		}
	}

	healUndoBtn := widget.NewButton("Undo Heal (Ctrl+Z)", func() {
		es.undoHeal()
	})

	es.healOverlay.onHealStroke = func(src fyne.Position, dsts []fyne.Position) {
		es.doHealStroke(src, dsts)
		if es.healActive {
			es.healStatusLabel.SetText("Step 1: click source (sample area)")
		}
	}
	es.healOverlay.onSourceSet = func() {
		if es.healActive {
			es.healStatusLabel.SetText("Step 2: click/drag destination to heal")
		}
	}

	// Ctrl+Z undo shortcut on the window canvas.
	win.Canvas().AddShortcut(
		&desktop.CustomShortcut{KeyName: fyne.KeyZ, Modifier: fyne.KeyModifierControl},
		func(_ fyne.Shortcut) { es.undoHeal() },
	)

	// Refresh histograms whenever a level slider changes
	refreshHists := func(_ float64) {
		for _, h := range es.rgbHists {
			h.Refresh()
		}
	}

	levelsTab := container.NewTabItem("Levels", container.NewVBox(
		widget.NewLabel("Red"),
		es.rgbHists[0],
		sliderRow("Min", es.rMinSlider, refreshHists),
		sliderRow("Max", es.rMaxSlider, refreshHists),
		widget.NewLabel("Green"),
		es.rgbHists[1],
		sliderRow("Min", es.gMinSlider, refreshHists),
		sliderRow("Max", es.gMaxSlider, refreshHists),
		widget.NewLabel("Blue"),
		es.rgbHists[2],
		sliderRow("Min", es.bMinSlider, refreshHists),
		sliderRow("Max", es.bMaxSlider, refreshHists),
	))
	curvesTab := container.NewTabItem("Curves", container.NewVBox(
		es.curvesChannel,
		es.curves,
	))
	sharpenTab := container.NewTabItem("Sharpen", container.NewVBox(
		sliderRow("Strength", es.sharpSlider),
		sliderRow("Radius", es.sharpRadiusSlider),
	))
	healTab := container.NewTabItem("Heal", container.NewVBox(
		healToggleBtn,
		es.healStatusLabel,
		sliderRow("Brush Size", es.healBrushSlider),
		healUndoBtn,
	))
	tabs := container.NewAppTabs(levelsTab, curvesTab, sharpenTab, healTab)

	controls := container.NewVBox(
		loadBtn,
		widget.NewSeparator(),
		tabs,
		widget.NewSeparator(),
		container.NewHBox(applyBtn, resetBtn),
		saveBtn,
	)

	paddedControls := container.New(layout.NewCustomPaddedLayout(0, 0, 20, 20), controls)
	controlsScroll := container.NewVScroll(paddedControls)
	controlsScroll.SetMinSize(fyne.NewSize(280, 200))

	split := container.NewHSplit(controlsScroll, rightPanel)
	split.SetOffset(0.28)

	setter := func(img image.Image) {
		es.setImage(img)
	}
	return split, setter
}
