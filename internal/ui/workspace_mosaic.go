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
	"sync"

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

type mosaicState struct {
	inputs      []mosaic.Input
	statuses    []mosaic.InputStatus
	result      *mosaic.Result
	resultName  string
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
	artifactMasks        *models.ArtifactMaskProject
	// exposureNormMode controls per-frame exposure-time normalization applied
	// before drizzle. Defaults to Off so existing behavior is preserved.
	exposureNormMode mosaic.NormalizationMode
}

func newMosaicWorkspace(app fyne.App, win fyne.Window) (fyne.CanvasObject, *fyne.Menu, *fyne.MenuItem, *fyne.MenuItem) {
	state := &mosaicState{drizzleSettings: defaultDrizzleSettings(), alignmentSettings: defaultAlignmentSettings(), skysubSettings: defaultSkysubSettings()}
	ws := &mosaicWorkspace{app: app, win: win, state: state, zoomLevel: 1.0, stretchMode: stretch.Asinh, mtfMidtone: stretch.DefaultMTFMidtone}
	// ws.activeFilter is set when a filter batch is loaded; used for default save names.
	// ws.lastProjectName is updated on save/load so the save dialog pre-populates the same name.

	preview := canvas.NewImageFromImage(blankImg())
	preview.FillMode = canvas.ImageFillContain
	preview.SetMinSize(fyne.NewSize(520, 420))

	statsLabel := widget.NewLabel(mosaicEmptyStatsText())
	statsLabel.TextStyle = fyne.TextStyle{Monospace: true}
	statusLabel := widget.NewLabel("No FITS files loaded.")
	statusLabel.Wrapping = fyne.TextWrapWord
	statusLabel.TextStyle = fyne.TextStyle{Monospace: true}
	offsetControls := container.NewVBox(widget.NewLabel("No FITS files loaded."))
	offsetScroll := container.NewVScroll(offsetControls)
	offsetScroll.SetMinSize(fyne.NewSize(260, 180))
	offsetHeader := container.NewVBox()
	saveBtn := widget.NewButton("Save Drizzle FITS", func() {})
	saveBtn.Importance = widget.HighImportance
	saveBtn.Disable()
	sendToExamineBtn := widget.NewButton("Send to Examine", func() {
		if globalSendToExamine == nil || state.result == nil {
			return
		}
		if globalSendDiagnosticToExamine != nil && state.result.DiagnosticProducts {
			layers := map[string]fitsio.ImageData{}
			if len(state.result.Weights) == state.result.Width*state.result.Height {
				layers["WHT"] = fitsio.ImageData{Width: state.result.Width, Height: state.result.Height, Pixels: state.result.Weights}
			}
			if len(state.result.NContrib) == state.result.Width*state.result.Height {
				layers["NCONTRIB"] = fitsio.ImageData{Width: state.result.Width, Height: state.result.Height, Int32Pixels: state.result.NContrib}
			}
			for i, plane := range state.result.ContextPlanes {
				ctx := make([]int32, len(plane))
				for j, v := range plane {
					ctx[j] = int32(v)
				}
				layers[fmt.Sprintf("CTX%02d", i+1)] = fitsio.ImageData{Width: state.result.Width, Height: state.result.Height, Int32Pixels: ctx}
			}
			if len(state.result.CRMask) == state.result.Width*state.result.Height {
				layers["CRMASK"] = fitsio.ImageData{Width: state.result.Width, Height: state.result.Height, Int32Pixels: state.result.CRMask}
			}
			if len(state.result.DQ) == state.result.Width*state.result.Height {
				layers["DQ"] = fitsio.ImageData{Width: state.result.Width, Height: state.result.Height, Int32Pixels: state.result.DQ}
			}
			if len(state.result.SkyModel) == state.result.Width*state.result.Height {
				layers["SKYMODEL"] = fitsio.ImageData{Width: state.result.Width, Height: state.result.Height, Pixels: state.result.SkyModel}
			}
			if len(state.result.Seam) == state.result.Width*state.result.Height {
				layers["SEAM"] = fitsio.ImageData{Width: state.result.Width, Height: state.result.Height, Pixels: state.result.Seam}
			}
			globalSendDiagnosticToExamine(state.result.Pixels, state.result.Width, state.result.Height, layers)
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
	mtfMidtoneEntry := NewNumberEntry(0.01, 3)

	blackEntry.SetValue(0)
	whiteEntry.SetValue(1)
	bgEntry.SetValue(0)
	peakEntry.SetValue(1000)
	scaledPeakEntry.SetValue(1000)
	mtfMidtoneEntry.SetValue(stretch.DefaultMTFMidtone)

	// Mirror create-once widgets onto ws so methods extracted from this
	// constructor can read them. The locals remain in use within the
	// constructor; both refer to the same widget object.
	ws.blackEntry = blackEntry
	ws.whiteEntry = whiteEntry
	ws.bgEntry = bgEntry
	ws.peakEntry = peakEntry
	ws.scaledPeakEntry = scaledPeakEntry
	ws.mtfMidtoneEntry = mtfMidtoneEntry
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
		sizeFileDialog(fd)
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
		sizeFileDialog(fd)
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

		alignCtx, alignGeneration, started := ws.beginMosaicAlignment()
		if !started {
			dialog.ShowInformation("Star Alignment", "An alignment operation is already running.", win)
			return
		}
		settings := state.alignmentSettings
		go func() {
			var finishOnce sync.Once
			finishOK := false
			finish := func() bool {
				finishOnce.Do(func() { finishOK = ws.finishMosaicAlignment(alignGeneration) })
				return finishOK
			}
			queued := false
			defer func() {
				if !queued {
					finish()
				}
			}()
			pt := newProgressTrackerWithContext("Aligning By Selected Stars", "Matching selected stars across images...", win, alignCtx, func() { ws.cancelMosaicAlignment() })
			if alignCtx.Err() != nil {
				pt.hide()
				return
			}
			// TweakReg modes stream each frame's pixels on demand during
			// alignment, so only legacy warp-based modes require every frame
			// resident up front.
			alignMode := mosaic.AlignmentMode(settings.AlignmentMode)
			if !mosaic.AlignmentStreamsPixels(alignMode) {
				if err := ws.ensureInputPixelsLoaded(); err != nil {
					fyne.Do(func() {
						pt.hide()
						dialog.ShowError(err, win)
					})
					return
				}
			}
			workerSnapshot := ws.alignmentInputSnapshot()
			alignInputs, stateIndices := alignmentWorksetFor(workerSnapshot.inputs, workerSnapshot.statuses, workerSnapshot.reference, settings.NumRefs)
			if len(alignInputs) < 2 {
				fyne.Do(func() {
					pt.hide()
					dialog.ShowInformation("Star Alignment", "All eligible inputs already have saved alignments.", win)
				})
				return
			}
			results, err := mosaic.AlignInputsBySelectedStarsWithModeCtx(alignCtx, alignInputs, refStars, alignmentNumRefsFor(workerSnapshot.reference, settings.NumRefs), alignMode, settings.SearchRadiusArcsec)
			if alignCtx.Err() != nil {
				pt.hide()
				return
			}

			var rows []alignmentResultRow
			if err == nil {
				rows = buildAlignmentResultRowsForStateIndices(results, stateIndices)
				if len(alignInputs) > 0 {
					bindAlignmentResultSnapshot(rows, workerSnapshot)
				}
			}

			queued = true
			fyne.Do(func() {
				finished := finish()
				if alignCtx.Err() != nil || !finished {
					pt.hide()
					return
				}
				pt.hide()
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
					name := mosaic.InputLabel(workerSnapshot.inputs[r.stateIdx])
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
						content.Add(widget.NewLabel(alignmentDiagnosticsText(r.result)))
						diagBtn := widget.NewButton("Diagnostics…", func() {
							showAlignmentDiagnosticsDialog(win, r, name)
						})
						content.Add(diagBtn)
					} else {
						label := fmt.Sprintf("%s  [failed: %s]", name, r.result.Error)
						chk := widget.NewCheck(label, nil)
						chk.Disable()
						checks[i] = chk
						content.Add(chk)
					}
				}

				var d dialog.Dialog
				exportBtn := widget.NewButton("Export CSV...", func() {
					save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, saveErr error) {
						if saveErr != nil || uc == nil {
							return
						}
						path := uc.URI().Path()
						_ = uc.Close()
						if filepath.Ext(path) == "" {
							path += ".csv"
						}
						file, createErr := os.Create(path)
						if createErr != nil {
							dialog.ShowError(createErr, win)
							return
						}
						writeErr := writeAlignmentCSV(file, rows, state.inputs)
						closeErr := file.Close()
						if writeErr != nil {
							dialog.ShowError(writeErr, win)
							return
						}
						if closeErr != nil {
							dialog.ShowError(closeErr, win)
							return
						}
						dialog.ShowInformation("Exported", "Alignment CSV exported successfully.", win)
					}, win)
					name := "alignment_results.csv"
					if ws.activeFilter != "" {
						name = ws.activeFilter + "_alignment_results.csv"
					}
					save.SetFileName(name)
					save.SetFilter(storage.NewExtensionFileFilter([]string{".csv"}))
					save.Show()
				})
				applyBtn := widget.NewButton("Apply", func() {
					saveErr := ws.applyAlignmentRowsAndSave(rows, func(i int) bool {
						return checks[i] != nil && checks[i].Checked
					}, mosaic.MergeSaveAlignmentSidecar)
					d.Hide()
					ws.rebuildOffsetControls()
					ws.updateStatus()
					go ws.buildDrizzlePreview()
					if saveErr != nil {
						dialog.ShowError(saveErr, win)
					}
				})
				scroll := container.NewVScroll(content)
				scroll.SetMinSize(fyne.NewSize(520, 200))
				d = dialog.NewCustom("Star Alignment Results", "Dismiss", container.NewVBox(scroll, container.NewGridWithColumns(2, exportBtn, applyBtn)), win)
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

	loadBtn := widget.NewButton("Add FITS / ASDF", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			r.Close()
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			ws.loadPaths([]string{path}, "Loading FITS")
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts", ".asdf"}))
		ws.configureLastDir(fd)
		fd.SetView(dialog.ListView)
		sizeFileDialog(fd)
		fd.Show()
	})

	directoryBtn := widget.NewButton("Add FITS / ASDF Directory", func() {
		fd := dialog.NewFolderOpen(func(listable fyne.ListableURI, err error) {
			if err != nil || listable == nil {
				return
			}
			dir := listable.Path()
			app.Preferences().SetString("lastDir", dir)
			paths, scanErr := mosaic.DiscoverFITSFiles(dir)
			if scanErr != nil {
				dialog.ShowError(scanErr, win)
				return
			}
			ws.loadPaths(paths, "Loading FITS Directory")
		}, win)
		ws.configureLastDir(fd)
		fd.Resize(fyne.NewSize(640, 480))
		fd.Show()
	})
	ws.directoryBtn = directoryBtn

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
				filesByFilter, scanErr := mosaic.DiscoverFilterFiles(dir)
				fyne.Do(func() {
					progressDialog.Hide()
					if scanErr != nil {
						dialog.ShowError(scanErr, win)
						return
					}

					// Independent, order-independent facets: each constrains the
					// same flattened file list; none cascades into another.
					files := mosaic.AllFilterFiles(filesByFilter)
					filterSelect := NewSafeSelect(mosaic.FilterFacetOptions(files), nil)
					proposalSelect := NewSafeSelect(mosaic.ProposalFacetOptions(files), nil)
					exposureSelect := NewSafeSelect(mosaic.ExposureFacetOptions(files), nil)
					instrumentSelect := NewSafeSelect(mosaic.InstrumentFacetOptions(files), nil)

					// Date range is bounded by the actual observation dates, as a
					// min/max pair of selects (omitted when no DATE-OBS is present).
					dates := mosaic.DateValues(files)
					var dateMinSelect, dateMaxSelect *SafeSelect
					if len(dates) > 0 {
						dateMinSelect = NewSafeSelect(append([]string(nil), dates...), nil)
						dateMaxSelect = NewSafeSelect(append([]string(nil), dates...), nil)
					}

					// typeRadio selects which calibrated product to load (_flc vs _flt).
					var typeRadio *widget.RadioGroup
					productType := func() string {
						if typeRadio == nil {
							return ""
						}
						return strings.TrimPrefix(typeRadio.Selected, ".")
					}
					criteria := func() mosaic.FileCriteria {
						c := mosaic.FileCriteria{
							Filter:      mosaic.FacetValue(filterSelect.Selected),
							ProposalID:  mosaic.FacetValue(proposalSelect.Selected),
							Instrument:  mosaic.FacetValue(instrumentSelect.Selected),
							Exposure:    mosaic.FacetValue(exposureSelect.Selected),
							ProductType: productType(),
						}
						if dateMinSelect != nil {
							c.DateMin = dateMinSelect.Selected
							c.DateMax = dateMaxSelect.Selected
						}
						return c
					}

					// Observation date per path, shown alongside the file name.
					dateByPath := make(map[string]string, len(files))
					for _, f := range files {
						dateByPath[f.Path] = f.DateObs
					}

					var fileChecks []mosaicFilterBatchFileCheck
					var checkBoxes []*widget.Check
					var updatingChecks bool
					checkContainer := container.NewVBox()
					filesScroll := container.NewVScroll(checkContainer)
					filesScroll.SetMinSize(fyne.NewSize(300, 200))
					footprintPreview := newMosaicFilterPreview()
					var previewGeneration uint64
					var previewWindow fyne.Window
					var refreshPreview func()

					filteredFiles := func() []mosaic.FilterFile {
						filter := mosaic.FacetValue(filterSelect.Selected)
						product := productType()
						instrument := mosaic.FacetValue(instrumentSelect.Selected)
						if filter == "" && product == "" && instrument == "" {
							return files
						}
						filtered := make([]mosaic.FilterFile, 0, len(files))
						for _, f := range files {
							if filter != "" && f.Filter != filter {
								continue
							}
							if product != "" && mosaic.ProductType(f.Path) != product {
								continue
							}
							if instrument != "" && f.Instrument != instrument {
								continue
							}
							filtered = append(filtered, f)
						}
						return filtered
					}

					var updating bool

					// upstreamFilteredFiles applies the facets that sit above Filter in
					// the cascade (product type and instrument), so the Filter list and
					// everything below it narrow to the chosen product/instrument.
					upstreamFilteredFiles := func() []mosaic.FilterFile {
						product := productType()
						instrument := mosaic.FacetValue(instrumentSelect.Selected)
						if product == "" && instrument == "" {
							return files
						}
						filtered := make([]mosaic.FilterFile, 0, len(files))
						for _, f := range files {
							if product != "" && mosaic.ProductType(f.Path) != product {
								continue
							}
							if instrument != "" && f.Instrument != instrument {
								continue
							}
							filtered = append(filtered, f)
						}
						return filtered
					}

					setSelectSelection := func(sel *SafeSelect, options []string) {
						oldFacet := mosaic.FacetValue(sel.Selected)
						sel.Options = options
						sel.Refresh()
						if oldFacet != "" {
							for _, opt := range options {
								if mosaic.FacetValue(opt) == oldFacet {
									sel.SetSelected(opt)
									return
								}
							}
						}
						if len(options) > 0 {
							sel.SetSelected(options[0])
						} else {
							sel.SetSelected("")
						}
					}

					updateDependentOptions := func() {
						if updating {
							return
						}
						updating = true
						defer func() { updating = false }()

						// 1. Update filter options based on product type and instrument
						setSelectSelection(filterSelect, mosaic.FilterFacetOptions(upstreamFilteredFiles()))

						// 2. Update dependent selections based on both product type and filter
						filtered := filteredFiles()
						setSelectSelection(proposalSelect, mosaic.ProposalFacetOptions(filtered))
						setSelectSelection(exposureSelect, mosaic.ExposureFacetOptions(filtered))
						if dateMinSelect != nil {
							dates := mosaic.DateValues(filtered)
							dateMinSelect.Options = append([]string(nil), dates...)
							dateMaxSelect.Options = append([]string(nil), dates...)
							dateMinSelect.Refresh()
							dateMaxSelect.Refresh()
							if len(dates) > 0 {
								dateMinSelect.SetSelected(dates[0])
								dateMaxSelect.SetSelected(dates[len(dates)-1])
							} else {
								dateMinSelect.SetSelected("")
								dateMaxSelect.SetSelected("")
							}
						}
					}

					planPreview := func(selected []mosaicFilterBatchPreviewRequest) {
						previewGeneration++
						generation := previewGeneration
						footprintPreview.loading()
						go func() {
							var inputs []mosaic.Input
							loaded := make(map[string]bool, len(selected))
							for _, request := range selected {
								meta, err := mosaic.LoadInputsMetadataFromPath(request.path)
								if err == nil {
									inputs = append(inputs, meta...)
									loaded[request.path] = true
								}
							}
							scale := mosaic.ResolvePreviewScale(inputs, state.drizzleSettings.Scale, state.drizzleSettings.FinalScale)
							groups, width, height, _ := mosaic.PlanFootprintPreview(inputs, scale)
							byPath := make(map[string]mosaic.FootprintPreview, len(groups))
							for _, group := range groups {
								byPath[group.SourcePath] = group
							}
							orderedGroups := make([]mosaic.FootprintPreview, 0, len(selected))
							for _, request := range selected {
								group, ok := byPath[request.path]
								if !loaded[request.path] || !ok {
									group = mosaic.FootprintPreview{SourcePath: request.path, Status: "warning: metadata unavailable", Error: "unable to read WCS metadata"}
								}
								group.SourceNumber = request.sourceNumber
								orderedGroups = append(orderedGroups, group)
							}
							fyne.Do(func() {
								if generation == previewGeneration {
									footprintPreview.show(orderedGroups, width, height)
								}
							})
						}()
					}

					updateSelectedFiles := func() {
						if updating {
							return
						}
						paths := mosaic.MatchFiles(files, criteria())
						fileChecks = make([]mosaicFilterBatchFileCheck, len(paths))
						checkBoxes = make([]*widget.Check, len(paths))
						checkContainer.Objects = nil
						for i, path := range paths {
							i, path := i, path
							fileChecks[i] = mosaicFilterBatchFileCheck{path: path, checked: true}
							label := fmt.Sprintf("%d. %s", i+1, filepath.Base(path))
							if date := dateByPath[path]; date != "" {
								label += "  —  " + date
							}
							chk := widget.NewCheck(label, func(v bool) {
								fileChecks[i].checked = v
								if !updatingChecks && filterPreviewRefreshRequests(previewWindow != nil, false, 1) > 0 && refreshPreview != nil {
									refreshPreview()
								}
							})
							chk.SetChecked(true)
							checkBoxes[i] = chk
							checkContainer.Add(chk)
						}
						checkContainer.Refresh()
						requests := make([]mosaicFilterBatchPreviewRequest, len(paths))
						for i, path := range paths {
							requests[i] = mosaicFilterBatchPreviewRequest{path: path, sourceNumber: i + 1}
						}
						planPreview(requests)
					}

					refreshPreview = func() {
						// Re-run metadata planning for the current list without rebuilding
						// checkboxes, preserving the user's load selection.
						selected := snapshotCheckedPaths(fileChecks)
						planPreview(selected)
					}

					filterSelect.OnChanged = func(string) {
						updateDependentOptions()
						updateSelectedFiles()
					}
					proposalSelect.OnChanged = func(string) { updateSelectedFiles() }
					exposureSelect.OnChanged = func(string) { updateSelectedFiles() }
					instrumentSelect.OnChanged = func(string) {
						updateDependentOptions()
						updateSelectedFiles()
					}
					if dateMinSelect != nil {
						dateMinSelect.OnChanged = func(string) { updateSelectedFiles() }
						dateMaxSelect.OnChanged = func(string) { updateSelectedFiles() }
					}

					hasFLC, hasFLT, hasCal := mosaic.AvailableProductTypes(filesByFilter)
					var typeOptions []string
					if hasFLC {
						typeOptions = append(typeOptions, ".flc")
					}
					if hasFLT {
						typeOptions = append(typeOptions, ".flt")
					}
					if hasCal {
						typeOptions = append(typeOptions, ".cal")
					}
					typeRadio = widget.NewRadioGroup(typeOptions, nil)
					typeRadio.Horizontal = true
					if len(typeOptions) > 0 {
						typeRadio.SetSelected(typeOptions[0])
					}
					// Only one product type present: lock the choice to it.
					if len(typeOptions) <= 1 {
						typeRadio.Disable()
					}
					typeRadio.OnChanged = func(string) {
						updateDependentOptions()
						updateSelectedFiles()
					}

					// Instrument defaults to "Any" so mixed-instrument batches are
					// discoverable; the user narrows it when combining a single one.
					instrumentSelect.SetSelected(instrumentSelect.Options[0])

					// Default to a concrete filter (preserving the prior
					// single-filter workflow) and the full available date range.
					if len(filterSelect.Options) > 1 {
						filterSelect.SetSelected(filterSelect.Options[1])
					} else {
						filterSelect.SetSelected(filterSelect.Options[0])
					}
					updateDependentOptions()
					updateSelectedFiles()

					setAllFilesChecked := func(checked bool) {
						updatingChecks = true
						for i, chk := range checkBoxes {
							if chk == nil {
								continue
							}
							fileChecks[i].checked = checked
							chk.SetChecked(checked)
						}
						updatingChecks = false
						if filterPreviewRefreshRequests(previewWindow != nil, true, len(checkBoxes)) > 0 && refreshPreview != nil {
							refreshPreview()
						}
					}

					formItems := []*widget.FormItem{
						widget.NewFormItem("Image Type", typeRadio),
						widget.NewFormItem("Instrument", instrumentSelect),
						widget.NewFormItem("Filter", filterSelect),
						widget.NewFormItem("Proposal ID", proposalSelect),
						widget.NewFormItem("Exposure Time", exposureSelect),
					}
					if dateMinSelect != nil {
						formItems = append(formItems,
							widget.NewFormItem("Date From", dateMinSelect),
							widget.NewFormItem("Date To", dateMaxSelect),
						)
					}

					openPreviewBtn := widget.NewButton("Open Footprint Preview", func() {
						if previewWindow == nil {
							previewWindow = app.NewWindow("Drizzle Footprint Preview")
							refreshBtn := widget.NewButton("Refresh Preview", func() {
								if refreshPreview != nil {
									refreshPreview()
								}
							})
							previewWindow.SetContent(container.NewBorder(refreshBtn, nil, nil, nil, footprintPreview.root))
							previewWindow.Resize(fyne.NewSize(900, 700))
							previewWindow.SetOnClosed(func() { previewWindow = nil })
						}
						previewWindow.Show()
						if refreshPreview != nil {
							refreshPreview()
						}
					})

					body := container.NewVBox(
						widget.NewLabel("Filter the discovered calibrated _flc/_flt inputs (each facet is optional):"),
						widget.NewForm(formItems...),
						container.NewGridWithColumns(2,
							widget.NewButton("Check All", func() { setAllFilesChecked(true) }),
							widget.NewButton("Uncheck All", func() { setAllFilesChecked(false) }),
						),
						openPreviewBtn,
						filesScroll,
					)
					// Scroll the complete form/body; the modal's native action row remains
					// outside this scroller and is therefore always visible below it.
					content := container.NewVScroll(body)
					confirm := dialog.NewCustomConfirm("Load Filter Batch", "Load Files", "Cancel", content, func(ok bool) {
						if previewWindow != nil {
							previewWindow.Close()
						}
						if !ok {
							return
						}
						// "Any" filter loads a mixed-filter batch; activeFilter is
						// left empty so per-filter prefs/save names are skipped.
						ws.activeFilter = mosaic.FacetValue(filterSelect.Selected)
						ws.resetMTFMidtone()
						if ws.activeFilter != "" {
							ws.loadLevelPrefsAndMode(ws.activeFilter)
						}
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
					// Fit both 300px panes plus dialog padding without horizontal
					// clipping; the body scrolls vertically below the native actions.
					confirm.Resize(fyne.NewSize(720, 480))
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
		fd.Resize(fyne.NewSize(640, 480))
		fd.Show()
	})
	ws.batchBtn = batchBtn

	savePreviewToggle := NewToggle(func(v bool) {
		state.savePreview = v
	})

	measureBtn := widget.NewButton("Measure", func() {
		ws.enterMeasureMode()
	})

	starAlignBtn := widget.NewButton("Align / Register Frames", func() {
		if len(state.inputs) < 2 {
			dialog.ShowInformation("Missing Inputs", "Load at least two FITS files before star alignment.", win)
			return
		}
		alignCtx, alignGeneration, started := ws.beginMosaicAlignment()
		if !started {
			dialog.ShowInformation("Star Alignment", "An alignment operation is already running.", win)
			return
		}
		settings := state.alignmentSettings
		go func() {
			var finishOnce sync.Once
			finishOK := false
			finish := func() bool {
				finishOnce.Do(func() { finishOK = ws.finishMosaicAlignment(alignGeneration) })
				return finishOK
			}
			queued := false
			defer func() {
				if !queued {
					finish()
				}
			}()
			pt := newProgressTrackerWithContext("Aligning By Stars", "Refining per-image offsets from stars in the shared overlap...", win, alignCtx, func() { ws.cancelMosaicAlignment() })
			if alignCtx.Err() != nil {
				pt.hide()
				return
			}
			// TweakReg modes stream each frame's pixels on demand during
			// alignment, so only legacy warp-based modes require every frame
			// resident up front.
			alignMode := mosaic.AlignmentMode(settings.AlignmentMode)
			if !mosaic.AlignmentStreamsPixels(alignMode) {
				if err := ws.ensureInputPixelsLoaded(); err != nil {
					pt.hide()
					fyne.Do(func() { dialog.ShowError(err, win) })
					return
				}
			}
			workerSnapshot := ws.alignmentInputSnapshot()
			alignInputs, stateIndices := alignmentWorksetFor(workerSnapshot.inputs, workerSnapshot.statuses, workerSnapshot.reference, settings.NumRefs)
			if len(alignInputs) < 2 {
				pt.hide()
				fyne.Do(func() {
					dialog.ShowInformation("Star Alignment", "All eligible inputs already have saved alignments.", win)
				})
				return
			}
			results, err := mosaic.AlignInputsByStarsWithMode(alignInputs, alignmentNumRefsFor(workerSnapshot.reference, settings.NumRefs), alignMode, settings.SearchRadiusArcsec, mosaic.AlignProgress{
				Progress: func(done, total int) { pt.progress("Aligning", done, total) },
				Ctx:      pt.ctx,
			})
			if alignCtx.Err() != nil {
				pt.hide()
				return
			}
			if errors.Is(err, mosaic.ErrCancelled) {
				debuglog.Log("starAlign: cancelled by user")
				pt.hide()
				return
			}

			// Build the row data entirely off the main goroutine before touching UI.
			var rows []alignmentResultRow
			if err == nil {
				rows = buildAlignmentResultRowsForStateIndices(results, stateIndices)
				if len(alignInputs) > 0 {
					bindAlignmentResultSnapshot(rows, workerSnapshot)
				}
			}

			pt.hide()
			queued = true
			fyne.Do(func() {
				finished := finish()
				if alignCtx.Err() != nil || !finished {
					return
				}
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
					name := mosaic.InputLabel(workerSnapshot.inputs[r.stateIdx])
					if r.result.Applied {
						rot := 0.0
						if r.result.HasManualTransform {
							t := r.result.ManualTransform
							rot = math.Atan2(t.D, t.A) * 180 / math.Pi
						}
						label := fmt.Sprintf("%s  X: %.2f  Y: %.2f  Rot (deg): %.4f", name, r.result.OffsetX, r.result.OffsetY, rot)
						chk := widget.NewCheck(label, nil)
						chk.SetChecked(true)
						checks[i] = chk
						content.Add(chk)
						content.Add(widget.NewLabel(alignmentDiagnosticsText(r.result)))
						diagBtn := widget.NewButton("Diagnostics…", func() {
							showAlignmentDiagnosticsDialog(win, r, name)
						})
						content.Add(diagBtn)
					} else {
						label := fmt.Sprintf("%s  [failed: %s]", name, r.result.Error)
						chk := widget.NewCheck(label, nil)
						chk.Disable()
						checks[i] = chk
						content.Add(chk)
					}
				}

				var d dialog.Dialog
				exportBtn := widget.NewButton("Export CSV...", func() {
					save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, saveErr error) {
						if saveErr != nil || uc == nil {
							return
						}
						path := uc.URI().Path()
						_ = uc.Close()
						if filepath.Ext(path) == "" {
							path += ".csv"
						}
						file, createErr := os.Create(path)
						if createErr != nil {
							dialog.ShowError(createErr, win)
							return
						}
						writeErr := writeAlignmentCSV(file, rows, state.inputs)
						closeErr := file.Close()
						if writeErr != nil {
							dialog.ShowError(writeErr, win)
							return
						}
						if closeErr != nil {
							dialog.ShowError(closeErr, win)
							return
						}
						dialog.ShowInformation("Exported", "Alignment CSV exported successfully.", win)
					}, win)
					name := "alignment_results.csv"
					if ws.activeFilter != "" {
						name = ws.activeFilter + "_alignment_results.csv"
					}
					save.SetFileName(name)
					save.SetFilter(storage.NewExtensionFileFilter([]string{".csv"}))
					save.Show()
				})
				applyBtn := widget.NewButton("Apply", func() {
					saveErr := ws.applyAlignmentRowsAndSave(rows, func(i int) bool {
						return checks[i] != nil && checks[i].Checked
					}, mosaic.MergeSaveAlignmentSidecar)
					d.Hide()
					ws.rebuildOffsetControls()
					ws.updateStatus()
					go ws.buildDrizzlePreview()
					if saveErr != nil {
						dialog.ShowError(saveErr, win)
					}
				})
				scroll := container.NewVScroll(content)
				scroll.SetMinSize(fyne.NewSize(520, 200))
				d = dialog.NewCustom("Star Alignment Results", "Dismiss", container.NewVBox(scroll, container.NewGridWithColumns(2, exportBtn, applyBtn)), win)
				d.Show()
			})
		}()
	})

	selectStarsBtn := widget.NewButton("Select Stars...", func() {
		ws.enterStarMode()
	})

	buildBtn := widget.NewButton("Create Mosaic", func() {
		if ws.queueRunning {
			return
		}
		if len(state.inputs) == 0 {
			dialog.ShowInformation("Missing Inputs", "Add one or more FITS files first.", win)
			return
		}
		if !state.drizzleSettingsSet {
			showDrizzleSettingsDialog(win, state.drizzleSettings, state.inputs, func(s models.DrizzleSettings) {
				state.drizzleSettings = s
				state.drizzleSettingsSet = true
				go ws.buildDrizzlePreview()
			})
			return
		}
		go ws.buildDrizzlePreview()
	})
	ws.buildBtn = buildBtn

	openBlinkerBtn := widget.NewButton("Open Blinker", func() {
		dir := strings.TrimSpace(state.drizzleSettings.DebugOutputDir)
		if dir == "" {
			dialog.ShowInformation("No Debug Dir", "Debug Output Dir is not set in Drizzle Settings. Set it and build a preview first.", win)
			return
		}
		black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
		showBlinkerWindow(app, dir, black, white, bg, peak, scaledPeak, ws.stretchMode)
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
			state.resultName = filepath.Base(path)
			stats := histogram.Compute(state.result.Pixels)
			ws.statsLabel.SetText(mosaicStatsText(state.resultName, stats.Mean, stats.Std, state.result.Width, state.result.Height))
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
		ws.inputMu.Lock()
		state.inputs = nil
		ws.inputGenerations = make(map[string]uint64)
		ws.inputMu.Unlock()
		state.statuses = nil
		ws.activeFilter = ""
		ws.levelsSet = false
		blackEntry.SetValue(0)
		whiteEntry.SetValue(1)
		bgEntry.SetValue(0)
		peakEntry.SetValue(1000)
		scaledPeakEntry.SetValue(1000)
		ws.resetMTFMidtone()
		ws.updateStatus()
		ws.resetPreview()
		ws.rebuildOffsetControls()
		ws.updateActionButtons()
	})
	clearBtn.Importance = widget.DangerImportance
	ws.clearBtn = clearBtn

	statusScroll := container.NewVScroll(statusLabel)
	statusScroll.SetMinSize(fyne.NewSize(260, 160))

	// Level controls form.
	var mtfMidtoneRow fyne.CanvasObject
	updateStretchParams := func() {
		if mtfMidtoneRow == nil {
			return
		}
		if ws.stretchMode == stretch.MTF {
			mtfMidtoneRow.Show()
		} else {
			mtfMidtoneRow.Hide()
		}
	}
	modeSelect := NewSafeSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq", "MTF"}, func(s string) {
		switch s {
		case "Linear":
			ws.stretchMode = stretch.Linear
		case "Log":
			ws.stretchMode = stretch.Log
		case "Sqrt":
			ws.stretchMode = stretch.Sqrt
		case "HistEq":
			ws.stretchMode = stretch.HistEq
		case "MTF":
			ws.stretchMode = stretch.MTF
		default:
			ws.stretchMode = stretch.Asinh
		}
		updateStretchParams()
	})
	modeSelect.SetSelected("Asinh")
	ws.modeSelect = modeSelect
	makeFormRow := func(label string, w fyne.CanvasObject) fyne.CanvasObject {
		lbl := widget.NewLabel(label)
		return container.NewBorder(nil, nil, container.New(&minWidthLayout{w: 90}, lbl), nil, w)
	}
	mtfMidtoneRow = makeFormRow("MTF midtone", mtfMidtoneEntry)
	updateStretchParams()
	magicPreset := widget.NewSelect([]string{"Balanced", "Nebula", "Galaxy"}, nil)
	magicPreset.SetSelected("Balanced")
	levelsForm := container.New(&fixedVSpacingLayout{15},
		makeFormRow("Mode", modeSelect),
		makeFormRow("Background", bgEntry),
		makeFormRow("Peak", peakEntry),
		makeFormRow("Scaled Peak", scaledPeakEntry),
		mtfMidtoneRow,
		makeFormRow("Magic preset", magicPreset),
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
	autoMTFBtn := widget.NewButton("Auto MTF", func() {
		ws.autoMTFLevels(ws.currentPreviewResult())
	})
	magicBtn := widget.NewButton("Magic", func() {
		ws.magicLevels(ws.currentPreviewResult(), processing.ParseMagicPreset(magicPreset.Selected))
	})
	applyLevelsBtn := widget.NewButton("Apply Values", func() {
		ws.mtfMidtone = ws.mtfMidtoneEntry.Value()
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
			ws.confirmReferenceFrameChange(&inp, func() {
				state.referenceInput = &inp
				refLabel.SetText("Reference: " + filepath.Base(path))
				ws.resetPreview()
				ws.updateActionButtons()
			})
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		ws.configureLastDir(fd)
		fd.SetView(dialog.ListView)
		sizeFileDialog(fd)
		fd.Show()
	})
	ws.setRefBtn = setRefBtn

	clearRefBtn := widget.NewButton("Clear Reference", func() {
		ws.confirmReferenceFrameChange(nil, func() {
			state.referenceInput = nil
			refLabel.SetText("Reference: none")
			ws.resetPreview()
			ws.updateActionButtons()
		})
	})
	clearRefBtn.Importance = widget.DangerImportance
	ws.clearRefBtn = clearRefBtn

	inputFramesBtn := widget.NewButton("Input Frames...", func() {
		ws.openInputFramesPopup()
	})

	inputFramesTable := newMosaicInputFramesScroll(offsetHeader, offsetScroll)
	inputTabs := container.NewAppTabs(
		container.NewTabItem("Input Frames", inputFramesTable),
		container.NewTabItem("Input Status", statusScroll),
	)

	previewSettings := container.NewVBox(
		levelsForm,
		func() fyne.CanvasObject {
			r := canvas.NewRectangle(color.Transparent)
			r.SetMinSize(fyne.NewSize(1, 20))
			return r
		}(),
		container.NewGridWithColumns(2, autoLevelsBtn, autoMTFBtn),
		container.NewGridWithColumns(2, magicBtn, applyLevelsBtn),
	)
	drizzleCommands := container.NewVBox(
		container.NewGridWithColumns(2, loadBtn, batchBtn),
		directoryBtn,
		container.NewHBox(savePreviewToggle, widget.NewLabel("Save Preview")),
		widget.NewSeparator(),
		widget.NewLabel("Baseline Reference"),
		refLabel,
		container.New(&fixedVSpacingLayout{15},
			container.NewGridWithColumns(2, setRefBtn, clearRefBtn),
			container.NewGridWithColumns(2, starAlignBtn, selectStarsBtn),
			container.NewGridWithColumns(2, measureBtn, buildBtn),
			container.NewGridWithColumns(2, openBlinkerBtn, saveOffsetsBtn),
			container.NewGridWithColumns(2, loadOffsetsBtn, clearOffsetsBtn),
			container.NewGridWithColumns(2, clearBtn, layout.NewSpacer()),
		),
	)
	inputFrames := container.NewBorder(inputFramesBtn, nil, nil, nil, inputTabs)
	controls := widget.NewAccordion(
		widget.NewAccordionItem("Preview Settings", previewSettings),
		widget.NewAccordionItem("Drizzle Commands", drizzleCommands),
		widget.NewAccordionItem("Input Frames", inputFrames),
	)
	controls.MultiOpen = true
	controls.OpenAll()

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
	zoomPresets := []string{"fit", "6%", "12%", "25%", "50%", "75%", "100%", "150%", "200%", "300%", "400%"}
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
		if sel == "fit" {
			ws.zoomFitMode = true
			ws.updateZoom()
			return
		}
		ws.zoomFitMode = false
		s := strings.TrimSuffix(sel, "%")
		if val, err := strconv.ParseFloat(s, 64); err == nil {
			ws.zoomLevel = clampMosaicZoom(val / 100.0)
			ws.updateZoom()
		}
	}

	zoomInBtn := widget.NewButton("+", func() {
		ws.zoomFitMode = false
		ws.zoomLevel = clampMosaicZoom(ws.zoomLevel * 1.25)
		ws.updateZoom()
	})
	zoomOutBtn := widget.NewButton("-", func() {
		ws.zoomFitMode = false
		ws.zoomLevel = clampMosaicZoom(ws.zoomLevel / 1.25)
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
	zoomSelect.SetSelected("fit")

	ws.rebuildOffsetControls()
	ws.updateStatus()
	ws.updateActionButtons()

	// ---- Project save/load -----------------------------------------------

	// ---- Settings menu -----------------------------------------------

	loadMosaicItem := fyne.NewMenuItem("Load Mosaic Project", ws.loadMosaicProject)
	saveMosaicItem := fyne.NewMenuItem("Save Mosaic Project", ws.saveMosaicProject)

	settingsMenu := fyne.NewMenu("Mosaic",
		fyne.NewMenuItem("Drizzle Queue...", ws.openDrizzleQueue),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Drizzle Settings", ws.openDrizzleSettings),
		fyne.NewMenuItem("Alignment Settings", ws.openAlignmentSettings),
		fyne.NewMenuItem("Skysub Settings", ws.openSkysubSettings),
		fyne.NewMenuItem("Exposure Normalization", ws.openExposureReview),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Create Artifact Masks...", ws.openArtifactMaskEditor),
	)
	footerBottomPad := canvas.NewRectangle(color.Transparent)
	footerBottomPad.SetMinSize(fyne.NewSize(1, 20))
	previewPane := container.NewBorder(previewHeader, container.NewVBox(previewFooter, footerBottomPad), nil, nil, ws.previewSwap)
	split := container.NewHSplit(container.New(&sidePaddedLayout{20}, ws.leftStack), previewPane)
	split.SetOffset(0.38)
	return split, settingsMenu, loadMosaicItem, saveMosaicItem
}

func newMosaicInputFramesScroll(offsetHeader *fyne.Container, offsetScroll *container.Scroll) *container.Scroll {
	table := container.NewBorder(offsetHeader, nil, nil, nil, offsetScroll)
	scroll := container.NewHScroll(table)
	scroll.SetMinSize(fyne.NewSize(260, 180))
	return scroll
}
