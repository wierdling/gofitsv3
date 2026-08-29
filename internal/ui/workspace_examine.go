package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	ttwidget "github.com/dweymouth/fyne-tooltip/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
	"gofitsv3/internal/utils"
)

// globalSendToExamine is set by newExamineWorkspace and called by the mosaic workspace
// to load a drizzle result directly into the examine view.
var globalSendToExamine func(pixels []float32, width, height int)
var globalSendDiagnosticToExamine func(pixels []float32, width, height int, layers map[string]fitsio.ImageData)

type examineState struct {
	img              *models.LoadedImage
	images           []*models.LoadedImage
	selectedChip     int
	headerLines      []string
	flip             bool
	measureEnabled   bool
	cursor           *imagePoint
	measureStart     *imagePoint
	measureEnd       *imagePoint
	measurement      *rulerMeasurement
	loadGeneration   uint64
	diagnosticLayers map[string]*models.LoadedImage
}

func examineChipLabel(img *models.LoadedImage, ordinal int) string {
	extver := ""
	if img != nil {
		extver = fitsio.HeaderString(img.HDU.Header, "EXTVER")
	}
	if extver == "" {
		extver = fmt.Sprintf("%d", ordinal)
	}
	return fmt.Sprintf("SCI %d (EXTVER %s)", ordinal, extver)
}

func examineChipLabels(images []*models.LoadedImage) []string {
	labels := make([]string, len(images))
	for i, img := range images {
		labels[i] = examineChipLabel(img, i+1)
	}
	return labels
}

func examineChipIndex(images []*models.LoadedImage, extver string, fallback int) int {
	if len(images) == 0 {
		return -1
	}
	if extver != "" {
		for i, img := range images {
			if img != nil && fitsio.HeaderString(img.HDU.Header, "EXTVER") == extver {
				return i
			}
		}
	}
	if fallback >= 0 && fallback < len(images) {
		return fallback
	}
	return 0
}

func diagnosticLoadedImage(name, path string, data fitsio.ImageData) *models.LoadedImage {
	pixels := data.Pixels
	if len(pixels) == 0 && len(data.Int32Pixels) > 0 {
		pixels = make([]float32, len(data.Int32Pixels))
		for i, v := range data.Int32Pixels {
			pixels[i] = float32(v)
		}
	}
	return &models.LoadedImage{Path: path + "[" + name + "]", HDU: fitsio.HDU{Data: fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: pixels}}, Mode: 0, Black: 0, White: 1, Peak: 1, ScaledPeak: 1, ShowClip: true}
}

func newExamineWorkspace(app fyne.App, win fyne.Window) fyne.CanvasObject {
	state := &examineState{
		flip:             true,
		headerLines:      []string{"No FITS loaded."},
		diagnosticLayers: make(map[string]*models.LoadedImage),
	}
	vp := newViewport()
	vp.actionRow.Objects = []fyne.CanvasObject{layout.NewSpacer(), vp.StatsLabel, hpad(6)}
	reloadBtn := ttwidget.NewButton("Reload Current FITS", func() {})
	reloadBtn.SetToolTip("Reload the current FITS from disk, keeping the current stretch settings")
	reloadBtn.Disable()
	var chipSelect *SafeSelect
	var syncControlsFromImage func()
	var refresh func()
	diagnosticSelect := NewSafeSelect(nil, func(value string) {
		if img, ok := state.diagnosticLayers[value]; ok {
			state.img = img
			syncControlsFromImage()
			refresh()
		}
	})
	diagnosticSelect.Hide()

	pathLabel := widget.NewLabel("No FITS loaded.")
	pathLabel.Wrapping = fyne.TextWrapWord
	coordLabel := widget.NewLabel("Cursor: --")
	coordLabel.TextStyle = fyne.TextStyle{Monospace: true}
	measureLabel := widget.NewLabel("Measurement: --")
	measureLabel.TextStyle = fyne.TextStyle{Monospace: true}

	headerList := widget.NewList(
		func() int { return len(state.headerLines) },
		func() fyne.CanvasObject {
			lbl := widget.NewLabel("")
			lbl.Wrapping = fyne.TextWrapOff
			lbl.TextStyle = fyne.TextStyle{Monospace: true}
			return lbl
		},
		func(id widget.ListItemID, co fyne.CanvasObject) {
			co.(*widget.Label).SetText(state.headerLines[id])
		},
	)

	updateCursor := func(point *imagePoint) {
		state.cursor = point
		if point == nil {
			coordLabel.SetText("Cursor: --")
			return
		}
		coordLabel.SetText(fmt.Sprintf("Cursor: x=%d y=%d", point.X, point.Y))
	}

	updateMeasurement := func() {
		vp.setMeasurementOverlay(state.measureStart, state.measureEnd, state.flip)
		switch {
		case state.measurement != nil:
			m := state.measurement
			measureLabel.SetText(fmt.Sprintf("Measurement: (%d,%d) -> (%d,%d) | dx=%+d dy=%+d | d=%.2f px", m.Start.X, m.Start.Y, m.End.X, m.End.Y, m.DX, m.DY, m.Distance))
		case state.measureStart != nil:
			measureLabel.SetText(fmt.Sprintf("Measurement: start at (%d,%d); click second point", state.measureStart.X, state.measureStart.Y))
		default:
			measureLabel.SetText("Measurement: --")
		}
	}

	clearMeasurement := func() {
		state.measureStart = nil
		state.measureEnd = nil
		state.measurement = nil
		updateMeasurement()
	}

	var modeSelect *SafeSelect
	var mtfMidtoneRow fyne.CanvasObject
	updateStretchParams := func() {
		if mtfMidtoneRow == nil {
			return
		}
		if modeSelect != nil && labelToMode(modeSelect.Selected) == stretch.MTF {
			mtfMidtoneRow.Show()
		} else {
			mtfMidtoneRow.Hide()
		}
	}

	modeSelect = NewSafeSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq", "MTF"}, func(value string) {
		updateStretchParams()
		if state.img == nil {
			return
		}
		state.img.Mode = labelToMode(value)
		refresh()
	})
	modeSelect.SetSelected("Linear")

	bgEntry := NewNumberEntry(0.001, 4)
	peakEntry := NewNumberEntry(0.001, 4)
	sPeakEntry := NewNumberEntry(1, 1)
	mtfMidtoneEntry := NewNumberEntry(0.01, 3)
	mtfMidtoneEntry.SetValue(stretch.DefaultMTFMidtone)

	showClip := NewToggle(func(v bool) {
		if state.img == nil {
			return
		}
		state.img.ShowClip = v
		refresh()
	})
	showClip.SetChecked(true)

	flipCheck := NewToggle(func(v bool) {
		state.flip = v
		if refresh != nil {
			refresh()
		}
	})
	flipCheck.SetChecked(true)

	measureCheck := widget.NewCheck("Measure offsets", func(v bool) {
		state.measureEnabled = v
		if !v {
			updateCursor(nil)
		}
	})

	syncControlsFromImage = func() {
		if state.img == nil {
			modeSelect.SetSelected("Linear")
			bgEntry.SetValue(0)
			peakEntry.SetValue(1)
			sPeakEntry.SetValue(1)
			mtfMidtoneEntry.SetValue(stretch.DefaultMTFMidtone)
			showClip.SetChecked(true)
			updateStretchParams()
			return
		}
		modeSelect.SetSelected(modeToLabel(state.img.Mode))
		bgEntry.SetValue(state.img.Background)
		peakEntry.SetValue(state.img.Peak)
		sPeakEntry.SetValue(state.img.ScaledPeak)
		mtf := state.img.MTFMidtone
		if mtf <= 0 || mtf >= 1 {
			mtf = stretch.DefaultMTFMidtone
		}
		mtfMidtoneEntry.SetValue(mtf)
		showClip.SetChecked(state.img.ShowClip)
		updateStretchParams()
	}

	refresh = func() {
		if state.img == nil {
			reloadBtn.Disable()
			vp.image.Image = blankImg()
			vp.origW, vp.origH = 0, 0
			vp.bins = [256]int{}
			vp.blackBox.SetValue(0)
			vp.whiteBox.SetValue(0)
			if vp.StatsLabel != nil {
				vp.StatsLabel.SetText("Mean: -- | Std: --")
			}
			vp.histogram.Refresh()
			vp.applyZoom()
			vp.image.Refresh()
			updateCursor(nil)
			updateMeasurement()
			return
		}

		reloadBtn.Enable()
		stretched, mask := processing.ApplyStretchParallel(state.img)
		if state.flip {
			stretched = processing.FlipImageData(stretched)
			mask = processing.FlipMask(mask, stretched.Width, stretched.Height)
		}
		vp.image.Image = processing.ToGrayRGBA(stretched, mask)
		vp.origW, vp.origH = stretched.Width, stretched.Height

		stats := histogram.Compute(stretched.Pixels)
		vp.bins = stats.Hist
		if vp.StatsLabel != nil {
			vp.StatsLabel.SetText(fmt.Sprintf("Mean: %.4f | Std: %.4f", stats.Mean, stats.Std))
		}
		vp.blackBox.SetValue(state.img.Black)
		vp.whiteBox.SetValue(state.img.White)
		vp.histogram.Refresh()
		if vp.zoomLabel.Selected == "fit" {
			vp.zoom = vp.fitZoom()
		}
		vp.applyZoom()
		vp.image.Refresh()
		updateMeasurement()
	}

	vp.overlay.onPointerMove = func(pos fyne.Position) {
		point, ok := vp.imagePointAtPosition(pos, state.flip)
		if !ok {
			updateCursor(nil)
			return
		}
		updateCursor(&point)
	}
	vp.overlay.onPointerOut = func() {
		updateCursor(nil)
	}
	vp.overlay.onTapped = func(pos fyne.Position) {
		if !state.measureEnabled || state.img == nil {
			return
		}
		point, ok := vp.imagePointAtPosition(pos, state.flip)
		if !ok {
			return
		}
		if state.measureStart == nil || state.measureEnd != nil {
			state.measureStart = &imagePoint{X: point.X, Y: point.Y}
			state.measureEnd = nil
			state.measurement = nil
		} else {
			state.measureEnd = &imagePoint{X: point.X, Y: point.Y}
			measurement := measurePoints(*state.measureStart, *state.measureEnd)
			state.measurement = &measurement
		}
		updateMeasurement()
	}
	vp.onViewChanged = func() {
		updateMeasurement()
	}

	applyBtn := ttwidget.NewButton("Apply values", func() {
		if state.img == nil {
			return
		}
		state.img.Background = bgEntry.Value()
		state.img.Peak = peakEntry.Value()
		state.img.ScaledPeak = sPeakEntry.Value()
		state.img.Black = vp.blackBox.Value()
		state.img.White = vp.whiteBox.Value()
		state.img.MTFMidtone = mtfMidtoneEntry.Value()
		refresh()
	})
	applyBtn.SetToolTip("Apply the Background/Peak and Black/White values to the stretch")

	autoBtn := ttwidget.NewButton("Auto scaling", func() {
		if state.img == nil {
			return
		}
		processing.AutoScaleLikeFitsLiberator(state.img)
		vp.blackBox.SetValue(state.img.Black)
		vp.whiteBox.SetValue(state.img.White)
		bgEntry.SetValue(state.img.Background)
		peakEntry.SetValue(state.img.Peak)
		sPeakEntry.SetValue(state.img.ScaledPeak)
		refresh()
	})
	autoBtn.SetToolTip("Compute Background, Peak and Black/White levels automatically (FITS Liberator style)")

	autoMTFBtn := ttwidget.NewButton("Auto MTF", func() {
		if state.img == nil {
			return
		}
		processing.AutoMTFMidtone(state.img)
		syncControlsFromImage()
		refresh()
	})
	autoMTFBtn.SetToolTip("Calculate a PixInsight-style starting MTF midtone and select MTF stretch")

	magicPreset := widget.NewSelect([]string{"Balanced", "Nebula", "Galaxy"}, nil)
	magicPreset.SetSelected("Balanced")
	magicBtn := ttwidget.NewButton("Magic", func() {
		if state.img == nil {
			return
		}
		processing.ApplyMagicLevels(state.img, processing.ParseMagicPreset(magicPreset.Selected))
		processing.AutoMTFMidtone(state.img)
		syncControlsFromImage()
		refresh()
	})
	magicBtn.SetToolTip("Estimate stretch levels using the selected Magic target preset, then apply Auto MTF")

	loadFitsFromPath := func(path string, preserveStretch bool) {
		progressDialog := dialog.NewCustom("Loading FITS", "Reading FITS data...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()
		var savedState models.ChannelState
		selectedExtVer := ""
		selectedChip := 0
		if preserveStretch && state.img != nil {
			savedState = channelStateFromImage(state.img)
			selectedExtVer = fitsio.HeaderString(state.img.HDU.Header, "EXTVER")
			selectedChip = state.selectedChip
		}

		state.loadGeneration++
		generation := state.loadGeneration
		go func() {
			imgs, loadErr := loadImagesFromPath(path)
			fyne.Do(func() {
				progressDialog.Hide()
				if generation != state.loadGeneration {
					return
				}
				if loadErr != nil {
					dialog.ShowError(loadErr, win)
					return
				}

				state.images = imgs
				state.diagnosticLayers = make(map[string]*models.LoadedImage)
				if diagnosticFile, e := fitsio.LoadFile(path); e == nil {
					for _, hdu := range diagnosticFile.HDUs {
						name := fitsio.HeaderString(hdu.Header, "EXTNAME")
						if name == "WHT" || name == "NCONTRIB" || name == "CRMASK" || name == "DQ" || name == "SKYMODEL" || name == "SEAM" || (strings.HasPrefix(name, "CTX") && name != "CTXMAP") {
							state.diagnosticLayers[name] = diagnosticLoadedImage(name, path, hdu.Data)
						}
					}
				}
				diagnosticNames := make([]string, 0, len(state.diagnosticLayers))
				for name := range state.diagnosticLayers {
					diagnosticNames = append(diagnosticNames, name)
				}
				sort.Strings(diagnosticNames)
				diagnosticSelect.Options = diagnosticNames
				if len(diagnosticNames) > 0 {
					diagnosticSelect.SetSelected(diagnosticNames[0])
					diagnosticSelect.Show()
				} else {
					diagnosticSelect.Hide()
				}
				diagnosticSelect.Refresh()
				state.selectedChip = examineChipIndex(imgs, selectedExtVer, selectedChip)
				if state.selectedChip < 0 {
					state.img = nil
					return
				}
				state.img = imgs[state.selectedChip]
				if preserveStretch {
					state.img.Mode = labelToMode(savedState.Mode)
					state.img.Black = savedState.Black
					state.img.White = savedState.White
					state.img.Background = savedState.Background
					state.img.Peak = savedState.Peak
					state.img.ScaledPeak = savedState.ScaledPeak
					state.img.MTFMidtone = savedState.MTFMidtone
					state.img.ShowClip = savedState.ShowClip
				}
				state.headerLines = utils.FormatHeadersLines(state.img.Primary, state.img.HDU.Header)
				pathLabel.SetText(path)
				if chipSelect != nil {
					chipSelect.Options = examineChipLabels(state.images)
					chipSelect.SetSelectedIndex(state.selectedChip)
					if len(state.images) > 1 {
						chipSelect.Enable()
						chipSelect.Show()
					} else {
						chipSelect.Disable()
						chipSelect.Hide()
					}
					chipSelect.Refresh()
				}
				syncControlsFromImage()
				clearMeasurement()
				headerList.Refresh()
				refresh()
			})
		}()
	}

	chipSelect = NewSafeSelect(nil, func(_ string) {
		idx := chipSelect.SelectedIndex()
		if idx < 0 || idx >= len(state.images) || idx == state.selectedChip {
			return
		}
		state.selectedChip = idx
		state.img = state.images[idx]
		state.headerLines = utils.FormatHeadersLines(state.img.Primary, state.img.HDU.Header)
		pathLabel.SetText(state.img.Path)
		syncControlsFromImage()
		clearMeasurement()
		headerList.Refresh()
		refresh()
	})
	chipSelect.Disable()
	chipSelect.Hide()

	loadFits := func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			r.Close()
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			loadFitsFromPath(path, false)
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		if last := app.Preferences().String("lastDir"); last != "" {
			uri := storage.NewFileURI(last)
			if l, err := storage.ListerForURI(uri); err == nil {
				fd.SetLocation(l)
			}
		}
		fd.SetView(dialog.ListView)
		sizeFileDialog(fd)
		fd.Show()
	}

	reloadBtn.OnTapped = func() {
		if state.img == nil || state.img.Path == "" {
			dialog.ShowInformation("Reload", "Load a FITS file first.", win)
			return
		}
		loadFitsFromPath(state.img.Path, true)
	}

	// Send to Compose channel
	channelSelect := NewSafeSelect([]string{"Channel 1", "Channel 2", "Channel 3"}, nil)
	channelSelect.SetSelectedIndex(0)
	sendToChannelBtn := ttwidget.NewButton("Send to Channel", func() {
		if globalSendToChannel == nil || state.img == nil {
			return
		}
		// Snapshot the current UI values into a copy of the image.
		imgCopy := *state.img
		imgCopy.Black = vp.blackBox.Value()
		imgCopy.White = vp.whiteBox.Value()
		imgCopy.Background = bgEntry.Value()
		imgCopy.Peak = peakEntry.Value()
		imgCopy.ScaledPeak = sPeakEntry.Value()
		imgCopy.MTFMidtone = mtfMidtoneEntry.Value()
		idx := channelSelect.SelectedIndex()
		if idx < 0 {
			idx = 0
		}
		globalSendToChannel(idx, &imgCopy)
	})
	sendToChannelBtn.SetToolTip("Send this image (with current values) to the selected Compose channel")

	loadFitsBtn := ttwidget.NewButton("Load FITS", loadFits)
	loadFitsBtn.SetToolTip("Open a FITS file from disk")
	clearMeasureBtn := ttwidget.NewButton("Clear Measurement", clearMeasurement)
	clearMeasureBtn.SetToolTip("Clear the current ruler measurement")

	mtfMidtoneRow = widget.NewForm(widget.NewFormItem("MTF midtone", mtfMidtoneEntry))
	updateStretchParams()

	controls := container.NewVBox(
		container.NewHBox(loadFitsBtn, reloadBtn),
		pathLabel,
		widget.NewSeparator(),
		widget.NewLabel("Stretch Controls"),
		modeSelect,
		widget.NewForm(
			widget.NewFormItem("Background", bgEntry),
			widget.NewFormItem("Peak", peakEntry),
			widget.NewFormItem("Scaled Peak", sPeakEntry),
		),
		mtfMidtoneRow,
		container.NewHBox(showClip, widget.NewLabel("Show clipped")),
		container.NewHBox(flipCheck, widget.NewLabel("Flip image vertically")),
		container.NewHBox(autoBtn, autoMTFBtn, applyBtn),
		container.NewHBox(magicBtn, magicPreset),
		widget.NewSeparator(),
		widget.NewLabel("SCI chip"),
		chipSelect,
		widget.NewLabel("Diagnostic layer"),
		diagnosticSelect,
		widget.NewLabel("Examine Tools"),
		measureCheck,
		coordLabel,
		measureLabel,
		clearMeasureBtn,
		widget.NewSeparator(),
		widget.NewLabel("Send to Compose"),
		channelSelect,
		sendToChannelBtn,
	)
	paddedControls := container.NewBorder(nil, nil, hpad(8), hpad(8), controls)
	controlsScroll := container.NewVScroll(paddedControls)
	controlsScroll.SetMinSize(fyne.NewSize(280, 220))

	globalSendToExamine = func(pixels []float32, width, height int) {
		// A direct handoff supersedes any FITS load still completing in the
		// background, just like a newer file request does.
		state.loadGeneration++
		img := &models.LoadedImage{
			Path: "(mosaic result)",
			HDU: fitsio.HDU{
				Data: fitsio.ImageData{
					Pixels: pixels,
					Width:  width,
					Height: height,
				},
			},
			Mode:       0,
			Black:      0,
			White:      1,
			Background: 0,
			Peak:       1,
			ScaledPeak: 10,
			ShowClip:   true,
		}
		_, img.White = processing.AutoLevels(pixels)
		img.Peak = img.White
		state.img = img
		state.images = nil
		state.selectedChip = 0
		chipSelect.Options = nil
		chipSelect.Disable()
		chipSelect.Hide()
		chipSelect.Refresh()
		state.headerLines = []string{"Mosaic drizzle result", fmt.Sprintf("Size: %dx%d", width, height)}
		pathLabel.SetText("(mosaic result)")
		syncControlsFromImage()
		clearMeasurement()
		headerList.Refresh()
		refresh()
	}
	globalSendDiagnosticToExamine = func(pixels []float32, width, height int, layers map[string]fitsio.ImageData) {
		globalSendToExamine(pixels, width, height)
		state.diagnosticLayers = make(map[string]*models.LoadedImage)
		names := make([]string, 0, len(layers))
		for name, data := range layers {
			state.diagnosticLayers[name] = diagnosticLoadedImage(name, "(mosaic diagnostic)", data)
			names = append(names, name)
		}
		sort.Strings(names)
		diagnosticSelect.Options = names
		if len(names) > 0 {
			diagnosticSelect.SetSelected(names[0])
			diagnosticSelect.Show()
		} else {
			diagnosticSelect.Hide()
		}
		diagnosticSelect.Refresh()
	}

	viewerTabs := container.NewAppTabs(
		container.NewTabItem("Viewer", vp.container),
		container.NewTabItem("Headers", headerList),
	)

	return container.NewHSplit(controlsScroll, viewerTabs)
}
