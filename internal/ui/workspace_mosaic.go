package ui

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

// minWidthLayout enforces a minimum width on its single child while preserving its natural height.
type minWidthLayout struct{ w float32 }

func (l *minWidthLayout) Layout(obs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range obs {
		o.Resize(size)
		o.Move(fyne.NewPos(0, 0))
	}
}

func (l *minWidthLayout) MinSize(obs []fyne.CanvasObject) fyne.Size {
	var h float32
	for _, o := range obs {
		if m := o.MinSize().Height; m > h {
			h = m
		}
	}
	w := l.w
	for _, o := range obs {
		if m := o.MinSize().Width; m > w {
			w = m
		}
	}
	return fyne.NewSize(w, h)
}

// fixedVSpacingLayout stacks its children vertically with an exact pixel gap between them.
type fixedVSpacingLayout struct{ gap float32 }

func (l *fixedVSpacingLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	y := float32(0)
	for i, o := range objects {
		if i > 0 {
			y += l.gap
		}
		h := o.MinSize().Height
		o.Move(fyne.NewPos(0, y))
		o.Resize(fyne.NewSize(size.Width, h))
		y += h
	}
}

func (l *fixedVSpacingLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for i, o := range objects {
		if i > 0 {
			h += l.gap
		}
		ms := o.MinSize()
		if ms.Width > w {
			w = ms.Width
		}
		h += ms.Height
	}
	return fyne.NewSize(w, h)
}

// sidePaddedLayout adds equal horizontal padding on the left and right of its single child.
type sidePaddedLayout struct{ pad float32 }

func (l *sidePaddedLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(l.pad, 0))
		o.Resize(fyne.NewSize(size.Width-2*l.pad, size.Height))
	}
}

func (l *sidePaddedLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for _, o := range objects {
		ms := o.MinSize()
		if ms.Width > w {
			w = ms.Width
		}
		if ms.Height > h {
			h = ms.Height
		}
	}
	return fyne.NewSize(w+2*l.pad, h)
}

type mosaicState struct {
	inputs      []mosaic.Input
	statuses    []mosaic.InputStatus
	result      *mosaic.Result
	savePreview bool
	// referenceInput is an optional drizzled baseline used as the WCS anchor for
	// star alignment and drizzle. Its pixels are not included in the output.
	referenceInput       *mosaic.Input
	drizzleSettings      models.DrizzleSettings
	drizzleSettingsSet   bool
	alignmentSettings    models.AlignmentSettings
	alignmentSettingsSet bool
	skysubSettings       models.SkysubSettings
	skysubSettingsSet    bool
}

func newMosaicWorkspace(app fyne.App, win fyne.Window) (fyne.CanvasObject, *fyne.Menu) {
	state := &mosaicState{drizzleSettings: defaultDrizzleSettings(), alignmentSettings: defaultAlignmentSettings(), skysubSettings: defaultSkysubSettings()}
	activeFilter := ""    // set when a filter batch is loaded; used for default save names
	lastProjectName := "" // updated on save/load so the save dialog pre-populates the same name

	preview := canvas.NewImageFromImage(blankImg())
	preview.FillMode = canvas.ImageFillContain
	preview.SetMinSize(fyne.NewSize(520, 420))

	statsLabel := widget.NewLabel("Mean: -- | Std: -- | Size: --")
	statsLabel.TextStyle = fyne.TextStyle{Monospace: true}
	statusLabel := widget.NewLabel("No FITS files loaded.")
	statusLabel.Wrapping = fyne.TextWrapWord
	statusLabel.TextStyle = fyne.TextStyle{Monospace: true}
	offsetControls := container.NewVBox(widget.NewLabel("No FITS files loaded."))
	offsetScroll := container.NewVScroll(offsetControls)
	offsetScroll.SetMinSize(fyne.NewSize(260, 180))
	offsetHeader := container.NewVBox()
	saveBtn := widget.NewButton("Save Drizzle FITS", func() {})
	saveBtn.Disable()
	sendToExamineBtn := widget.NewButton("Send to Examine", func() {
		if globalSendToExamine == nil || state.result == nil {
			return
		}
		globalSendToExamine(state.result.Pixels, state.result.Width, state.result.Height)
	})
	sendToExamineBtn.Disable()
	saveOffsetsBtn := widget.NewButton("Save Offsets", func() {})
	saveOffsetsBtn.Disable()
	loadOffsetsBtn := widget.NewButton("Load Offsets", func() {})
	loadOffsetsBtn.Disable()

	// Reference baseline UI elements.
	refLabel := widget.NewLabel("Reference: none")
	refLabel.TextStyle = fyne.TextStyle{Italic: true}

	// inputsWithRef returns state.inputs prepended with the reference baseline
	// (if set). The reference is marked ReferenceOnly so Build() uses it only
	// for WCS anchoring and excludes its pixels from the output.
	inputsWithRef := func() []mosaic.Input {
		var active []mosaic.Input
		for _, inp := range state.inputs {
			if !inp.Excluded {
				active = append(active, inp)
			}
		}
		if state.referenceInput == nil {
			return active
		}
		ref := *state.referenceInput
		ref.ReferenceOnly = true
		return append([]mosaic.Input{ref}, active...)
	}

	// Active star picker (non-nil only while in star-selection mode).
	var activePicker *starPickerWidget

	// Active measure picker (non-nil only while in measure mode).
	var activeMeasure *measurePickerWidget

	// Containers swapped during star-selection/measure mode – set after controls are built.
	var leftStack *fyne.Container
	var previewSwap *fyne.Container
	var previewScroll *container.Scroll
	var pickerScroll *container.Scroll
	var controlsScroll *container.Scroll
	var starPanelScroll *container.Scroll
	var measurePanelScroll *container.Scroll

	// ---- Preview stretch levels -----------------------------------------------

	zoomLevel := 1.0
	zoomFitMode := false // true when "fit in preview" is active
	levelsSet := false
	var starModeRefResult *mosaic.Result
	stretchMode := stretch.Asinh

	var mosaicBins [256]int
	mosaicHistogram := canvas.NewRaster(func(w, h int) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := range img.Pix {
			img.Pix[i] = 255
		}
		maxCount := 0
		for _, c := range mosaicBins {
			if c > maxCount {
				maxCount = c
			}
		}
		if maxCount == 0 {
			return img
		}
		for i, c := range mosaicBins {
			x := i * w / len(mosaicBins)
			barH := int(float64(c) / float64(maxCount) * float64(h))
			for y := h - 1; y >= h-barH; y-- {
				idx := y*img.Stride + x*4
				img.Pix[idx] = 80
				img.Pix[idx+1] = 80
				img.Pix[idx+2] = 80
				img.Pix[idx+3] = 255
			}
		}
		return img
	})
	mosaicHistogram.SetMinSize(fyne.NewSize(200, 48))

	blackEntry := NewNumberEntry(0.001, 4)
	whiteEntry := NewNumberEntry(0.001, 4)
	bgEntry := NewNumberEntry(0.001, 4)
	peakEntry := NewNumberEntry(1, 1)
	scaledPeakEntry := NewNumberEntry(1, 1)

	blackEntry.SetValue(0)
	whiteEntry.SetValue(1)
	bgEntry.SetValue(0)
	peakEntry.SetValue(1000)
	scaledPeakEntry.SetValue(1000)

	parseLevelEntries := func() (black, white, bg, peak, scaledPeak float64) {
		black = blackEntry.Value()
		white = whiteEntry.Value()
		bg = bgEntry.Value()
		peak = peakEntry.Value()
		scaledPeak = scaledPeakEntry.Value()
		if peak <= 0 {
			peak = 1000
		}
		if scaledPeak <= 0 {
			scaledPeak = 1000
		}
		return
	}

	applyLevelsToPreview := func() {
		black, white, bg, peak, scaledPeak := parseLevelEntries()
		if activePicker != nil && starModeRefResult != nil {
			img := buildMosaicPreviewImageWithLevels(starModeRefResult, black, white, bg, peak, scaledPeak, stretchMode)
			activePicker.SetImage(img)
		} else if state.result != nil {
			img := buildMosaicPreviewImageWithLevels(state.result, black, white, bg, peak, scaledPeak, stretchMode)
			preview.Image = img
			preview.Refresh()
		}
	}

	var updateZoom func()
	var loadLevelPrefsAndMode func(string) bool

	autoLevels := func(pixels []float32) {
		img := &models.LoadedImage{
			HDU: fitsio.HDU{Data: fitsio.ImageData{Pixels: pixels}},
		}
		processing.AutoScaleLikeFitsLiberator(img)
		blackEntry.SetValue(img.Black)
		whiteEntry.SetValue(img.White)
		bgEntry.SetValue(img.Background)
		peakEntry.SetValue(img.Peak)
		scaledPeakEntry.SetValue(img.ScaledPeak)
		levelsSet = true
	}

	type savedLevels struct {
		Black      string `json:"black"`
		White      string `json:"white"`
		Background string `json:"background"`
		Peak       string `json:"peak"`
		ScaledPeak string `json:"scaledPeak"`
		Mode       string `json:"mode"`
	}

	prefKey := func(filter string) string { return "mosaicLevels_" + filter }

	saveLevelPrefs := func() {
		if activeFilter == "" {
			return
		}
		data, err := json.Marshal(savedLevels{
			Black:      fmt.Sprintf("%.4f", blackEntry.Value()),
			White:      fmt.Sprintf("%.4f", whiteEntry.Value()),
			Background: fmt.Sprintf("%.4f", bgEntry.Value()),
			Peak:       fmt.Sprintf("%.4f", peakEntry.Value()),
			ScaledPeak: fmt.Sprintf("%.4f", scaledPeakEntry.Value()),
			Mode:       modeNameForMode(stretchMode),
		})
		if err == nil {
			app.Preferences().SetString(prefKey(activeFilter), string(data))
		}
	}

	syncStatusOffsets := func() {
		for i := range state.statuses {
			if i >= len(state.inputs) {
				break
			}
			state.statuses[i].OffsetX = state.inputs[i].OffsetX
			state.statuses[i].OffsetY = state.inputs[i].OffsetY
			state.statuses[i].HasAffine = state.inputs[i].HasManualTransform
			if state.inputs[i].HasManualTransform {
				t := state.inputs[i].ManualTransform
				state.statuses[i].AffineRotDeg = math.Atan2(t.D, t.A) * 180 / math.Pi
			} else {
				state.statuses[i].AffineRotDeg = 0
			}
		}
	}
	currentFilterAndDir := func() (string, string, bool) {
		if len(state.inputs) == 0 {
			return "", "", false
		}
		filter := mosaic.FilterNameForInput(state.inputs[0])
		dir := filepath.Dir(state.inputs[0].Path)
		for _, input := range state.inputs[1:] {
			if mosaic.FilterNameForInput(input) != filter || filepath.Dir(input.Path) != dir {
				return "", "", false
			}
		}
		return filter, dir, true
	}
	updateOffsetButtons := func() {
		if _, _, ok := currentFilterAndDir(); ok {
			saveOffsetsBtn.Enable()
			loadOffsetsBtn.Enable()
		} else {
			saveOffsetsBtn.Disable()
			loadOffsetsBtn.Disable()
		}
	}
	updateStatus := func() {
		syncStatusOffsets()
		statusLabel.SetText(strings.Join(mosaic.FormatStatusLines(state.statuses), "\n"))
		updateOffsetButtons()
	}
	resetPreview := func() {
		state.result = nil
		saveBtn.Disable()
		preview.Image = blankImg()
		preview.Refresh()
		statsLabel.SetText("Mean: -- | Std: -- | Size: --")
		mosaicBins = [256]int{}
		mosaicHistogram.Refresh()
	}
	var rebuildOffsetControls func()

	// buildDrizzlePreview runs a drizzle build and updates the preview UI.
	// It must only be called from a goroutine (it shows a progress dialog and blocks).
	var buildDrizzlePreview func()

	applyAutoLoadedOffsets := func(inputs []mosaic.Input) []string {
		_, messages := mosaic.AutoLoadOffsets(inputs)
		return messages
	}

	// ---- Star selection mode ------------------------------------------------

	starCountLabel := widget.NewLabel("Selected: 0 / 10 stars")

	clearStarsBtn := widget.NewButton("Clear Stars", func() {
		if activePicker != nil {
			activePicker.ClearStars()
		}
	})

	applyStarsBtn := widget.NewButton("Apply", nil)
	cancelStarsBtn := widget.NewButton("Cancel", nil)

	// pickerToRefPixels converts stars from the picker's image-pixel space to
	// canonical raw-reference-pixel space.  When the picker is showing a drizzle
	// output the displayed image has a scale and an origin offset; we undo those
	// here so that saved star files are stable across re-drizzles.
	pickerToRefPixels := func(stars []processing.Star) []processing.Star {
		r := starModeRefResult
		if r == nil || r.Scale <= 0 {
			return stars
		}
		out := make([]processing.Star, len(stars))
		for i, s := range stars {
			out[i] = processing.Star{X: s.X/r.Scale + r.OriginX, Y: s.Y/r.Scale + r.OriginY}
		}
		return out
	}

	// refPixelsToPicker is the inverse: raw-ref-pixel → current picker image pixel.
	refPixelsToPicker := func(stars []processing.Star) []processing.Star {
		r := starModeRefResult
		if r == nil || r.Scale <= 0 {
			return stars
		}
		out := make([]processing.Star, len(stars))
		for i, s := range stars {
			out[i] = processing.Star{X: (s.X - r.OriginX) * r.Scale, Y: (s.Y - r.OriginY) * r.Scale}
		}
		return out
	}

	saveStarsBtn := widget.NewButton("Save Stars...", func() {
		if activePicker == nil || len(activePicker.Stars) == 0 {
			dialog.ShowInformation("No Stars", "Select at least one star before saving.", win)
			return
		}
		fd := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()
			if filepath.Ext(path) == "" {
				path += ".json"
			}
			// Save in raw reference-pixel space so the file is stable across
			// re-drizzles that change scale/origin.
			type starFile struct {
				Stars []processing.Star `json:"stars"`
			}
			data, jsonErr := json.MarshalIndent(starFile{Stars: pickerToRefPixels(activePicker.Stars)}, "", "  ")
			if jsonErr != nil {
				dialog.ShowError(jsonErr, win)
				return
			}
			if writeErr := os.WriteFile(path, data, 0644); writeErr != nil {
				dialog.ShowError(writeErr, win)
				return
			}
			app.Preferences().SetString("lastDir", filepath.Dir(path))
		}, win)
		starFileName := "ref_stars.json"
		if activeFilter != "" {
			starFileName = activeFilter + "_ref_stars.json"
		}
		fd.SetFileName(starFileName)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
		if last := app.Preferences().String("lastDir"); last != "" {
			if uri := storage.NewFileURI(last); uri != nil {
				if l, err := storage.ListerForURI(uri); err == nil {
					fd.SetLocation(l)
				}
			}
		}
		fd.Show()
	})

	loadStarsBtn := widget.NewButton("Load Stars...", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			r.Close()
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				dialog.ShowError(readErr, win)
				return
			}
			type starFile struct {
				Stars []processing.Star `json:"stars"`
			}
			var sf starFile
			if jsonErr := json.Unmarshal(data, &sf); jsonErr != nil {
				dialog.ShowError(jsonErr, win)
				return
			}
			if activePicker == nil {
				dialog.ShowInformation("Not in Star Mode", "Open the Select Stars dialog before loading.", win)
				return
			}
			activePicker.Stars = refPixelsToPicker(sf.Stars)
			if activePicker.OnChanged != nil {
				activePicker.OnChanged()
			}
			activePicker.Refresh()
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
		if last := app.Preferences().String("lastDir"); last != "" {
			if uri := storage.NewFileURI(last); uri != nil {
				if l, err := storage.ListerForURI(uri); err == nil {
					fd.SetLocation(l)
				}
			}
		}
		fd.SetView(dialog.ListView)
		fd.Show()
	})

	starPanel := container.NewVBox(
		widget.NewLabel("Select Reference Stars"),
		widget.NewSeparator(),
		widget.NewLabel("Click on stars in the preview image.\nRight-click a marker to remove it.\nUse zoom +/- to get a closer look.\nLevels controls are in the main panel."),
		widget.NewSeparator(),
		starCountLabel,
		clearStarsBtn,
		container.NewGridWithColumns(2, saveStarsBtn, loadStarsBtn),
		widget.NewSeparator(),
		container.NewGridWithColumns(2, cancelStarsBtn, applyStarsBtn),
	)

	exitStarMode := func() {
		activePicker = nil
		starModeRefResult = nil
		previewSwap.Objects = []fyne.CanvasObject{previewScroll}
		previewSwap.Refresh()

		if state.result != nil {
			black, white, bg, peak, scaledPeak := parseLevelEntries()
			img := buildMosaicPreviewImageWithLevels(state.result, black, white, bg, peak, scaledPeak, stretchMode)
			preview.Image = img
			preview.Refresh()
			stats := histogram.Compute(state.result.Pixels)
			mosaicBins = stats.Hist
			mosaicHistogram.Refresh()
			statsLabel.SetText(fmt.Sprintf("Mean: %.4f | Std: %.4f | Size: %dx%d", stats.Mean, stats.Std, state.result.Width, state.result.Height))
		} else {
			statsLabel.SetText("Mean: -- | Std: -- | Size: --")
			mosaicBins = [256]int{}
			mosaicHistogram.Refresh()
		}

		starPanelScroll.Hide()
		controlsScroll.Show()
		leftStack.Refresh()
	}

	enterStarMode := func() {
		if len(state.inputs) == 0 {
			dialog.ShowInformation("No Files", "Load at least one FITS file before selecting stars.", win)
			return
		}
		var refResult *mosaic.Result
		if state.result != nil {
			refResult = state.result
		} else {
			ref := state.inputs[0]
			refResult = &mosaic.Result{
				Pixels: ref.HDU.Data.Pixels,
				Width:  ref.HDU.Data.Width,
				Height: ref.HDU.Data.Height,
			}
		}
		starModeRefResult = refResult
		if !levelsSet {
			autoLevels(refResult.Pixels)
		}
		black, white, bg, peak, scaledPeak := parseLevelEntries()
		refImg := buildMosaicPreviewImageWithLevels(refResult, black, white, bg, peak, scaledPeak, stretchMode)

		activePicker = newStarPickerWidget(refImg, refResult.Width, refResult.Height)
		activePicker.SetZoom(zoomLevel)
		activePicker.OnChanged = func() {
			n := len(activePicker.Stars)
			starCountLabel.SetText(fmt.Sprintf("Selected: %d / %d stars", n, activePicker.MaxStars))
		}
		// Capture pixels for centroiding (copy slice header; pixels are not modified).
		centPixels := refResult.Pixels
		centW, centH := refResult.Width, refResult.Height
		activePicker.CentroidFn = func(x, y float64) (float64, float64, bool) {
			return processing.CentroidNear(centPixels, centW, centH, x, y, 15)
		}

		starCountLabel.SetText("Selected: 0 / 50 stars")

		pickerScroll.Content = activePicker
		pickerScroll.Refresh()
		previewSwap.Objects = []fyne.CanvasObject{pickerScroll}
		previewSwap.Refresh()

		statsLabel.SetText("Click on stars in the reference image. Right-click to remove.")

		controlsScroll.Hide()
		starPanelScroll.Show()
		leftStack.Refresh()
	}

	applyStarsBtn.OnTapped = func() {
		if activePicker == nil || len(activePicker.Stars) == 0 {
			dialog.ShowInformation("No Stars Selected", "Click on at least one star in the image before applying.", win)
			return
		}
		if len(state.inputs) < 2 {
			exitStarMode()
			return
		}
		// If stars were picked on the drizzled mosaic, convert from mosaic pixel
		// space back to inputs[0] reference pixel space before alignment.
		refStars := make([]processing.Star, len(activePicker.Stars))
		src := starModeRefResult // capture before exitStarMode clears it
		for i, s := range activePicker.Stars {
			if src != nil && src.Scale > 0 {
				refStars[i] = processing.Star{
					X: s.X/src.Scale + src.OriginX,
					Y: s.Y/src.Scale + src.OriginY,
				}
			} else {
				refStars[i] = s
			}
		}
		exitStarMode()

		progressDialog := dialog.NewCustom("Aligning By Selected Stars", "Matching selected stars across images...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()
		go func() {
			alignInputs := inputsWithRef()
			numRefs := state.alignmentSettings.NumRefs
			if numRefs < 1 {
				numRefs = 1
			}
			results, err := mosaic.AlignInputsBySelectedStarsWithMode(alignInputs, refStars, numRefs, mosaic.AlignmentMode(state.alignmentSettings.AlignmentMode), state.alignmentSettings.SearchRadiusArcsec)

			type alignRow struct {
				stateIdx int
				result   mosaic.StarAlignmentResult
			}
			var rows []alignRow
			if err == nil {
				var activeIndices []int
				for i, inp := range state.inputs {
					if !inp.Excluded {
						activeIndices = append(activeIndices, i)
					}
				}
				offset := 0
				if state.referenceInput != nil {
					offset = 1
				}
				for ri := offset; ri < len(results); ri++ {
					ai := ri - offset
					if ai >= len(activeIndices) {
						continue
					}
					si := activeIndices[ai]
					if si >= len(state.inputs) || state.inputs[si].OffsetLocked {
						continue
					}
					rows = append(rows, alignRow{stateIdx: si, result: results[ri]})
				}
			}

			fyne.Do(func() {
				progressDialog.Hide()
				if err != nil {
					dialog.ShowError(err, win)
					return
				}
				if len(rows) == 0 {
					dialog.ShowInformation("Star Alignment", "No alignment results to review.", win)
					return
				}

				content := container.NewVBox()
				checks := make([]*widget.Check, len(rows))
				for i, r := range rows {
					name := mosaic.InputLabel(state.inputs[r.stateIdx])
					if r.result.Applied {
						rot := 0.0
						if r.result.HasManualTransform {
							t := r.result.ManualTransform
							rot = math.Atan2(t.D, t.A) * 180 / math.Pi
						}
						label := fmt.Sprintf("%s  X: %.2f  Y: %.2f  Rot°: %.4f", name, r.result.OffsetX, r.result.OffsetY, rot)
						chk := widget.NewCheck(label, nil)
						chk.SetChecked(true)
						checks[i] = chk
						content.Add(chk)
					} else {
						label := fmt.Sprintf("%s  [failed: %s]", name, r.result.Error)
						chk := widget.NewCheck(label, nil)
						chk.Disable()
						checks[i] = chk
						content.Add(chk)
					}
				}

				var d dialog.Dialog
				applyBtn := widget.NewButton("Apply", func() {
					for i, r := range rows {
						if checks[i] == nil || !checks[i].Checked {
							continue
						}
						si := r.stateIdx
						if si >= len(state.inputs) || si >= len(state.statuses) {
							continue
						}
						state.inputs[si].OffsetX = r.result.OffsetX
						state.inputs[si].OffsetY = r.result.OffsetY
						state.inputs[si].ManualTransform = r.result.ManualTransform
						state.inputs[si].HasManualTransform = r.result.HasManualTransform
						if si == 0 && state.referenceInput == nil {
							state.statuses[si].Status = "reference"
						} else {
							state.statuses[si].Status = "star aligned"
						}
						state.statuses[si].Error = ""
					}
					d.Hide()
					rebuildOffsetControls()
					updateStatus()
					go buildDrizzlePreview()
				})
				scroll := container.NewVScroll(content)
				scroll.SetMinSize(fyne.NewSize(520, 200))
				d = dialog.NewCustom("Star Alignment Results", "Dismiss", container.NewVBox(scroll, applyBtn), win)
				d.Show()
			})
		}()
	}

	cancelStarsBtn.OnTapped = func() {
		exitStarMode()
	}

	// ---- Measure mode -------------------------------------------------------

	measureStatusLabel := widget.NewLabel("Click point A on the image.")
	measureStatusLabel.Wrapping = fyne.TextWrapWord

	cancelMeasureBtn := widget.NewButton("Cancel", nil)
	measureCentroidCheck := widget.NewCheck("Snap to centroid", nil)
	measureCentroidCheck.SetChecked(true)

	measurePanel := container.NewVBox(
		widget.NewLabel("Measure Distance"),
		widget.NewSeparator(),
		widget.NewLabel("Click two points on the image.\nPress Escape to cancel."),
		measureCentroidCheck,
		widget.NewSeparator(),
		measureStatusLabel,
		cancelMeasureBtn,
	)

	exitMeasureMode := func() {
		activeMeasure = nil
		previewSwap.Objects = []fyne.CanvasObject{previewScroll}
		previewSwap.Refresh()

		if state.result != nil {
			black, white, bg, peak, scaledPeak := parseLevelEntries()
			img := buildMosaicPreviewImageWithLevels(state.result, black, white, bg, peak, scaledPeak, stretchMode)
			preview.Image = img
			preview.Refresh()
			stats := histogram.Compute(state.result.Pixels)
			mosaicBins = stats.Hist
			mosaicHistogram.Refresh()
		}

		measurePanelScroll.Hide()
		controlsScroll.Show()
		leftStack.Refresh()
	}

	// pointInConvexQuad tests whether (px, py) lies inside the convex quad
	// defined by corners in TL(0), TR(1), BL(2), BR(3) order.
	pointInConvexQuad := func(px, py float64, corners [4][2]float64) bool {
		// Reorder to a consistent winding: TL, TR, BR, BL.
		pts := [4][2]float64{corners[0], corners[1], corners[3], corners[2]}
		var sign float64
		for i := 0; i < 4; i++ {
			a := pts[i]
			b := pts[(i+1)%4]
			cross := (b[0]-a[0])*(py-a[1]) - (b[1]-a[1])*(px-a[0])
			if i == 0 {
				if cross >= 0 {
					sign = 1
				} else {
					sign = -1
				}
			} else if cross*sign < 0 {
				return false
			}
		}
		return true
	}

	// pathsContainingPoint returns the set of unique input paths whose footprint
	// contains the given result-image pixel coordinate.
	pathsContainingPoint := func(px, py float64) map[string]bool {
		result := make(map[string]bool)
		if state.result == nil {
			return result
		}
		for i, fp := range state.result.InputFootprints {
			if i >= len(state.result.InputFootprintPaths) {
				break
			}
			if pointInConvexQuad(px, py, fp) {
				result[state.result.InputFootprintPaths[i]] = true
			}
		}
		return result
	}

	showMeasureResultDialog := func(ptA, ptB measurePoint) {
		if state.result == nil {
			return
		}
		scale := state.result.Scale
		if scale <= 0 {
			scale = 1
		}
		dxOrig := (ptB.X - ptA.X) / scale
		dyOrig := (ptB.Y - ptA.Y) / scale

		// Build drizzle-order map: path → 1-based rank (1 = reference).
		drizzleRank := make(map[string]int, len(state.inputs))
		if len(state.inputs) > 0 {
			drizzleRank[mosaic.InputKey(state.inputs[0])] = 1
			if len(state.inputs) > 1 {
				for rank, idx := range mosaic.DrizzleOrder(state.inputs) {
					if idx < len(state.inputs) {
						drizzleRank[mosaic.InputKey(state.inputs[idx])] = rank + 2
					}
				}
			}
		}

		labelForPath := func(p string) string {
			base := filepath.Base(p)
			if r, ok := drizzleRank[p]; ok {
				return fmt.Sprintf("%s (#%d)", base, r)
			}
			return base
		}

		pathsA := pathsContainingPoint(ptA.X, ptA.Y)
		pathsB := pathsContainingPoint(ptB.X, ptB.Y)

		// Find the minimum drizzle rank for each group so we know which is the
		// "earlier" (lower-ranked) reference group and which should move.
		minRankForPaths := func(paths map[string]bool) int {
			best := math.MaxInt64
			for p := range paths {
				if r, ok := drizzleRank[p]; ok && r < best {
					best = r
				}
			}
			return best
		}
		minRankA := minRankForPaths(pathsA)
		minRankB := minRankForPaths(pathsB)

		// moverPaths = group that should move; anchorPaths = reference group.
		// The mover group is whichever has the higher drizzle rank.
		// dxApply/dyApply is the offset to add to each mover image.
		var moverPaths map[string]bool
		var dxApply, dyApply float64
		autoDirection := minRankA != math.MaxInt64 && minRankB != math.MaxInt64 && len(pathsA) > 0 && len(pathsB) > 0
		if autoDirection {
			if minRankA <= minRankB {
				// A is earlier; move B's images toward A.
				moverPaths = pathsB
				dxApply = (ptA.X - ptB.X) / scale
				dyApply = (ptA.Y - ptB.Y) / scale
			} else {
				// B is earlier; move A's images toward B.
				moverPaths = pathsA
				dxApply = (ptB.X - ptA.X) / scale
				dyApply = (ptB.Y - ptA.Y) / scale
			}
		} else {
			// Can't determine direction; fall back to B-A and pre-check all.
			dxApply = dxOrig
			dyApply = dyOrig
		}

		namesA := []string{}
		namesB := []string{}
		for p := range pathsA {
			namesA = append(namesA, labelForPath(p))
		}
		for p := range pathsB {
			namesB = append(namesB, labelForPath(p))
		}
		sort.Strings(namesA)
		sort.Strings(namesB)

		nameA := "none"
		if len(namesA) > 0 {
			nameA = strings.Join(namesA, ", ")
		}
		nameB := "none"
		if len(namesB) > 0 {
			nameB = strings.Join(namesB, ", ")
		}

		// Build alphabetically sorted input list with checkboxes.
		type inputEntry struct {
			idx     int
			name    string
			path    string
			checked bool
		}
		entries := make([]inputEntry, len(state.inputs))
		for i, inp := range state.inputs {
			var preChecked bool
			if autoDirection {
				preChecked = moverPaths[mosaic.InputKey(inp)]
			} else {
				preChecked = pathsA[mosaic.InputKey(inp)] || pathsB[mosaic.InputKey(inp)]
			}
			entries[i] = inputEntry{idx: i, name: labelForPath(mosaic.InputKey(inp)), path: mosaic.InputKey(inp), checked: preChecked}
		}
		sort.Slice(entries, func(i, j int) bool {
			return strings.ToLower(entries[i].name) < strings.ToLower(entries[j].name)
		})

		checkBoxes := make([]*widget.Check, len(entries))
		checkList := container.NewVBox()
		for i := range entries {
			i := i
			chk := widget.NewCheck(entries[i].name, func(v bool) {
				entries[i].checked = v
			})
			chk.SetChecked(entries[i].checked)
			checkBoxes[i] = chk
			checkList.Add(chk)
		}
		checkScroll := container.NewVScroll(checkList)
		checkScroll.SetMinSize(fyne.NewSize(380, 400))

		deltaLabel := fmt.Sprintf("ΔX: %.2f px   ΔY: %.2f px  (original pixels)", dxApply, dyApply)

		content := container.NewVBox(
			widget.NewLabelWithStyle(deltaLabel, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabel(fmt.Sprintf("Point A in: %s", nameA)),
			widget.NewLabel(fmt.Sprintf("Point B in: %s", nameB)),
			widget.NewSeparator(),
			widget.NewLabel("Move selected images by the offset above:"),
			checkScroll,
		)

		dlgHeight := win.Canvas().Size().Height * 0.75
		if dlgHeight < 400 {
			dlgHeight = 400
		}
		dlg := dialog.NewCustomConfirm(
			"Measure Distance",
			"Apply & Drizzle",
			"Cancel",
			content,
			func(ok bool) {
				exitMeasureMode()
				if !ok {
					return
				}
				applied := 0
				for _, e := range entries {
					if !e.checked {
						continue
					}
					idx := e.idx
					if idx >= len(state.inputs) {
						continue
					}
					state.inputs[idx].OffsetX += dxApply
					state.inputs[idx].OffsetY += dyApply
					applied++
				}
				if applied == 0 {
					return
				}
				rebuildOffsetControls()
				updateStatus()
				// Start drizzle in background; progress dialog shows via fyne.DoAndWait.
				go buildDrizzlePreview()
			},
			win,
		)
		dlg.Resize(fyne.NewSize(460, dlgHeight))
		dlg.Show()
	}

	enterMeasureMode := func() {
		if state.result == nil {
			dialog.ShowInformation("No Drizzle Result", "Build a drizzle preview before measuring.", win)
			return
		}

		// Capture scroll position and zoom before switching modes (item 6).
		savedScrollOffset := previewScroll.Offset

		black, white, bg, peak, scaledPeak := parseLevelEntries()
		refImg := buildMosaicPreviewImageWithLevels(state.result, black, white, bg, peak, scaledPeak, stretchMode)

		activeMeasure = newMeasurePickerWidget(refImg, state.result.Width, state.result.Height)
		activeMeasure.SetZoom(zoomLevel)

		// Wire centroiding on the stretched preview image. The display-ready RGBA
		// has background compressed and stars as sharp bright peaks, giving much
		// better centroid accuracy than the raw float32 drizzle data.
		capturedMeasure := activeMeasure
		capturedPreview := refImg
		applyCentroidFn := func(enabled bool) {
			if enabled {
				capturedMeasure.CentroidFn = func(x, y float64) (float64, float64, bool) {
					return centroidNearPreview(capturedPreview, x, y, 8)
				}
			} else {
				capturedMeasure.CentroidFn = nil
			}
		}
		applyCentroidFn(measureCentroidCheck.Checked)
		measureCentroidCheck.OnChanged = func(checked bool) {
			if capturedMeasure == activeMeasure {
				applyCentroidFn(checked)
			}
		}

		activeMeasure.OnStatus = func(msg string) {
			measureStatusLabel.SetText(msg)
		}
		activeMeasure.OnCancel = func() {
			exitMeasureMode()
		}
		activeMeasure.OnComplete = func(ptA, ptB measurePoint) {
			showMeasureResultDialog(ptA, ptB)
		}

		measureStatusLabel.SetText("Click point A on the image.")

		pickerScroll.Content = activeMeasure
		pickerScroll.Offset = savedScrollOffset
		pickerScroll.Refresh()
		previewSwap.Objects = []fyne.CanvasObject{pickerScroll}
		previewSwap.Refresh()

		controlsScroll.Hide()
		measurePanelScroll.Show()
		leftStack.Refresh()

		// Give focus to the widget so Escape key events are delivered.
		if c, ok := win.Canvas().(interface{ Focus(fyne.Focusable) }); ok {
			c.Focus(activeMeasure)
		}
	}

	cancelMeasureBtn.OnTapped = func() {
		exitMeasureMode()
	}

	// ---- File loading -------------------------------------------------------

	loadPaths := func(paths []string, title string) {
		// Filter out paths already loaded.
		existingPaths := make(map[string]bool, len(state.inputs))
		for _, inp := range state.inputs {
			existingPaths[inp.Path] = true
		}
		filtered := paths[:0:0]
		for _, p := range paths {
			if !existingPaths[p] {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) == 0 {
			dialog.ShowInformation("Already Added", "All selected files are already in the mosaic.", win)
			return
		}
		skipped := len(paths) - len(filtered)
		paths = filtered

		progressDialog := dialog.NewCustom(title, "Reading FITS data...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			newInputs := make([]mosaic.Input, 0, len(paths))
			newStatuses := make([]mosaic.InputStatus, 0, len(paths))
			warnings := 0
			_ = skipped // available for future status reporting

			for _, path := range paths {
				loadedInputs, err := mosaic.LoadInputsFromPath(path)
				if err != nil {
					newStatuses = append(newStatuses, mosaic.InputStatus{Path: path, Status: "failed", Error: err.Error()})
					continue
				}
				for _, input := range loadedInputs {
					newInputs = append(newInputs, input)
					status := mosaic.InputStatus{Path: mosaic.InputKey(input), Included: true, Status: "loaded"}
					if !mosaic.LooksLikeFLC(path) {
						status.Status = "loaded (warning: not _flc)"
						warnings++
					}
					newStatuses = append(newStatuses, status)
				}
			}

			offsetMessages := applyAutoLoadedOffsets(newInputs)
			for i := range newInputs {
				if i < len(newStatuses) && (newInputs[i].OffsetX != 0 || newInputs[i].OffsetY != 0) && !strings.Contains(newStatuses[i].Status, "failed") {
					newStatuses[i].Status = "loaded offsets"
				}
			}

			// Sort newly loaded inputs by WCS distance from their first image so
			// the drizzle processing order is meaningful in the offset controls.
			if len(newInputs) > 1 {
				mosaic.SortInputsByWCSDistance(newInputs, newStatuses)
			}

			messages := make([]string, 0, len(offsetMessages)+1)
			if warnings > 0 {
				messages = append(messages, "Some loaded files are not standard _flc inputs. They were kept, but this workflow is tuned for HST _flc science files.")
			}
			messages = append(messages, offsetMessages...)
			fyne.DoAndWait(func() {
				progressDialog.Hide()
				state.inputs = append(state.inputs, newInputs...)
				state.statuses = append(state.statuses, newStatuses...)
				// Re-sort the full inputs list so overall drizzle order is correct.
				if len(state.inputs) > 1 {
					mosaic.SortInputsByWCSDistance(state.inputs, state.statuses)
				}
				resetPreview()
				rebuildOffsetControls()
				updateStatus()
				if len(messages) > 0 {
					dialog.ShowInformation("Mosaic Load", strings.Join(messages, "\n"), win)
				}
			})
		}()
	}

	configureLastDir := func(fd *dialog.FileDialog) {
		if last := app.Preferences().String("lastDir"); last != "" {
			uri := storage.NewFileURI(last)
			if l, err := storage.ListerForURI(uri); err == nil {
				fd.SetLocation(l)
			}
		}
	}

	rebuildOffsetControls = func() {
		offsetControls.Objects = nil
		offsetHeader.Objects = nil
		if len(state.inputs) == 0 {
			offsetControls.Add(widget.NewLabel("No FITS files loaded."))
			offsetControls.Refresh()
			offsetHeader.Refresh()
			return
		}

		// nameCell wraps a label in a container with a minimum width so every row's
		// name column is the same width regardless of text length.
		nameCell := func(obj fyne.CanvasObject) fyne.CanvasObject {
			return container.New(&minWidthLayout{160}, obj)
		}
		hdrLabel := func(text string) fyne.CanvasObject {
			return widget.NewLabelWithStyle(text, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
		}

		// Measure actual data-widget widths at runtime so the header cells match exactly.
		compactEntry := NewNumberEntry(1, 2)
		compactEntry.MinWidth = 1 // use the content's natural minimum (no extra enforcement)
		entryColW := compactEntry.MinSize().Width

		btnUpW := widget.NewButton("↑", nil).MinSize().Width
		flashBtnW := widget.NewButton("Flash", nil).MinSize().Width
		applyBtnW := widget.NewButton("Apply", nil).MinSize().Width
		// Compute column width for lock/include: at least as wide as the label so text is not clipped.
		checkNatW := container.NewCenter(widget.NewCheck("", nil)).MinSize().Width
		lockColW := hdrLabel("Lock").MinSize().Width
		if lockColW < checkNatW {
			lockColW = checkNatW
		}
		inclColW := hdrLabel("Incl.").MinSize().Width
		if inclColW < checkNatW {
			inclColW = checkNatW
		}

		// hdrCell forces a header label to exactly the width of the corresponding data widget.
		hdrCell := func(text string, w float32) fyne.CanvasObject {
			return container.New(&minWidthLayout{w: w}, hdrLabel(text))
		}
		hdrSpace := func(w float32) fyne.CanvasObject {
			r := canvas.NewRectangle(color.Transparent)
			r.SetMinSize(fyne.NewSize(w, 1))
			return r
		}

		header := container.NewHBox(
			hdrSpace(btnUpW), hdrSpace(btnUpW),
			nameCell(hdrLabel("Name")),
			hdrCell("X", entryColW),
			hdrCell("Y", entryColW),
			hdrCell("Rot°", entryColW),
			hdrCell("Flash", flashBtnW),
			hdrCell("Apply", applyBtnW),
			hdrCell("Lock", lockColW),
			hdrCell("Incl.", inclColW),
		)
		offsetHeader.Add(header)
		offsetHeader.Refresh()

		for idx := range state.inputs {
			name := mosaic.InputLabel(state.inputs[idx])
			xEntry := NewNumberEntry(1, 2)
			xEntry.MinWidth = 1
			xEntry.SetValue(state.inputs[idx].OffsetX)
			yEntry := NewNumberEntry(1, 2)
			yEntry.MinWidth = 1
			yEntry.SetValue(state.inputs[idx].OffsetY)
			rotEntry := NewNumberEntry(0.01, 4)
			rotEntry.MinWidth = 1
			currentRot := 0.0
			if state.inputs[idx].HasManualTransform {
				t := state.inputs[idx].ManualTransform
				currentRot = math.Atan2(t.D, t.A) * 180 / math.Pi
			}
			rotEntry.SetValue(currentRot)

			includeCheck := widget.NewCheck("", func(index int) func(bool) {
				return func(included bool) {
					state.inputs[index].Excluded = !included
				}
			}(idx))
			includeCheck.SetChecked(!state.inputs[idx].Excluded)

			lockCheck := widget.NewCheck("", func(index int) func(bool) {
				return func(locked bool) {
					state.inputs[index].OffsetLocked = locked
				}
			}(idx))
			lockCheck.SetChecked(state.inputs[idx].OffsetLocked)

			applyBtn := widget.NewButton("Apply", func(index int, xBox, yBox, rotBox *NumberEntry) func() {
				return func() {
					xVal := xBox.Value()
					yVal := yBox.Value()
					rotVal := rotBox.Value()
					state.inputs[index].OffsetX = xVal
					state.inputs[index].OffsetY = yVal
					if rotVal != 0 {
						rad := rotVal * math.Pi / 180
						cx := float64(state.inputs[index].HDU.Data.Width-1) / 2
						cy := float64(state.inputs[index].HDU.Data.Height-1) / 2
						state.inputs[index].ManualTransform = processing.RotationAround(cx, cy, rad)
						state.inputs[index].HasManualTransform = true
					} else {
						state.inputs[index].ManualTransform = processing.IdentityTransform()
						state.inputs[index].HasManualTransform = false
					}
					if index < len(state.statuses) && state.statuses[index].Status == "loaded" {
						state.statuses[index].Status = "manual offset set"
					}
					resetPreview()
					updateStatus()
				}
			}(idx, xEntry, yEntry, rotEntry))

			flashBtn := widget.NewButton("Flash", func(index int) func() {
				return func() {
					if state.result == nil || activePicker != nil || activeMeasure != nil {
						return
					}
					inputPath := mosaic.InputKey(state.inputs[index])
					var fps [][4][2]float64
					for fi, fp := range state.result.InputFootprints {
						if fi < len(state.result.InputFootprintPaths) && state.result.InputFootprintPaths[fi] == inputPath {
							fps = append(fps, fp)
						}
					}
					if len(fps) == 0 {
						return
					}
					black, white, bg, peak, scaledPeak := parseLevelEntries()
					go func() {
						baseImg := buildMosaicPreviewImageWithLevels(state.result, black, white, bg, peak, scaledPeak, stretchMode)
						flashImg := buildMosaicPreviewImageWithLevels(state.result, black, white, bg, peak, scaledPeak, stretchMode)
						salmon := color.RGBA{R: 250, G: 128, B: 114, A: 255}
						pairs := [4][2]int{{0, 1}, {1, 3}, {3, 2}, {2, 0}}
						for _, fp := range fps {
							for _, p := range pairs {
								drawThickLine(flashImg, fp[p[0]], fp[p[1]], 5, salmon)
							}
						}
						for i := 0; i < 3; i++ {
							fyne.DoAndWait(func() {
								preview.Image = flashImg
								preview.Refresh()
							})
							time.Sleep(500 * time.Millisecond)
							fyne.DoAndWait(func() {
								preview.Image = baseImg
								preview.Refresh()
							})
							if i < 2 {
								time.Sleep(500 * time.Millisecond)
							}
						}
					}()
				}
			}(idx))

			if idx == 0 {
				xEntry.Disable()
				yEntry.Disable()
				rotEntry.Disable()
				applyBtn.Disable()
				lockCheck.Disable()
			}

			upBtn := widget.NewButton("↑", func(index int) func() {
				return func() {
					if index == 0 {
						return
					}
					state.inputs[index-1], state.inputs[index] = state.inputs[index], state.inputs[index-1]
					if index < len(state.statuses) && index-1 < len(state.statuses) {
						state.statuses[index-1], state.statuses[index] = state.statuses[index], state.statuses[index-1]
					}
					resetPreview()
					rebuildOffsetControls()
				}
			}(idx))
			downBtn := widget.NewButton("↓", func(index int) func() {
				return func() {
					if index >= len(state.inputs)-1 {
						return
					}
					state.inputs[index], state.inputs[index+1] = state.inputs[index+1], state.inputs[index]
					if index < len(state.statuses) && index+1 < len(state.statuses) {
						state.statuses[index], state.statuses[index+1] = state.statuses[index+1], state.statuses[index]
					}
					resetPreview()
					rebuildOffsetControls()
				}
			}(idx))
			if idx == 0 {
				upBtn.Disable()
			}
			if idx == len(state.inputs)-1 {
				downBtn.Disable()
			}

			row := container.NewHBox(
				upBtn, downBtn,
				nameCell(widget.NewLabel(name)),
				xEntry, yEntry, rotEntry,
				flashBtn, applyBtn,
				container.New(&minWidthLayout{w: lockColW}, container.NewCenter(lockCheck)),
				container.New(&minWidthLayout{w: inclColW}, container.NewCenter(includeCheck)),
			)
			offsetControls.Add(row)
		}
		offsetControls.Refresh()
	}

	loadBtn := widget.NewButton("Add FITS", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			r.Close()
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			loadPaths([]string{path}, "Loading FITS")
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		configureLastDir(fd)
		fd.SetView(dialog.ListView)
		fd.Show()
	})

	batchBtn := widget.NewButton("Add Filter Batch", func() {
		fd := dialog.NewFolderOpen(func(listable fyne.ListableURI, err error) {
			if err != nil || listable == nil {
				return
			}
			dir := listable.Path()
			app.Preferences().SetString("lastDir", dir)

			progressDialog := dialog.NewCustom("Scanning Filters", "Reading primary FITS headers...", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()

			go func() {
				groups, scanErr := mosaic.DiscoverFilters(dir)
				fyne.Do(func() {
					progressDialog.Hide()
					if scanErr != nil {
						dialog.ShowError(scanErr, win)
						return
					}

					options := mosaic.FilterOptions(groups)
					filterSelect := widget.NewSelect(options, nil)

					type fileCheck struct {
						path    string
						checked bool
					}
					var fileChecks []fileCheck
					checkContainer := container.NewVBox()
					filesScroll := container.NewVScroll(checkContainer)
					filesScroll.SetMinSize(fyne.NewSize(420, 220))

					updateSelectedFiles := func(option string) {
						paths := mosaic.PathsForFilterOption(groups, option)
						fileChecks = make([]fileCheck, len(paths))
						checkContainer.Objects = nil
						for i, path := range paths {
							i, path := i, path
							fileChecks[i] = fileCheck{path: path, checked: true}
							chk := widget.NewCheck(filepath.Base(path), func(v bool) {
								fileChecks[i].checked = v
							})
							chk.SetChecked(true)
							checkContainer.Add(chk)
						}
						checkContainer.Refresh()
					}
					filterSelect.OnChanged = updateSelectedFiles
					filterSelect.SetSelected(options[0])

					content := container.NewVBox(
						widget.NewLabel("Select the filter to load from the discovered raw _flc inputs:"),
						filterSelect,
						filesScroll,
					)
					confirm := dialog.NewCustomConfirm("Load Filter Batch", "Load Files", "Cancel", content, func(ok bool) {
						if !ok {
							return
						}
						selected := filterSelect.Selected
						// Extract bare filter name (strip " (N files)" suffix).
						if idx := strings.LastIndex(selected, " ("); idx >= 0 {
							selected = selected[:idx]
						}
						activeFilter = selected
						loadLevelPrefsAndMode(activeFilter)
						var paths []string
						for _, fc := range fileChecks {
							if fc.checked {
								paths = append(paths, fc.path)
							}
						}
						if len(paths) == 0 {
							return
						}
						loadPaths(paths, "Loading Filter Batch")
					}, win)
					confirm.Resize(fyne.NewSize(540, 420))
					confirm.Show()
				})
			}()
		}, win)
		if last := app.Preferences().String("lastDir"); last != "" {
			uri := storage.NewFileURI(last)
			if l, err := storage.ListerForURI(uri); err == nil {
				fd.SetLocation(l)
			}
		}
		fd.Show()
	})

	savePreviewToggle := NewToggle(func(v bool) {
		state.savePreview = v
	})

	measureBtn := widget.NewButton("Measure", func() {
		enterMeasureMode()
	})

	starAlignBtn := widget.NewButton("Align By Stars", func() {
		if len(state.inputs) < 2 {
			dialog.ShowInformation("Missing Inputs", "Load at least two FITS files before star alignment.", win)
			return
		}
		progressDialog := dialog.NewCustom("Aligning By Stars", "Refining per-image offsets from stars in the shared overlap...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()
		go func() {
			alignInputs := inputsWithRef()
			numRefs := state.alignmentSettings.NumRefs
			if numRefs < 1 {
				numRefs = 1
			}
			results, err := mosaic.AlignInputsByStarsWithMode(alignInputs, numRefs, mosaic.AlignmentMode(state.alignmentSettings.AlignmentMode), state.alignmentSettings.SearchRadiusArcsec)

			// Build the row data entirely off the main goroutine before touching UI.
			type alignRow struct {
				stateIdx int
				result   mosaic.StarAlignmentResult
			}
			var rows []alignRow
			if err == nil {
				var activeIndices []int
				for i, inp := range state.inputs {
					if !inp.Excluded {
						activeIndices = append(activeIndices, i)
					}
				}
				offset := 0
				if state.referenceInput != nil {
					offset = 1
				}
				for ri := offset; ri < len(results); ri++ {
					ai := ri - offset
					if ai >= len(activeIndices) {
						continue
					}
					si := activeIndices[ai]
					if si >= len(state.inputs) || state.inputs[si].OffsetLocked {
						continue
					}
					rows = append(rows, alignRow{stateIdx: si, result: results[ri]})
				}
			}

			fyne.Do(func() {
				progressDialog.Hide()
				if err != nil {
					dialog.ShowError(err, win)
					return
				}
				if len(rows) == 0 {
					dialog.ShowInformation("Star Alignment", "No alignment results to review.", win)
					return
				}

				content := container.NewVBox()
				checks := make([]*widget.Check, len(rows))
				for i, r := range rows {
					name := mosaic.InputLabel(state.inputs[r.stateIdx])
					if r.result.Applied {
						rot := 0.0
						if r.result.HasManualTransform {
							t := r.result.ManualTransform
							rot = math.Atan2(t.D, t.A) * 180 / math.Pi
						}
						label := fmt.Sprintf("%s  X: %.2f  Y: %.2f  Rot°: %.4f", name, r.result.OffsetX, r.result.OffsetY, rot)
						chk := widget.NewCheck(label, nil)
						chk.SetChecked(true)
						checks[i] = chk
						content.Add(chk)
					} else {
						label := fmt.Sprintf("%s  [failed: %s]", name, r.result.Error)
						chk := widget.NewCheck(label, nil)
						chk.Disable()
						checks[i] = chk
						content.Add(chk)
					}
				}

				var d dialog.Dialog
				applyBtn := widget.NewButton("Apply", func() {
					for i, r := range rows {
						if checks[i] == nil || !checks[i].Checked {
							continue
						}
						si := r.stateIdx
						if si >= len(state.inputs) || si >= len(state.statuses) {
							continue
						}
						state.inputs[si].OffsetX = r.result.OffsetX
						state.inputs[si].OffsetY = r.result.OffsetY
						state.inputs[si].ManualTransform = r.result.ManualTransform
						state.inputs[si].HasManualTransform = r.result.HasManualTransform
						if si == 0 && state.referenceInput == nil {
							state.statuses[si].Status = "reference"
						} else {
							state.statuses[si].Status = "star aligned"
						}
						state.statuses[si].Error = ""
					}
					d.Hide()
					rebuildOffsetControls()
					updateStatus()
					go buildDrizzlePreview()
				})
				scroll := container.NewVScroll(content)
				scroll.SetMinSize(fyne.NewSize(520, 200))
				d = dialog.NewCustom("Star Alignment Results", "Dismiss", container.NewVBox(scroll, applyBtn), win)
				d.Show()
			})
		}()
	})

	selectStarsBtn := widget.NewButton("Select Stars...", func() {
		enterStarMode()
	})

	buildDrizzlePreview = func() {
		// Release previous result before building the new one so the old pixel
		// arrays can be collected before the new ones are allocated.
		state.result = nil

		var progressDialog dialog.Dialog
		fyne.DoAndWait(func() {
			progressDialog = dialog.NewCustom("Processing", "Aligning, cleaning, and drizzling selected inputs...", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()
		})

		s := state.drizzleSettings
		weightingMode := mosaic.WeightingMode(s.WeightingMode)
		if weightingMode == mosaic.WeightUniform && s.UseERRWeighting {
			weightingMode = mosaic.WeightERR
		}
		result, err := mosaic.Build(inputsWithRef(), mosaic.Options{
			Scale:         s.Scale,
			FinalScale:    s.FinalScale,
			PixFrac:       s.PixFrac,
			CRMethod:      mosaic.CRMethod(s.CRMethod),
			SepKernel:     mosaic.DrizzleKernel(s.SepKernel),
			FinalKernel:   mosaic.DrizzleKernel(s.FinalKernel),
			WeightingMode: weightingMode,
			CRSeedSNR:     s.CRSeedSNR,
			CRDerivScale:  s.CRDerivScale,
			Skysub:        skysubOptionsFromSettings(state.skysubSettings),
		})

		fyne.DoAndWait(func() { progressDialog.Hide() })

		if err != nil {
			fyne.Do(func() { dialog.ShowError(err, win) })
			return
		}

		debuglog.Log(fmt.Sprintf("buildDrizzlePreview: Build done, result %dx%d (%d pixels)", result.Width, result.Height, len(result.Pixels)))
		state.result = result

		// Do CPU-heavy work off the main thread before touching any UI.
		if !levelsSet {
			debuglog.Log("buildDrizzlePreview: autoLevels (off main thread)")
			autoLevels(result.Pixels)
		}
		debuglog.Log("buildDrizzlePreview: buildMosaicPreviewImageWithLevels (off main thread)")
		black, white, bg, peak, scaledPeak := parseLevelEntries()
		previewImg := buildMosaicPreviewImageWithLevels(result, black, white, bg, peak, scaledPeak, stretchMode)
		debuglog.Log("buildDrizzlePreview: histogram.Compute (off main thread)")
		stats := histogram.Compute(result.Pixels)

		debuglog.Log("buildDrizzlePreview: submitting UI update to main thread")
		uiDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-uiDone:
					return
				case <-ticker.C:
					debuglog.Log("buildDrizzlePreview: still waiting for main thread...")
				}
			}
		}()
		fyne.DoAndWait(func() {
			close(uiDone)
			debuglog.Log("buildDrizzlePreview: UI update start (on main thread)")
			state.statuses = result.Inputs
			saveBtn.Enable()
			sendToExamineBtn.Enable()
			debuglog.Log("buildDrizzlePreview: rebuildOffsetControls")
			rebuildOffsetControls()
			debuglog.Log("buildDrizzlePreview: updateStatus")
			updateStatus()
			debuglog.Log("buildDrizzlePreview: preview.Refresh")
			preview.Image = previewImg
			preview.Refresh()
			mosaicBins = stats.Hist
			mosaicHistogram.Refresh()
			statsLabel.SetText(fmt.Sprintf("Mean: %.4f | Std: %.4f | Size: %dx%d", stats.Mean, stats.Std, result.Width, result.Height))
			debuglog.Log("buildDrizzlePreview: updateZoom")
			updateZoom()
			debuglog.Log("buildDrizzlePreview: UI update done")
		})

		if state.savePreview {
			debuglog.Log("buildDrizzlePreview: saving preview FITS")
			filter, dir, ok := currentFilterAndDir()
			if !ok {
				fyne.Do(func() {
					dialog.ShowInformation("Preview Save Skipped", "Automatic preview save requires the loaded files to come from one directory and one filter.", win)
				})
				return
			}
			previewPath := filepath.Join(dir, filter+"_preview.fits")
			if err := mosaic.SaveResultFITS(previewPath, result); err != nil {
				fyne.Do(func() { dialog.ShowError(err, win) })
				return
			}
			debuglog.Log("buildDrizzlePreview: preview FITS saved")
			fyne.Do(func() {
				dialog.ShowInformation("Preview Saved", fmt.Sprintf("Saved preview FITS to %s.", filepath.Base(previewPath)), win)
			})
		}
	}

	buildBtn := widget.NewButton("Build Drizzle Preview", func() {
		if len(state.inputs) == 0 {
			dialog.ShowInformation("Missing Inputs", "Add one or more FITS files first.", win)
			return
		}
		if !state.drizzleSettingsSet {
			showDrizzleSettingsDialog(win, state.drizzleSettings, func(s models.DrizzleSettings) {
				state.drizzleSettings = s
				state.drizzleSettingsSet = true
				go buildDrizzlePreview()
			})
			return
		}
		go buildDrizzlePreview()
	})

	saveBtn.OnTapped = func() {
		if state.result == nil {
			dialog.ShowInformation("Nothing to Save", "Build a drizzle result first.", win)
			return
		}
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()
			if filepath.Ext(path) == "" {
				path += ".fits"
			}
			if err := mosaic.SaveResultFITS(path, state.result); err != nil {
				dialog.ShowError(err, win)
				return
			}
			dialog.ShowInformation("Saved", "Drizzle FITS saved successfully.", win)
		}, win)
		drizzleName := "mosaic_drizzle.fits"
		if activeFilter != "" {
			drizzleName = activeFilter + "_drizzle.fits"
		}
		save.SetFileName(drizzleName)
		save.SetFilter(storage.NewExtensionFileFilter([]string{".fits"}))
		save.Show()
	}

	saveOffsetsBtn.OnTapped = func() {
		filter, dir, ok := currentFilterAndDir()
		if !ok {
			dialog.ShowInformation("Unavailable", "Offset save currently requires the loaded files to come from one directory and one filter.", win)
			return
		}
		defaultPath := filepath.Join(dir, mosaic.OffsetFileName(filter))
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()
			if filepath.Ext(path) == "" {
				path = defaultPath
			}
			if err := mosaic.SaveOffsetsForInputs(path, filter, state.inputs); err != nil {
				dialog.ShowError(err, win)
				return
			}
			_ = mosaic.UpdateMasterOffsets(dir, state.inputs)
			dialog.ShowInformation("Saved", "Offset file saved successfully.", win)
		}, win)
		save.SetFileName(mosaic.OffsetFileName(filter))
		save.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
		save.Show()
	}

	loadOffsetsBtn.OnTapped = func() {
		filter, dir, ok := currentFilterAndDir()
		if !ok {
			dialog.ShowInformation("Unavailable", "Offset load currently requires the loaded files to come from one directory and one filter.", win)
			return
		}
		path := filepath.Join(dir, mosaic.OffsetFileName(filter))
		loadedFilter, records, err := mosaic.LoadOffsets(path)
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		if loadedFilter != "" && loadedFilter != filter {
			dialog.ShowInformation("Filter Mismatch", fmt.Sprintf("Offset file is for filter %s, but the loaded data is %s.", loadedFilter, filter), win)
			return
		}
		applied := mosaic.ApplyOffsetsToInputs(state.inputs, filter, records)
		resetPreview()
		rebuildOffsetControls()
		updateStatus()
		dialog.ShowInformation("Loaded", fmt.Sprintf("Applied %d saved offsets from %s.", applied, filepath.Base(path)), win)
	}

	clearOffsetsBtn := widget.NewButton("Clear Offsets", func() {
		for i := range state.inputs {
			state.inputs[i].OffsetX = 0
			state.inputs[i].OffsetY = 0
			state.inputs[i].ManualTransform = processing.IdentityTransform()
			state.inputs[i].HasManualTransform = false
		}
		resetPreview()
		rebuildOffsetControls()
	})
	clearOffsetsBtn.Importance = widget.DangerImportance

	clearBtn := widget.NewButton("Clear", func() {
		state.inputs = nil
		state.statuses = nil
		activeFilter = ""
		levelsSet = false
		blackEntry.SetValue(0)
		whiteEntry.SetValue(1)
		bgEntry.SetValue(0)
		peakEntry.SetValue(1000)
		scaledPeakEntry.SetValue(1000)
		updateStatus()
		resetPreview()
		rebuildOffsetControls()
	})
	clearBtn.Importance = widget.DangerImportance

	statusScroll := container.NewVScroll(statusLabel)
	statusScroll.SetMinSize(fyne.NewSize(260, 160))

	// Level controls form.
	modeSelect := widget.NewSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq"}, func(s string) {
		switch s {
		case "Linear":
			stretchMode = stretch.Linear
		case "Log":
			stretchMode = stretch.Log
		case "Sqrt":
			stretchMode = stretch.Sqrt
		case "HistEq":
			stretchMode = stretch.HistEq
		default:
			stretchMode = stretch.Asinh
		}
	})
	modeSelect.SetSelected("Asinh")
	makeFormRow := func(label string, w fyne.CanvasObject) fyne.CanvasObject {
		lbl := widget.NewLabel(label)
		return container.NewBorder(nil, nil, container.New(&minWidthLayout{w: 90}, lbl), nil, w)
	}
	levelsForm := container.New(&fixedVSpacingLayout{15},
		makeFormRow("Mode", modeSelect),
		makeFormRow("Background", bgEntry),
		makeFormRow("Peak", peakEntry),
		makeFormRow("Scaled Peak", scaledPeakEntry),
	)
	autoLevelsBtn := widget.NewButton("Auto Scaling", func() {
		if state.result != nil {
			autoLevels(state.result.Pixels)
			applyLevelsToPreview()
		} else if starModeRefResult != nil {
			autoLevels(starModeRefResult.Pixels)
			applyLevelsToPreview()
		}
	})
	applyLevelsBtn := widget.NewButton("Apply Values", func() {
		applyLevelsToPreview()
		saveLevelPrefs()
	})

	// loadLevelPrefsAndMode loads saved settings for filter and also updates modeSelect.
	loadLevelPrefsAndMode = func(filter string) bool {
		raw := app.Preferences().String(prefKey(filter))
		if raw == "" {
			return false
		}
		type savedLevels2 struct {
			Black      string `json:"black"`
			White      string `json:"white"`
			Background string `json:"background"`
			Peak       string `json:"peak"`
			ScaledPeak string `json:"scaledPeak"`
			Mode       string `json:"mode"`
		}
		var sl savedLevels2
		if err := json.Unmarshal([]byte(raw), &sl); err != nil {
			return false
		}
		if v, err2 := strconv.ParseFloat(strings.TrimSpace(sl.Black), 64); err2 == nil {
			blackEntry.SetValue(v)
		}
		if v, err2 := strconv.ParseFloat(strings.TrimSpace(sl.White), 64); err2 == nil {
			whiteEntry.SetValue(v)
		}
		if v, err2 := strconv.ParseFloat(strings.TrimSpace(sl.Background), 64); err2 == nil {
			bgEntry.SetValue(v)
		}
		if v, err2 := strconv.ParseFloat(strings.TrimSpace(sl.Peak), 64); err2 == nil {
			peakEntry.SetValue(v)
		}
		if v, err2 := strconv.ParseFloat(strings.TrimSpace(sl.ScaledPeak), 64); err2 == nil {
			scaledPeakEntry.SetValue(v)
		}
		if sl.Mode != "" {
			modeSelect.SetSelected(sl.Mode)
		}
		levelsSet = true
		return true
	}

	setRefBtn := widget.NewButton("Set Reference Baseline...", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			r.Close()
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			inp, loadErr := mosaic.LoadInputFromPath(path)
			if loadErr != nil {
				dialog.ShowError(loadErr, win)
				return
			}
			inp.ReferenceOnly = true
			state.referenceInput = &inp
			refLabel.SetText("Reference: " + filepath.Base(path))
			resetPreview()
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		configureLastDir(fd)
		fd.SetView(dialog.ListView)
		fd.Show()
	})

	clearRefBtn := widget.NewButton("Clear Reference", func() {
		state.referenceInput = nil
		refLabel.SetText("Reference: none")
		resetPreview()
	})
	clearRefBtn.Importance = widget.DangerImportance

	inputTabs := container.NewAppTabs(
		container.NewTabItem("Input Frames", container.NewBorder(offsetHeader, nil, nil, nil, offsetScroll)),
		container.NewTabItem("Input Status", statusScroll),
	)

	controls := container.NewVBox(
		widget.NewLabel("Preview Levels"),
		levelsForm,
		func() fyne.CanvasObject {
			r := canvas.NewRectangle(color.Transparent)
			r.SetMinSize(fyne.NewSize(1, 20))
			return r
		}(),
		container.NewGridWithColumns(2, autoLevelsBtn, applyLevelsBtn),
		widget.NewSeparator(),
		widget.NewLabel("Mosaic / Drizzle"),
		container.NewGridWithColumns(2, loadBtn, batchBtn),
		container.NewHBox(savePreviewToggle, widget.NewLabel("Save Preview")),
		widget.NewSeparator(),
		widget.NewLabel("Baseline Reference"),
		refLabel,
		container.New(&fixedVSpacingLayout{15},
			container.NewGridWithColumns(2, setRefBtn, clearRefBtn),
			container.NewGridWithColumns(2, starAlignBtn, selectStarsBtn),
			container.NewGridWithColumns(2, measureBtn, buildBtn),
			container.NewGridWithColumns(2, saveOffsetsBtn, loadOffsetsBtn),
			container.NewGridWithColumns(2, clearOffsetsBtn, clearBtn),
		),
		widget.NewSeparator(),
		inputTabs,
	)

	// Now assign all the variables that enterStarMode/exitStarMode need.
	// Wrap controls with a 20px right pad so the vertical scrollbar never
	// overlaps the rightmost widgets.
	controlsRightPad := canvas.NewRectangle(color.Transparent)
	controlsRightPad.SetMinSize(fyne.NewSize(20, 1))
	controlsScroll = container.NewVScroll(container.NewBorder(nil, nil, nil, controlsRightPad, controls))
	controlsScroll.SetMinSize(fyne.NewSize(320, 220))

	starPanelScroll = container.NewVScroll(starPanel)
	starPanelScroll.SetMinSize(fyne.NewSize(320, 220))
	starPanelScroll.Hide()

	measurePanelScroll = container.NewVScroll(measurePanel)
	measurePanelScroll.SetMinSize(fyne.NewSize(320, 220))
	measurePanelScroll.Hide()

	leftStack = container.NewStack(controlsScroll, starPanelScroll, measurePanelScroll)

	previewScroll = container.NewScroll(preview)
	pickerScroll = container.NewScroll(widget.NewLabel(""))
	previewSwap = container.NewStack(previewScroll)

	// Zoom controls for the preview pane header.
	zoomPresets := []string{"fit in preview", "6%", "12%", "25%", "50%", "75%", "100%", "150%", "200%", "300%", "400%"}
	zoomSelect := widget.NewSelect(zoomPresets, nil)
	zoomCustomEntry := widget.NewEntry()
	zoomCustomEntry.SetPlaceHolder("custom %")
	zoomCustomEntry.Resize(fyne.NewSize(70, zoomCustomEntry.MinSize().Height))
	zoomCustomOption := ""     // tracks a custom option added to the dropdown
	zoomSelectSyncing := false // guard against re-entrant OnChanged

	// fitZoom returns the zoom level that fits the image in the preview scroll.
	fitZoom := func() float64 {
		var imgW, imgH int
		if state.result != nil {
			imgW, imgH = state.result.Width, state.result.Height
		} else {
			imgW, imgH = 600, 500
		}
		sz := previewScroll.Size()
		if sz.Width <= 1 || sz.Height <= 1 {
			sz = fyne.NewSize(600, 500)
		}
		z := math.Min(float64(sz.Width)/float64(imgW), float64(sz.Height)/float64(imgH))
		return math.Max(z, 1.0/16)
	}

	// setZoomSelectLabel updates the dropdown to reflect the current zoom without triggering OnChanged.
	setZoomSelectLabel := func(option string) {
		if zoomSelectSyncing {
			return
		}
		zoomSelectSyncing = true
		defer func() { zoomSelectSyncing = false }()
		// If it's not a preset, manage a single custom slot at the end.
		isPreset := false
		for _, p := range zoomPresets {
			if p == option {
				isPreset = true
				break
			}
		}
		if !isPreset {
			opts := zoomSelect.Options
			if zoomCustomOption != "" {
				filtered := opts[:0]
				for _, o := range opts {
					if o != zoomCustomOption {
						filtered = append(filtered, o)
					}
				}
				opts = filtered
			}
			zoomCustomOption = option
			opts = append(opts, option)
			zoomSelect.Options = opts
		}
		zoomSelect.SetSelected(option)
	}

	updateZoom = func() {
		if zoomFitMode {
			zoomLevel = fitZoom()
		}
		if activeMeasure != nil {
			activeMeasure.SetZoom(zoomLevel)
			pickerScroll.Refresh()
		} else if activePicker != nil {
			activePicker.SetZoom(zoomLevel)
			pickerScroll.Refresh()
		} else {
			var w, h float32
			if state.result != nil {
				w = float32(float64(state.result.Width) * zoomLevel)
				h = float32(float64(state.result.Height) * zoomLevel)
			} else {
				w = float32(600 * zoomLevel)
				h = float32(500 * zoomLevel)
			}
			preview.SetMinSize(fyne.NewSize(w, h))
			preview.Refresh()
			previewScroll.Refresh()
		}
		if !zoomFitMode {
			pct := math.Round(zoomLevel*100*10) / 10
			var label string
			if pct == math.Trunc(pct) {
				label = fmt.Sprintf("%d%%", int(pct))
			} else {
				label = fmt.Sprintf("%.1f%%", pct)
			}
			setZoomSelectLabel(label)
		}
	}

	applyCustomZoom := func() {
		s := strings.TrimSuffix(strings.TrimSpace(zoomCustomEntry.Text), "%")
		pct, err := strconv.ParseFloat(s, 64)
		if err != nil || pct <= 0 {
			zoomCustomEntry.SetText("")
			return
		}
		zoomFitMode = false
		zoomLevel = math.Max(math.Min(pct/100.0, 16), 1.0/16)
		zoomCustomEntry.SetText("")
		updateZoom()
	}
	zoomCustomEntry.OnSubmitted = func(_ string) { applyCustomZoom() }

	zoomSelect.OnChanged = func(sel string) {
		if zoomSelectSyncing {
			return
		}
		if sel == "fit in preview" {
			zoomFitMode = true
			updateZoom()
			return
		}
		zoomFitMode = false
		s := strings.TrimSuffix(sel, "%")
		if val, err := strconv.ParseFloat(s, 64); err == nil {
			zoomLevel = math.Max(math.Min(val/100.0, 16), 1.0/16)
			updateZoom()
		}
	}

	zoomInBtn := widget.NewButton("+", func() {
		zoomFitMode = false
		zoomLevel = math.Min(zoomLevel*1.25, 16)
		updateZoom()
	})
	zoomOutBtn := widget.NewButton("-", func() {
		zoomFitMode = false
		zoomLevel = math.Max(zoomLevel/1.25, 1.0/16)
		updateZoom()
	})

	previewHeader := container.NewVBox(
		container.NewHBox(layout.NewSpacer(), statsLabel, saveBtn, sendToExamineBtn),
		mosaicHistogram,
	)
	previewFooter := container.NewHBox(
		layout.NewSpacer(),
		widget.NewLabel("Black"),
		blackEntry,
		zoomOutBtn,
		zoomSelect,
		zoomInBtn,
		widget.NewLabel("White"),
		whiteEntry,
		layout.NewSpacer(),
	)

	// Default to "fit in preview" on startup.
	zoomFitMode = true
	zoomSelect.SetSelected("fit in preview")

	rebuildOffsetControls()
	updateStatus()

	// ---- Project save/load -----------------------------------------------

	saveMosaicProject := func() {
		proj := models.MosaicProject{
			DrizzleSettings:      state.drizzleSettings,
			DrizzleSettingsSet:   state.drizzleSettingsSet,
			AlignmentSettings:    state.alignmentSettings,
			AlignmentSettingsSet: state.alignmentSettingsSet,
			SkysubSettings:       state.skysubSettings,
			SkysubSettingsSet:    state.skysubSettingsSet,
			ActiveFilter:         activeFilter,
		}
		for _, inp := range state.inputs {
			mis := models.MosaicInputState{
				Path:         inp.Path,
				SCIExt:       inp.SCIExt,
				OffsetX:      inp.OffsetX,
				OffsetY:      inp.OffsetY,
				HasTransform: inp.HasManualTransform,
				Locked:       inp.OffsetLocked,
			}
			if inp.HasManualTransform {
				t := inp.ManualTransform
				mis.TransformA, mis.TransformB, mis.TransformC = t.A, t.B, t.C
				mis.TransformD, mis.TransformE, mis.TransformF = t.D, t.E, t.F
			}
			proj.Inputs = append(proj.Inputs, mis)
		}
		if state.referenceInput != nil {
			proj.ReferencePath = state.referenceInput.Path
			proj.ReferenceSCIExt = state.referenceInput.SCIExt
		}
		fd := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()
			if filepath.Ext(path) == "" {
				path += ".json"
			}
			data, jsonErr := json.MarshalIndent(proj, "", "  ")
			if jsonErr != nil {
				dialog.ShowError(jsonErr, win)
				return
			}
			if writeErr := os.WriteFile(path, data, 0644); writeErr != nil {
				dialog.ShowError(writeErr, win)
				return
			}
			lastProjectName = filepath.Base(path)
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			dialog.ShowInformation("Saved", "Mosaic project saved.", win)
		}, win)
		name := lastProjectName
		if name == "" {
			name = "mosaic_project.json"
			if activeFilter != "" {
				name = activeFilter + "_project.json"
			}
		}
		fd.SetFileName(name)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
		configureLastDir(fd)
		fd.Show()
	}

	loadMosaicProject := func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			r.Close()
			lastProjectName = filepath.Base(path)
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				dialog.ShowError(readErr, win)
				return
			}
			var proj models.MosaicProject
			if jsonErr := json.Unmarshal(data, &proj); jsonErr != nil {
				dialog.ShowError(jsonErr, win)
				return
			}

			state.drizzleSettings = proj.DrizzleSettings
			state.drizzleSettingsSet = proj.DrizzleSettingsSet
			state.alignmentSettings = proj.AlignmentSettings
			state.alignmentSettingsSet = proj.AlignmentSettingsSet
			state.skysubSettings = proj.SkysubSettings
			state.skysubSettingsSet = proj.SkysubSettingsSet
			if proj.ActiveFilter != "" {
				activeFilter = proj.ActiveFilter
				loadLevelPrefsAndMode(activeFilter)
			}

			// Reload input FITS files.
			progressDialog := dialog.NewCustom("Loading Project", "Reading FITS files...", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()
			go func() {
				var newInputs []mosaic.Input
				var newStatuses []mosaic.InputStatus
				var newRef *mosaic.Input
				refLabelText := "Reference: none"

				for _, mis := range proj.Inputs {
					loadedInputs, loadErr := mosaic.LoadInputsFromPath(mis.Path)
					if loadErr != nil {
						newStatuses = append(newStatuses, mosaic.InputStatus{Path: mis.Path, Status: "failed", Error: loadErr.Error()})
						continue
					}
					matched := false
					for _, loaded := range loadedInputs {
						if mis.SCIExt != 0 && loaded.SCIExt != mis.SCIExt {
							continue
						}
						inp := loaded
						inp.OffsetX = mis.OffsetX
						inp.OffsetY = mis.OffsetY
						inp.HasManualTransform = mis.HasTransform
						inp.OffsetLocked = mis.Locked
						if mis.HasTransform {
							inp.ManualTransform = processing.AffineTransform{
								A: mis.TransformA, B: mis.TransformB, C: mis.TransformC,
								D: mis.TransformD, E: mis.TransformE, F: mis.TransformF,
							}
						}
						newInputs = append(newInputs, inp)
						newStatuses = append(newStatuses, mosaic.InputStatus{Path: mosaic.InputKey(inp), Included: true, Status: "loaded"})
						matched = true
						break
					}
					if !matched {
						newStatuses = append(newStatuses, mosaic.InputStatus{Path: mis.Path, Status: "failed", Error: fmt.Sprintf("missing sci,%d in %s", mis.SCIExt, filepath.Base(mis.Path))})
					}
				}

				if proj.ReferencePath != "" {
					refInputs, refErr := mosaic.LoadInputsFromPath(proj.ReferencePath)
					if refErr == nil {
						for _, loaded := range refInputs {
							if proj.ReferenceSCIExt != 0 && loaded.SCIExt != proj.ReferenceSCIExt {
								continue
							}
							loaded.ReferenceOnly = true
							newRef = &loaded
							refLabelText = "Reference: " + mosaic.InputLabel(loaded)
							break
						}
					}
				}

				fyne.Do(func() {
					state.inputs = newInputs
					state.statuses = newStatuses
					state.referenceInput = newRef
					refLabel.SetText(refLabelText)
					progressDialog.Hide()
					resetPreview()
					rebuildOffsetControls()
					updateStatus()
				})
			}()
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
		configureLastDir(fd)
		fd.SetView(dialog.ListView)
		fd.Show()
	}

	// ---- Settings menu -----------------------------------------------

	openDrizzleSettings := func() {
		showDrizzleSettingsDialog(win, state.drizzleSettings, func(s models.DrizzleSettings) {
			state.drizzleSettings = s
			state.drizzleSettingsSet = true
		})
	}

	openAlignmentSettings := func() {
		showAlignmentSettingsDialog(win, state.alignmentSettings, func(s models.AlignmentSettings) {
			state.alignmentSettings = s
			state.alignmentSettingsSet = true
		})
	}

	openSkysubSettings := func() {
		showSkysubSettingsDialog(win, state.skysubSettings, func(s models.SkysubSettings) {
			state.skysubSettings = s
			state.skysubSettingsSet = true
		})
	}

	settingsMenu := fyne.NewMenu("Mosaic",
		fyne.NewMenuItem("Load Mosaic Project", loadMosaicProject),
		fyne.NewMenuItem("Save Mosaic Project", saveMosaicProject),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Drizzle Settings", openDrizzleSettings),
		fyne.NewMenuItem("Alignment Settings", openAlignmentSettings),
		fyne.NewMenuItem("Skysub Settings", openSkysubSettings),
	)
	footerBottomPad := canvas.NewRectangle(color.Transparent)
	footerBottomPad.SetMinSize(fyne.NewSize(1, 20))
	previewPane := container.NewBorder(previewHeader, container.NewVBox(previewFooter, footerBottomPad), nil, nil, previewSwap)
	split := container.NewHSplit(container.New(&sidePaddedLayout{20}, leftStack), previewPane)
	split.SetOffset(0.38)
	return split, settingsMenu
}

// buildMosaicPreviewImageWithLevels renders a mosaic result to RGBA using the same
// ApplyStretchParallel pipeline used throughout the rest of the application.
func buildMosaicPreviewImageWithLevels(result *mosaic.Result, black, white, background, peak, scaledPeak float64, mode stretch.Mode) *image.RGBA {
	debuglog.Log(fmt.Sprintf("buildMosaicPreviewImage: start %dx%d", result.Width, result.Height))
	img := &models.LoadedImage{
		HDU: fitsio.HDU{
			Data: fitsio.ImageData{
				Pixels: result.Pixels,
				Width:  result.Width,
				Height: result.Height,
			},
		},
		Mode:       mode,
		Black:      black,
		White:      white,
		Background: background,
		Peak:       peak,
		ScaledPeak: scaledPeak,
	}
	debuglog.Log("buildMosaicPreviewImage: ApplyStretchParallel")
	stretched, mask := processing.ApplyStretchParallel(img)
	if mask == nil {
		mask = make([]byte, len(stretched.Pixels))
	}
	debuglog.Log("buildMosaicPreviewImage: ToGrayRGBA")
	rgba := processing.ToGrayRGBA(stretched, mask)
	debuglog.Log(fmt.Sprintf("buildMosaicPreviewImage: drawInputBorders (%d footprints)", len(result.InputFootprints)))
	drawInputBorders(rgba, result.InputFootprints)
	debuglog.Log("buildMosaicPreviewImage: done")
	return rgba
}

// drawInputBorders draws a 5-pixel pure-white border around each input image
// footprint so the seam between images is visible in the preview.
// corners order: TL, TR, BL, BR (matching mosaic.imageCorners).
func drawInputBorders(img *image.RGBA, footprints [][4][2]float64) {
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for _, fp := range footprints {
		// Draw edges: TL→TR, TR→BR, BR→BL, BL→TL
		pairs := [4][2]int{{0, 1}, {1, 3}, {3, 2}, {2, 0}}
		for _, p := range pairs {
			drawThickLine(img, fp[p[0]], fp[p[1]], 5, white)
		}
	}
}

// drawThickLine draws a line from a to b with the given thickness (in pixels)
// using a simple perpendicular offset approach.
func drawThickLine(img *image.RGBA, a, b [2]float64, thickness int, c color.RGBA) {
	dx := b[0] - a[0]
	dy := b[1] - a[1]
	length := math.Sqrt(dx*dx + dy*dy)
	if length == 0 || math.IsNaN(length) || math.IsInf(length, 0) || length > 1e7 {
		return
	}
	// Perpendicular unit vector.
	px := -dy / length
	py := dx / length
	half := float64(thickness) / 2.0
	steps := int(length) + 1
	for s := 0; s <= steps; s++ {
		t := float64(s) / float64(steps)
		cx := a[0] + t*dx
		cy := a[1] + t*dy
		for d := -half; d <= half; d += 0.5 {
			ix := int(math.Round(cx + d*px))
			iy := int(math.Round(cy + d*py))
			if ix >= 0 && iy >= 0 && ix < img.Bounds().Max.X && iy < img.Bounds().Max.Y {
				img.SetRGBA(ix, iy, c)
			}
		}
	}
}

// centroidNearPreview finds the flux-weighted centroid of the nearest bright
// point source within searchRadius pixels of (x, y), operating on the R channel
// of the stretched preview image. Because the preview has already been
// background-subtracted and stretched, stars appear as sharp bright peaks and
// the centroid is far more reliable than on raw float32 drizzle data.
func centroidNearPreview(img *image.RGBA, x, y float64, searchRadius int) (float64, float64, bool) {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()

	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	pixel := func(px, py int) int {
		return int(img.RGBAAt(b.Min.X+px, b.Min.Y+py).R)
	}

	cx := int(math.Round(x))
	cy := int(math.Round(y))
	cx = clamp(cx, 0, w-1)
	cy = clamp(cy, 0, h-1)

	// Hill-climb from the click position to the nearest local brightness maximum.
	// This finds the closest bright peak rather than the globally brightest pixel
	// in the search box, so clicking one star won't snap to a brighter nearby star.
	for step := 0; step < 30; step++ {
		best := pixel(cx, cy)
		bx, by := cx, cy
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				nx := clamp(cx+dx, 0, w-1)
				ny := clamp(cy+dy, 0, h-1)
				if v := pixel(nx, ny); v > best {
					best = v
					bx, by = nx, ny
				}
			}
		}
		if bx == cx && by == cy {
			break
		}
		// Stop if we've wandered too far from the original click.
		ddx := bx - int(math.Round(x))
		ddy := by - int(math.Round(y))
		if ddx*ddx+ddy*ddy > searchRadius*searchRadius {
			break
		}
		cx, cy = bx, by
	}

	peakX, peakY := cx, cy
	peakVal := pixel(peakX, peakY)

	// Reject if the peak is too dim to be a real source.
	if peakVal < 16 {
		return x, y, false
	}

	// Flux-weighted centroid in a ±7 pixel window around the peak.
	const hw = 7
	c0x, c1x := peakX-hw, peakX+hw
	c0y, c1y := peakY-hw, peakY+hw
	if c0x < 0 {
		c0x = 0
	}
	if c1x >= w {
		c1x = w - 1
	}
	if c0y < 0 {
		c0y = 0
	}
	if c1y >= h {
		c1y = h - 1
	}

	var sumX, sumY, sumW float64
	for py := c0y; py <= c1y; py++ {
		for px := c0x; px <= c1x; px++ {
			v := float64(img.RGBAAt(b.Min.X+px, b.Min.Y+py).R)
			sumX += float64(px) * v
			sumY += float64(py) * v
			sumW += v
		}
	}
	if sumW == 0 {
		return x, y, false
	}
	return sumX / sumW, sumY / sumW, true
}

func modeNameForMode(m stretch.Mode) string {
	switch m {
	case stretch.Linear:
		return "Linear"
	case stretch.Log:
		return "Log"
	case stretch.Sqrt:
		return "Sqrt"
	case stretch.HistEq:
		return "HistEq"
	default:
		return "Asinh"
	}
}
