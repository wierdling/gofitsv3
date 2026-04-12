package ui

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

type mosaicState struct {
	inputs      []mosaic.Input
	statuses    []mosaic.InputStatus
	result      *mosaic.Result
	scale       float64
	clean       bool
	savePreview bool
}

func newMosaicWorkspace(app fyne.App, win fyne.Window) fyne.CanvasObject {
	state := &mosaicState{scale: 1.0}
	activeFilter := "" // set when a filter batch is loaded; used for default save names

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
	saveBtn := widget.NewButton("Save Drizzle FITS", func() {})
	saveBtn.Disable()
	saveOffsetsBtn := widget.NewButton("Save Offsets", func() {})
	saveOffsetsBtn.Disable()
	loadOffsetsBtn := widget.NewButton("Load Offsets", func() {})
	loadOffsetsBtn.Disable()

	// Active star picker (non-nil only while in star-selection mode).
	var activePicker *starPickerWidget

	// Containers swapped during star-selection mode – set after controls are built.
	var leftStack *fyne.Container
	var previewSwap *fyne.Container
	var previewScroll *container.Scroll
	var pickerScroll *container.Scroll
	var controlsScroll *container.Scroll
	var starPanelScroll *container.Scroll

	// ---- Preview stretch levels -----------------------------------------------

	zoomLevel := 1.0
	levelsSet := false
	var starModeRefResult *mosaic.Result
	stretchMode := stretch.Asinh

	blackEntry := widget.NewEntry()
	whiteEntry := widget.NewEntry()
	bgEntry := widget.NewEntry()
	peakEntry := widget.NewEntry()
	scaledPeakEntry := widget.NewEntry()

	blackEntry.SetText("0.0000")
	whiteEntry.SetText("1.0000")
	bgEntry.SetText("0.0000")
	peakEntry.SetText("1000.0")
	scaledPeakEntry.SetText("1000.0")

	parseLevelEntries := func() (black, white, bg, peak, scaledPeak float64) {
		black, _ = strconv.ParseFloat(strings.TrimSpace(blackEntry.Text), 64)
		white, _ = strconv.ParseFloat(strings.TrimSpace(whiteEntry.Text), 64)
		bg, _ = strconv.ParseFloat(strings.TrimSpace(bgEntry.Text), 64)
		peak, _ = strconv.ParseFloat(strings.TrimSpace(peakEntry.Text), 64)
		scaledPeak, _ = strconv.ParseFloat(strings.TrimSpace(scaledPeakEntry.Text), 64)
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
		minV, maxV := processing.AutoLevels(pixels)
		blackEntry.SetText(fmt.Sprintf("%.4f", minV))
		whiteEntry.SetText(fmt.Sprintf("%.4f", maxV))
		bgEntry.SetText(fmt.Sprintf("%.4f", minV))
		peakEntry.SetText(fmt.Sprintf("%.4f", maxV))
		scaledPeakEntry.SetText(fmt.Sprintf("%.4f", maxV))
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
			Black:      blackEntry.Text,
			White:      whiteEntry.Text,
			Background: bgEntry.Text,
			Peak:       peakEntry.Text,
			ScaledPeak: scaledPeakEntry.Text,
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
	}
	var rebuildOffsetControls func()

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
			type starFile struct {
				Stars []processing.Star `json:"stars"`
			}
			data, jsonErr := json.MarshalIndent(starFile{Stars: activePicker.Stars}, "", "  ")
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
			activePicker.Stars = sf.Stars
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
			statsLabel.SetText(fmt.Sprintf("Mean: %.4f | Std: %.4f | Size: %dx%d", stats.Mean, stats.Std, state.result.Width, state.result.Height))
		} else {
			statsLabel.SetText("Mean: -- | Std: -- | Size: --")
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
			results, err := mosaic.AlignInputsBySelectedStars(state.inputs, refStars)
			progressDialog.Hide()
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			for i := range results {
				if i >= len(state.inputs) || i >= len(state.statuses) {
					continue
				}
				if results[i].Applied {
					state.inputs[i].OffsetX = results[i].OffsetX
					state.inputs[i].OffsetY = results[i].OffsetY
					if i == 0 {
						state.statuses[i].Status = "reference"
					} else {
						state.statuses[i].Status = "star aligned"
					}
					state.statuses[i].Error = ""
				} else if results[i].Error != "" {
					state.statuses[i].Status = "star align failed"
					state.statuses[i].Error = results[i].Error
				}
			}
			resetPreview()
			rebuildOffsetControls()
			updateStatus()
		}()
	}

	cancelStarsBtn.OnTapped = func() {
		exitStarMode()
	}

	// ---- File loading -------------------------------------------------------

	loadPaths := func(paths []string, title string) {
		progressDialog := dialog.NewCustom(title, "Reading FITS data...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			newInputs := make([]mosaic.Input, 0, len(paths))
			newStatuses := make([]mosaic.InputStatus, 0, len(paths))
			warnings := 0

			for _, path := range paths {
				input, err := mosaic.LoadInputFromPath(path)
				if err != nil {
					newStatuses = append(newStatuses, mosaic.InputStatus{Path: path, Status: "failed", Error: err.Error()})
					continue
				}

				newInputs = append(newInputs, input)
				status := mosaic.InputStatus{Path: path, Included: true, Status: "loaded"}
				if !mosaic.LooksLikeFLC(path) {
					status.Status = "loaded (warning: not _flc)"
					warnings++
				}

				newStatuses = append(newStatuses, status)
			}

			offsetMessages := applyAutoLoadedOffsets(newInputs)
			for i := range newInputs {
				if i < len(newStatuses) && (newInputs[i].OffsetX != 0 || newInputs[i].OffsetY != 0) && !strings.Contains(newStatuses[i].Status, "failed") {
					newStatuses[i].Status = "loaded offsets"
				}
			}

			progressDialog.Hide()
			state.inputs = append(state.inputs, newInputs...)
			state.statuses = append(state.statuses, newStatuses...)
			resetPreview()
			rebuildOffsetControls()
			updateStatus()

			messages := make([]string, 0, len(offsetMessages)+1)
			if warnings > 0 {
				messages = append(messages, "Some loaded files are not standard _flc inputs. They were kept, but this workflow is tuned for HST _flc science files.")
			}
			messages = append(messages, offsetMessages...)
			if len(messages) > 0 {
				dialog.ShowInformation("Mosaic Load", strings.Join(messages, "\n"), win)
			}
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
		if len(state.inputs) == 0 {
			offsetControls.Add(widget.NewLabel("No FITS files loaded."))
			offsetControls.Refresh()
			return
		}
		for idx := range state.inputs {
			name := filepath.Base(state.inputs[idx].Path)
			xEntry := widget.NewEntry()
			xEntry.SetText(fmt.Sprintf("%.2f", state.inputs[idx].OffsetX))
			yEntry := widget.NewEntry()
			yEntry.SetText(fmt.Sprintf("%.2f", state.inputs[idx].OffsetY))
			applyBtn := widget.NewButton("Apply", func(index int, xBox, yBox *widget.Entry) func() {
				return func() {
					xVal, errX := strconv.ParseFloat(strings.TrimSpace(xBox.Text), 64)
					yVal, errY := strconv.ParseFloat(strings.TrimSpace(yBox.Text), 64)
					if errX != nil || errY != nil {
						dialog.ShowInformation("Invalid Offset", "Offsets must be valid numbers in pixels.", win)
						return
					}
					state.inputs[index].OffsetX = xVal
					state.inputs[index].OffsetY = yVal
					if index < len(state.statuses) && state.statuses[index].Status == "loaded" {
						state.statuses[index].Status = "manual offset set"
					}
					resetPreview()
					updateStatus()
				}
			}(idx, xEntry, yEntry))
			if idx == 0 {
				xEntry.Disable()
				yEntry.Disable()
				applyBtn.Disable()
				name += " (reference)"
			}
			row := container.NewBorder(nil, nil, widget.NewLabel(name), applyBtn,
				container.NewGridWithColumns(4,
					widget.NewLabel("X"), xEntry,
					widget.NewLabel("Y"), yEntry,
				),
			)
			offsetControls.Add(row)
			offsetControls.Add(widget.NewSeparator())
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

	scaleEntry := widget.NewEntry()
	scaleEntry.SetText("1.0")
	scaleEntry.OnChanged = func(s string) {
		var val float64
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%f", &val); err == nil && val > 0 {
			state.scale = val
		}
	}

	cleanCheck := widget.NewCheck("Clean cosmic rays before drizzle", func(v bool) {
		state.clean = v
	})
	savePreviewCheck := widget.NewCheck("Save Preview", func(v bool) {
		state.savePreview = v
	})

	starAlignBtn := widget.NewButton("Align By Stars", func() {
		if len(state.inputs) < 2 {
			dialog.ShowInformation("Missing Inputs", "Load at least two FITS files before star alignment.", win)
			return
		}
		progressDialog := dialog.NewCustom("Aligning By Stars", "Refining per-image offsets from stars in the shared overlap...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()
		go func() {
			results, err := mosaic.AlignInputsByStars(state.inputs)
			progressDialog.Hide()
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			for i := range results {
				if i >= len(state.inputs) || i >= len(state.statuses) {
					continue
				}
				if results[i].Applied {
					state.inputs[i].OffsetX = results[i].OffsetX
					state.inputs[i].OffsetY = results[i].OffsetY
					if i == 0 {
						state.statuses[i].Status = "reference"
					} else {
						state.statuses[i].Status = "star aligned"
					}
					state.statuses[i].Error = ""
				} else if results[i].Error != "" {
					state.statuses[i].Status = "star align failed"
					state.statuses[i].Error = results[i].Error
				}
			}
			resetPreview()
			rebuildOffsetControls()
			updateStatus()
		}()
	})

	selectStarsBtn := widget.NewButton("Select Stars...", func() {
		enterStarMode()
	})

	buildBtn := widget.NewButton("Build Drizzle Preview", func() {
		if len(state.inputs) == 0 {
			dialog.ShowInformation("Missing Inputs", "Add one or more FITS files first.", win)
			return
		}

		progressDialog := dialog.NewCustom("Building Drizzle Preview", "Aligning, cleaning, and drizzling selected inputs...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			result, err := mosaic.Build(state.inputs, mosaic.Options{
				Scale:           state.scale,
				CleanCosmicRays: state.clean,
			})
			progressDialog.Hide()
			if err != nil {
				dialog.ShowError(err, win)
				return
			}

			state.result = result
			state.statuses = result.Inputs
			saveBtn.Enable()
			rebuildOffsetControls()
			updateStatus()
			if !levelsSet {
				autoLevels(result.Pixels)
			}
			black, white, bg, peak, scaledPeak := parseLevelEntries()
			img := buildMosaicPreviewImageWithLevels(result, black, white, bg, peak, scaledPeak, stretchMode)
			preview.Image = img
			preview.Refresh()
			stats := histogram.Compute(result.Pixels)
			statsLabel.SetText(fmt.Sprintf("Mean: %.4f | Std: %.4f | Size: %dx%d", stats.Mean, stats.Std, result.Width, result.Height))
			updateZoom()

			if state.savePreview {
				filter, dir, ok := currentFilterAndDir()
				if !ok {
					dialog.ShowInformation("Preview Save Skipped", "Automatic preview save requires the loaded files to come from one directory and one filter.", win)
					return
				}
				previewPath := filepath.Join(dir, filter+"_preview.fits")
				if err := mosaic.SaveResultFITS(previewPath, result); err != nil {
					dialog.ShowError(err, win)
					return
				}
				dialog.ShowInformation("Preview Saved", fmt.Sprintf("Saved preview FITS to %s.", filepath.Base(previewPath)), win)
			}
		}()
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
			dialog.ShowInformation("Saved", "Offset file saved successfully.", win)
		}, win)
		save.SetFileName(mosaic.OffsetFileName(filter))
		save.SetFilter(storage.NewExtensionFileFilter([]string{".txt"}))
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
		}
		resetPreview()
		rebuildOffsetControls()
	})

	clearBtn := widget.NewButton("Clear", func() {
		state.inputs = nil
		state.statuses = nil
		activeFilter = ""
		levelsSet = false
		blackEntry.SetText("0.0000")
		whiteEntry.SetText("1.0000")
		bgEntry.SetText("0.0000")
		peakEntry.SetText("1000.0")
		scaledPeakEntry.SetText("1000.0")
		updateStatus()
		resetPreview()
		rebuildOffsetControls()
	})

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
	levelsForm := widget.NewForm(
		widget.NewFormItem("Mode", modeSelect),
		widget.NewFormItem("Black", blackEntry),
		widget.NewFormItem("White", whiteEntry),
		widget.NewFormItem("Background", bgEntry),
		widget.NewFormItem("Peak", peakEntry),
		widget.NewFormItem("Scaled Peak", scaledPeakEntry),
	)
	autoLevelsBtn := widget.NewButton("Auto Levels", func() {
		if state.result != nil {
			autoLevels(state.result.Pixels)
			applyLevelsToPreview()
		} else if starModeRefResult != nil {
			autoLevels(starModeRefResult.Pixels)
			applyLevelsToPreview()
		}
	})
	applyLevelsBtn := widget.NewButton("Apply", func() {
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
		blackEntry.SetText(sl.Black)
		whiteEntry.SetText(sl.White)
		bgEntry.SetText(sl.Background)
		peakEntry.SetText(sl.Peak)
		scaledPeakEntry.SetText(sl.ScaledPeak)
		if sl.Mode != "" {
			modeSelect.SetSelected(sl.Mode)
		}
		levelsSet = true
		return true
	}

	controls := container.NewVBox(
		widget.NewLabel("Mosaic / Drizzle"),
		container.NewGridWithColumns(2, loadBtn, batchBtn),
		widget.NewForm(widget.NewFormItem("Scale", scaleEntry)),
		cleanCheck,
		savePreviewCheck,
		container.NewGridWithColumns(2, starAlignBtn, selectStarsBtn),
		buildBtn,
		saveBtn,
		container.NewGridWithColumns(2, saveOffsetsBtn, loadOffsetsBtn),
		clearOffsetsBtn,
		clearBtn,
		widget.NewSeparator(),
		widget.NewLabel("Per-Image Offsets (pixels)"),
		offsetScroll,
		widget.NewSeparator(),
		widget.NewLabel("Input Status"),
		statusScroll,
		widget.NewSeparator(),
		widget.NewLabel("Preview Levels"),
		levelsForm,
		container.NewGridWithColumns(2, autoLevelsBtn, applyLevelsBtn),
	)

	// Now assign all the variables that enterStarMode/exitStarMode need.
	controlsScroll = container.NewVScroll(controls)
	controlsScroll.SetMinSize(fyne.NewSize(320, 220))

	starPanelScroll = container.NewVScroll(starPanel)
	starPanelScroll.SetMinSize(fyne.NewSize(320, 220))
	starPanelScroll.Hide()

	leftStack = container.NewStack(controlsScroll, starPanelScroll)

	previewScroll = container.NewScroll(preview)
	pickerScroll = container.NewScroll(widget.NewLabel(""))
	previewSwap = container.NewStack(previewScroll)

	// Zoom controls for the preview pane header.
	zoomLabel := widget.NewLabel("100%")
	updateZoom = func() {
		pct := int(zoomLevel * 100)
		zoomLabel.SetText(fmt.Sprintf("%d%%", pct))
		if activePicker != nil {
			activePicker.SetZoom(zoomLevel)
			pickerScroll.Refresh()
		} else {
			w := float32(600 * zoomLevel)
			h := float32(500 * zoomLevel)
			preview.SetMinSize(fyne.NewSize(w, h))
			preview.Refresh()
			previewScroll.Refresh()
		}
	}
	zoomInBtn := widget.NewButton("+", func() {
		zoomLevel = math.Min(zoomLevel*1.5, 16)
		updateZoom()
	})
	zoomOutBtn := widget.NewButton("-", func() {
		zoomLevel = math.Max(zoomLevel/1.5, 1.0/1.5)
		updateZoom()
	})
	zoomResetBtn := widget.NewButton("1:1", func() {
		zoomLevel = 1.0
		updateZoom()
	})
	zoomRow := container.NewBorder(nil, nil,
		container.NewHBox(widget.NewLabel("Zoom:"), zoomOutBtn, zoomLabel, zoomInBtn, zoomResetBtn),
		nil,
		statsLabel,
	)

	rebuildOffsetControls()
	updateStatus()

	previewPane := container.NewBorder(zoomRow, nil, nil, nil, previewSwap)
	split := container.NewHSplit(leftStack, previewPane)
	split.SetOffset(0.38)
	return split
}

// buildMosaicPreviewImageWithLevels renders a mosaic result to RGBA using the same
// ApplyStretchParallel pipeline used throughout the rest of the application.
func buildMosaicPreviewImageWithLevels(result *mosaic.Result, black, white, background, peak, scaledPeak float64, mode stretch.Mode) *image.RGBA {
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
	stretched, mask := processing.ApplyStretchParallel(img)
	if mask == nil {
		mask = make([]byte, len(stretched.Pixels))
	}
	return processing.ToGrayRGBA(stretched, mask)
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
