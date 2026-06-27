package ui

import (
	"fmt"
	"path/filepath"

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
	"gofitsv3/internal/utils"
)

// globalSendToExamine is set by newExamineWorkspace and called by the mosaic workspace
// to load a drizzle result directly into the examine view.
var globalSendToExamine func(pixels []float32, width, height int)

type examineState struct {
	img            *models.LoadedImage
	headerLines    []string
	flip           bool
	measureEnabled bool
	cursor         *imagePoint
	measureStart   *imagePoint
	measureEnd     *imagePoint
	measurement    *rulerMeasurement
}

func newExamineWorkspace(app fyne.App, win fyne.Window) fyne.CanvasObject {
	state := &examineState{
		flip:        true,
		headerLines: []string{"No FITS loaded."},
	}
	vp := newViewport()
	vp.actionRow.Objects = []fyne.CanvasObject{layout.NewSpacer(), vp.StatsLabel, hpad(6)}
	reloadBtn := ttwidget.NewButton("Reload Current FITS", func() {})
	reloadBtn.SetToolTip("Reload the current FITS from disk, keeping the current stretch settings")
	reloadBtn.Disable()

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

	var refresh func()

	modeSelect := NewSafeSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq"}, func(value string) {
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

	syncControlsFromImage := func() {
		if state.img == nil {
			modeSelect.SetSelected("Linear")
			bgEntry.SetValue(0)
			peakEntry.SetValue(1)
			sPeakEntry.SetValue(1)
			showClip.SetChecked(true)
			return
		}
		modeSelect.SetSelected(modeToLabel(state.img.Mode))
		bgEntry.SetValue(state.img.Background)
		peakEntry.SetValue(state.img.Peak)
		sPeakEntry.SetValue(state.img.ScaledPeak)
		showClip.SetChecked(state.img.ShowClip)
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
		if vp.zoomLabel.Selected == "fit in preview" {
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

	loadFitsFromPath := func(path string, preserveStretch bool) {
		progressDialog := dialog.NewCustom("Loading FITS", "Reading FITS data...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			var savedState models.ChannelState
			if preserveStretch && state.img != nil {
				savedState = channelStateFromImage(state.img)
			}

			img, loadErr := loadImageFromPath(path)
			fyne.Do(func() {
				progressDialog.Hide()
				if loadErr != nil {
					dialog.ShowError(loadErr, win)
					return
				}

				state.img = img
				if preserveStretch {
					state.img.Mode = labelToMode(savedState.Mode)
					state.img.Black = savedState.Black
					state.img.White = savedState.White
					state.img.Background = savedState.Background
					state.img.Peak = savedState.Peak
					state.img.ScaledPeak = savedState.ScaledPeak
					state.img.ShowClip = savedState.ShowClip
				}
				state.headerLines = utils.FormatHeadersLines(img.Primary, img.HDU.Header)
				pathLabel.SetText(path)
				syncControlsFromImage()
				clearMeasurement()
				headerList.Refresh()
				refresh()
			})
		}()
	}

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
		container.NewHBox(showClip, widget.NewLabel("Show clipped")),
		container.NewHBox(flipCheck, widget.NewLabel("Flip image vertically")),
		container.NewHBox(autoBtn, applyBtn),
		widget.NewSeparator(),
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
		state.headerLines = []string{"Mosaic drizzle result", fmt.Sprintf("Size: %dx%d", width, height)}
		pathLabel.SetText("(mosaic result)")
		syncControlsFromImage()
		clearMeasurement()
		headerList.Refresh()
		refresh()
	}

	viewerTabs := container.NewAppTabs(
		container.NewTabItem("Viewer", vp.container),
		container.NewTabItem("Headers", headerList),
	)

	return container.NewHSplit(controlsScroll, viewerTabs)
}
