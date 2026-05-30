package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
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

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

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
	ws := &mosaicWorkspace{app: app, win: win, state: state, zoomLevel: 1.0, stretchMode: stretch.Asinh}
	// ws.activeFilter is set when a filter batch is loaded; used for default save names.
	// ws.lastProjectName is updated on save/load so the save dialog pre-populates the same name.

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

	// Picker/measure pointers, mode-swap containers, and preview-level state all
	// live on ws (initialized to zero values; ws.zoomLevel and ws.stretchMode are
	// set in the ws literal above).

	mosaicHistogram := canvas.NewRaster(func(w, h int) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := range img.Pix {
			img.Pix[i] = 255
		}
		maxCount := 0
		for _, c := range ws.mosaicBins {
			if c > maxCount {
				maxCount = c
			}
		}
		if maxCount == 0 {
			return img
		}
		for i, c := range ws.mosaicBins {
			x := i * w / len(ws.mosaicBins)
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

	// Mirror create-once widgets onto ws so methods extracted from this
	// constructor can read them. The locals remain in use within the
	// constructor; both refer to the same widget object.
	ws.blackEntry = blackEntry
	ws.whiteEntry = whiteEntry
	ws.bgEntry = bgEntry
	ws.peakEntry = peakEntry
	ws.scaledPeakEntry = scaledPeakEntry
	ws.statusLabel = statusLabel
	ws.saveOffsetsBtn = saveOffsetsBtn
	ws.loadOffsetsBtn = loadOffsetsBtn
	ws.preview = preview
	ws.statsLabel = statsLabel
	ws.saveBtn = saveBtn
	ws.sendToExamineBtn = sendToExamineBtn
	ws.refLabel = refLabel
	ws.mosaicHistogram = mosaicHistogram
	ws.offsetControls = offsetControls
	ws.offsetScroll = offsetScroll
	ws.offsetHeader = offsetHeader

	// ---- Star selection mode ------------------------------------------------

	starCountLabel := widget.NewLabel("Selected: 0 / 10 stars")
	ws.starCountLabel = starCountLabel

	clearStarsBtn := widget.NewButton("Clear Stars", func() {
		if ws.activePicker != nil {
			ws.activePicker.ClearStars()
		}
	})

	applyStarsBtn := widget.NewButton("Apply", nil)
	cancelStarsBtn := widget.NewButton("Cancel", nil)

	saveStarsBtn := widget.NewButton("Save Stars...", func() {
		if ws.activePicker == nil || len(ws.activePicker.Stars) == 0 {
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
			data, jsonErr := json.MarshalIndent(starFile{Stars: ws.pickerToRefPixels(ws.activePicker.Stars)}, "", "  ")
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
		if ws.activeFilter != "" {
			starFileName = ws.activeFilter + "_ref_stars.json"
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
			if ws.activePicker == nil {
				dialog.ShowInformation("Not in Star Mode", "Open the Select Stars dialog before loading.", win)
				return
			}
			ws.activePicker.Stars = ws.refPixelsToPicker(sf.Stars)
			if ws.activePicker.OnChanged != nil {
				ws.activePicker.OnChanged()
			}
			ws.activePicker.Refresh()
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

	applyStarsBtn.OnTapped = func() {
		if ws.activePicker == nil || len(ws.activePicker.Stars) == 0 {
			dialog.ShowInformation("No Stars Selected", "Click on at least one star in the image before applying.", win)
			return
		}
		if len(state.inputs) < 2 {
			ws.exitStarMode()
			return
		}
		// If stars were picked on the drizzled mosaic, convert from mosaic pixel
		// space back to inputs[0] reference pixel space before alignment.
		refStars := make([]processing.Star, len(ws.activePicker.Stars))
		src := ws.starModeRefResult // capture before exitStarMode clears it
		for i, s := range ws.activePicker.Stars {
			if src != nil && src.Scale > 0 {
				refStars[i] = processing.Star{
					X: s.X/src.Scale + src.OriginX,
					Y: s.Y/src.Scale + src.OriginY,
				}
			} else {
				refStars[i] = s
			}
		}
		ws.exitStarMode()

		progressDialog := dialog.NewCustom("Aligning By Selected Stars", "Matching selected stars across images...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()
		go func() {
			if state.alignmentSettings.DebugAlignment {
				defer installAlignmentDebugHook(win)()
			}
			alignInputs := ws.inputsWithRef()
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
						label := fmt.Sprintf("%s  X: %.2f  Y: %.2f  RotÃƒÆ’Ã¢â‚¬Å¡Ãƒâ€šÃ‚Â°: %.4f", name, r.result.OffsetX, r.result.OffsetY, rot)
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
					ws.rebuildOffsetControls()
					ws.updateStatus()
					go ws.buildDrizzlePreview()
				})
				scroll := container.NewVScroll(content)
				scroll.SetMinSize(fyne.NewSize(520, 200))
				d = dialog.NewCustom("Star Alignment Results", "Dismiss", container.NewVBox(scroll, applyBtn), win)
				d.Show()
			})
		}()
	}

	cancelStarsBtn.OnTapped = func() {
		ws.exitStarMode()
	}

	// ---- Measure mode -------------------------------------------------------

	measureStatusLabel := widget.NewLabel("Click point A on the image.")
	measureStatusLabel.Wrapping = fyne.TextWrapWord

	cancelMeasureBtn := widget.NewButton("Cancel", nil)
	measureCentroidCheck := widget.NewCheck("Snap to centroid", nil)
	measureCentroidCheck.SetChecked(true)
	ws.measureStatusLabel = measureStatusLabel
	ws.measureCentroidCheck = measureCentroidCheck

	measurePanel := container.NewVBox(
		widget.NewLabel("Measure Distance"),
		widget.NewSeparator(),
		widget.NewLabel("Click two points on the image.\nPress Escape to cancel."),
		measureCentroidCheck,
		widget.NewSeparator(),
		measureStatusLabel,
		cancelMeasureBtn,
	)

	cancelMeasureBtn.OnTapped = func() {
		ws.exitMeasureMode()
	}

	// ---- File loading -------------------------------------------------------

	loadBtn := widget.NewButton("Add FITS", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			r.Close()
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			ws.loadPaths([]string{path}, "Loading FITS")
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		ws.configureLastDir(fd)
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
					filterSelect := NewSafeSelect(options, nil)

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
						ws.activeFilter = selected
						ws.loadLevelPrefsAndMode(ws.activeFilter)
						var paths []string
						for _, fc := range fileChecks {
							if fc.checked {
								paths = append(paths, fc.path)
							}
						}
						if len(paths) == 0 {
							return
						}
						ws.loadPaths(paths, "Loading Filter Batch")
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
		ws.enterMeasureMode()
	})

	starAlignBtn := widget.NewButton("Align By Stars", func() {
		if len(state.inputs) < 2 {
			dialog.ShowInformation("Missing Inputs", "Load at least two FITS files before star alignment.", win)
			return
		}
		pt := newProgressTracker("Aligning By Stars", "Refining per-image offsets from stars in the shared overlap...", win)
		progressDialog := pt.dialog
		go func() {
			if state.alignmentSettings.DebugAlignment {
				defer installAlignmentDebugHook(win)()
			}
			alignInputs := ws.inputsWithRef()
			numRefs := state.alignmentSettings.NumRefs
			if numRefs < 1 {
				numRefs = 1
			}
			results, err := mosaic.AlignInputsByStarsWithMode(alignInputs, numRefs, mosaic.AlignmentMode(state.alignmentSettings.AlignmentMode), state.alignmentSettings.SearchRadiusArcsec, mosaic.AlignProgress{
				Progress: func(done, total int) { pt.progress("Aligning", done, total) },
				Ctx:      pt.ctx,
			})
			if errors.Is(err, mosaic.ErrCancelled) {
				debuglog.Log("starAlign: cancelled by user")
				fyne.Do(func() { progressDialog.Hide() })
				return
			}

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
						label := fmt.Sprintf("%s  X: %.2f  Y: %.2f  RotÃƒÆ’Ã¢â‚¬Å¡Ãƒâ€šÃ‚Â°: %.4f", name, r.result.OffsetX, r.result.OffsetY, rot)
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
					ws.rebuildOffsetControls()
					ws.updateStatus()
					go ws.buildDrizzlePreview()
				})
				scroll := container.NewVScroll(content)
				scroll.SetMinSize(fyne.NewSize(520, 200))
				d = dialog.NewCustom("Star Alignment Results", "Dismiss", container.NewVBox(scroll, applyBtn), win)
				d.Show()
			})
		}()
	})

	selectStarsBtn := widget.NewButton("Select Stars...", func() {
		ws.enterStarMode()
	})

	buildBtn := widget.NewButton("Build Drizzle Preview", func() {
		if len(state.inputs) == 0 {
			dialog.ShowInformation("Missing Inputs", "Add one or more FITS files first.", win)
			return
		}
		if !state.drizzleSettingsSet {
			showDrizzleSettingsDialog(win, state.drizzleSettings, func(s models.DrizzleSettings) {
				state.drizzleSettings = s
				state.drizzleSettingsSet = true
				go ws.buildDrizzlePreview()
			})
			return
		}
		go ws.buildDrizzlePreview()
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
		if ws.activeFilter != "" {
			drizzleName = ws.activeFilter + "_drizzle.fits"
		}
		save.SetFileName(drizzleName)
		save.SetFilter(storage.NewExtensionFileFilter([]string{".fits"}))
		save.Show()
	}

	saveOffsetsBtn.OnTapped = func() {
		filter, dir, ok := ws.currentFilterAndDir()
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
		filter, dir, ok := ws.currentFilterAndDir()
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
		ws.resetPreview()
		ws.rebuildOffsetControls()
		ws.updateStatus()
		dialog.ShowInformation("Loaded", fmt.Sprintf("Applied %d saved offsets from %s.", applied, filepath.Base(path)), win)
	}

	clearOffsetsBtn := widget.NewButton("Clear Offsets", func() {
		for i := range state.inputs {
			state.inputs[i].OffsetX = 0
			state.inputs[i].OffsetY = 0
			state.inputs[i].ManualTransform = processing.IdentityTransform()
			state.inputs[i].HasManualTransform = false
		}
		ws.resetPreview()
		ws.rebuildOffsetControls()
	})
	clearOffsetsBtn.Importance = widget.DangerImportance

	clearBtn := widget.NewButton("Clear", func() {
		state.inputs = nil
		state.statuses = nil
		ws.activeFilter = ""
		ws.levelsSet = false
		blackEntry.SetValue(0)
		whiteEntry.SetValue(1)
		bgEntry.SetValue(0)
		peakEntry.SetValue(1000)
		scaledPeakEntry.SetValue(1000)
		ws.updateStatus()
		ws.resetPreview()
		ws.rebuildOffsetControls()
	})
	clearBtn.Importance = widget.DangerImportance

	statusScroll := container.NewVScroll(statusLabel)
	statusScroll.SetMinSize(fyne.NewSize(260, 160))

	// Level controls form.
	modeSelect := NewSafeSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq"}, func(s string) {
		switch s {
		case "Linear":
			ws.stretchMode = stretch.Linear
		case "Log":
			ws.stretchMode = stretch.Log
		case "Sqrt":
			ws.stretchMode = stretch.Sqrt
		case "HistEq":
			ws.stretchMode = stretch.HistEq
		default:
			ws.stretchMode = stretch.Asinh
		}
	})
	modeSelect.SetSelected("Asinh")
	ws.modeSelect = modeSelect
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
			ws.autoLevels(state.result.Pixels)
			ws.applyLevelsToPreview()
		} else if ws.starModeRefResult != nil {
			ws.autoLevels(ws.starModeRefResult.Pixels)
			ws.applyLevelsToPreview()
		}
	})
	applyLevelsBtn := widget.NewButton("Apply Values", func() {
		ws.applyLevelsToPreview()
		ws.saveLevelPrefs()
	})

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
			ws.resetPreview()
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		ws.configureLastDir(fd)
		fd.SetView(dialog.ListView)
		fd.Show()
	})

	clearRefBtn := widget.NewButton("Clear Reference", func() {
		state.referenceInput = nil
		refLabel.SetText("Reference: none")
		ws.resetPreview()
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
	ws.controlsScroll = container.NewVScroll(container.NewBorder(nil, nil, nil, controlsRightPad, controls))
	ws.controlsScroll.SetMinSize(fyne.NewSize(320, 220))

	ws.starPanelScroll = container.NewVScroll(starPanel)
	ws.starPanelScroll.SetMinSize(fyne.NewSize(320, 220))
	ws.starPanelScroll.Hide()

	ws.measurePanelScroll = container.NewVScroll(measurePanel)
	ws.measurePanelScroll.SetMinSize(fyne.NewSize(320, 220))
	ws.measurePanelScroll.Hide()

	ws.leftStack = container.NewStack(ws.controlsScroll, ws.starPanelScroll, ws.measurePanelScroll)

	ws.previewScroll = container.NewScroll(preview)
	ws.pickerScroll = container.NewScroll(widget.NewLabel(""))
	ws.previewSwap = container.NewStack(ws.previewScroll)

	// Zoom controls for the preview pane header.
	zoomPresets := []string{"fit in preview", "6%", "12%", "25%", "50%", "75%", "100%", "150%", "200%", "300%", "400%"}
	zoomSelect := NewSafeSelect(zoomPresets, nil)
	zoomCustomEntry := widget.NewEntry()
	zoomCustomEntry.SetPlaceHolder("custom %")
	zoomCustomEntry.Resize(fyne.NewSize(70, zoomCustomEntry.MinSize().Height))
	ws.zoomPresets = zoomPresets
	ws.zoomSelect = zoomSelect
	ws.zoomCustomEntry = zoomCustomEntry
	// ws.zoomCustomOption tracks a custom option added to the dropdown.
	// ws.zoomSelectSyncing guards against re-entrant OnChanged.

	zoomCustomEntry.OnSubmitted = func(_ string) { ws.applyCustomZoom() }

	zoomSelect.OnChanged = func(sel string) {
		if ws.zoomSelectSyncing {
			return
		}
		if sel == "fit in preview" {
			ws.zoomFitMode = true
			ws.updateZoom()
			return
		}
		ws.zoomFitMode = false
		s := strings.TrimSuffix(sel, "%")
		if val, err := strconv.ParseFloat(s, 64); err == nil {
			ws.zoomLevel = math.Max(math.Min(val/100.0, 16), 1.0/16)
			ws.updateZoom()
		}
	}

	zoomInBtn := widget.NewButton("+", func() {
		ws.zoomFitMode = false
		ws.zoomLevel = math.Min(ws.zoomLevel*1.25, 16)
		ws.updateZoom()
	})
	zoomOutBtn := widget.NewButton("-", func() {
		ws.zoomFitMode = false
		ws.zoomLevel = math.Max(ws.zoomLevel/1.25, 1.0/16)
		ws.updateZoom()
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
	ws.zoomFitMode = true
	zoomSelect.SetSelected("fit in preview")

	ws.rebuildOffsetControls()
	ws.updateStatus()

	// ---- Project save/load -----------------------------------------------

	// ---- Settings menu -----------------------------------------------

	settingsMenu := fyne.NewMenu("Mosaic",
		fyne.NewMenuItem("Load Mosaic Project", ws.loadMosaicProject),
		fyne.NewMenuItem("Save Mosaic Project", ws.saveMosaicProject),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Drizzle Settings", ws.openDrizzleSettings),
		fyne.NewMenuItem("Alignment Settings", ws.openAlignmentSettings),
		fyne.NewMenuItem("Skysub Settings", ws.openSkysubSettings),
	)
	footerBottomPad := canvas.NewRectangle(color.Transparent)
	footerBottomPad.SetMinSize(fyne.NewSize(1, 20))
	previewPane := container.NewBorder(previewHeader, container.NewVBox(previewFooter, footerBottomPad), nil, nil, ws.previewSwap)
	split := container.NewHSplit(container.New(&sidePaddedLayout{20}, ws.leftStack), previewPane)
	split.SetOffset(0.38)
	return split, settingsMenu
}
