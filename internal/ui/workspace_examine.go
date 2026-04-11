package ui

import (
	"fmt"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/utils"
)

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
	reloadBtn := widget.NewButton("Reload Current FITS", func() {})
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

	modeSelect := widget.NewSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq"}, func(value string) {
		if state.img == nil {
			return
		}
		state.img.Mode = labelToMode(value)
		refresh()
	})
	modeSelect.SetSelected("Linear")

	bgEntry := widget.NewEntry()
	peakEntry := widget.NewEntry()
	sPeakEntry := widget.NewEntry()
	bgEntry.SetText("0")
	peakEntry.SetText("1")
	sPeakEntry.SetText("1")

	showClip := widget.NewCheck("Show clipped", func(v bool) {
		if state.img == nil {
			return
		}
		state.img.ShowClip = v
		refresh()
	})
	showClip.SetChecked(true)

	flipCheck := widget.NewCheck("Flip image vertically", func(v bool) {
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
			bgEntry.SetText("0")
			peakEntry.SetText("1")
			sPeakEntry.SetText("1")
			showClip.SetChecked(true)
			return
		}
		modeSelect.SetSelected(modeToLabel(state.img.Mode))
		bgEntry.SetText(fmt.Sprintf("%.3f", state.img.Background))
		peakEntry.SetText(fmt.Sprintf("%.3f", state.img.Peak))
		sPeakEntry.SetText(fmt.Sprintf("%.3f", state.img.ScaledPeak))
		showClip.SetChecked(state.img.ShowClip)
	}

	refresh = func() {
		if state.img == nil {
			reloadBtn.Disable()
			vp.image.Image = blankImg()
			vp.origW, vp.origH = 0, 0
			vp.bins = [256]int{}
			vp.blackBox.SetText("--")
			vp.whiteBox.SetText("--")
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
		vp.blackBox.SetText(fmt.Sprintf("%.3f", state.img.Black))
		vp.whiteBox.SetText(fmt.Sprintf("%.3f", state.img.White))
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

	applyBtn := widget.NewButton("Apply values", func() {
		if state.img == nil {
			return
		}
		if v, err := utils.ParseFloat(bgEntry.Text); err == nil {
			state.img.Background = v
		}
		if v, err := utils.ParseFloat(peakEntry.Text); err == nil {
			state.img.Peak = v
		}
		if v, err := utils.ParseFloat(sPeakEntry.Text); err == nil {
			state.img.ScaledPeak = v
		}
		if v, err := utils.ParseFloat(vp.blackBox.Text); err == nil {
			state.img.Black = v
		}
		if v, err := utils.ParseFloat(vp.whiteBox.Text); err == nil {
			state.img.White = v
		}
		refresh()
	})

	autoBtn := widget.NewButton("Auto scaling", func() {
		if state.img == nil {
			return
		}
		blackVal := state.img.Black
		if v, err := utils.ParseFloat(vp.blackBox.Text); err == nil {
			blackVal = v
		}
		whiteVal := state.img.White
		if v, err := utils.ParseFloat(vp.whiteBox.Text); err == nil {
			whiteVal = v
		} else {
			_, whiteVal = processing.AutoLevels(state.img.HDU.Data.Pixels)
		}
		state.img.Background = blackVal
		state.img.Peak = whiteVal
		state.img.ScaledPeak = 10
		state.img.White = whiteVal
		state.img.Black = 0
		vp.blackBox.SetText("0")
		vp.whiteBox.SetText(fmt.Sprintf("%.2f", whiteVal))
		bgEntry.SetText(fmt.Sprintf("%.2f", blackVal))
		peakEntry.SetText(fmt.Sprintf("%.2f", whiteVal))
		sPeakEntry.SetText("10")
		refresh()
	})

	loadFitsFromPath := func(path string, preserveStretch bool) {
		progressDialog := dialog.NewCustom("Loading FITS", "Reading FITS data...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			var savedState models.ChannelState
			if preserveStretch && state.img != nil {
				savedState = channelStateFromImage(state.img)
			}

			img, loadErr := loadImageFromPath(path)
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

	controls := container.NewVBox(
		widget.NewButton("Load FITS", loadFits),
		reloadBtn,
		pathLabel,
		widget.NewSeparator(),
		widget.NewLabel("Stretch Controls"),
		modeSelect,
		widget.NewForm(
			widget.NewFormItem("Background", bgEntry),
			widget.NewFormItem("Peak", peakEntry),
			widget.NewFormItem("Scaled Peak", sPeakEntry),
		),
		showClip,
		flipCheck,
		container.NewHBox(autoBtn, applyBtn),
		widget.NewSeparator(),
		widget.NewLabel("Examine Tools"),
		measureCheck,
		coordLabel,
		measureLabel,
		widget.NewButton("Clear Measurement", clearMeasurement),
	)
	controlsScroll := container.NewVScroll(controls)
	controlsScroll.SetMinSize(fyne.NewSize(280, 220))

	viewerTabs := container.NewAppTabs(
		container.NewTabItem("Viewer", vp.container),
		container.NewTabItem("Headers", headerList),
	)

	return container.NewHSplit(controlsScroll, viewerTabs)
}
