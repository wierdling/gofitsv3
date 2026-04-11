package ui

import (
	"fmt"
	"image"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/histogram"
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

	loadPaths := func(paths []string, title string) {
		progressDialog := dialog.NewCustom(title, "Reading FITS data...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			newInputs := make([]mosaic.Input, 0, len(paths))
			newStatuses := make([]mosaic.InputStatus, 0, len(paths))
			warnings := 0

			for _, path := range paths {
				img, err := loadImageFromPath(path)
				if err != nil {
					newStatuses = append(newStatuses, mosaic.InputStatus{Path: path, Status: "failed", Error: err.Error()})
					continue
				}

				newInputs = append(newInputs, mosaic.Input{Path: path, PrimaryHeader: img.Primary, HDU: img.HDU})
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
			row := container.NewVBox(
				widget.NewLabel(name),
				container.NewGridWithColumns(5,
					widget.NewLabel("X"), xEntry,
					widget.NewLabel("Y"), yEntry,
					applyBtn,
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
				filesLabel := widget.NewLabel("")
				filesLabel.Wrapping = fyne.TextWrapWord
				filesScroll := container.NewVScroll(filesLabel)
				filesScroll.SetMinSize(fyne.NewSize(420, 220))
				updateSelectedFiles := func(option string) {
					paths := mosaic.PathsForFilterOption(groups, option)
					labels := make([]string, 0, len(paths)+1)
					labels = append(labels, fmt.Sprintf("%d files will be loaded:", len(paths)))
					for _, path := range paths {
						labels = append(labels, filepath.Base(path))
					}
					filesLabel.SetText(strings.Join(labels, "\n"))
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
					paths := mosaic.PathsForFilterOption(groups, filterSelect.Selected)
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
			updateMosaicPreview(preview, statsLabel, result)

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
		save.SetFileName("mosaic_drizzle.fits")
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

	clearBtn := widget.NewButton("Clear", func() {
		state.inputs = nil
		state.statuses = nil
		updateStatus()
		resetPreview()
		rebuildOffsetControls()
	})

	statusScroll := container.NewVScroll(statusLabel)
	statusScroll.SetMinSize(fyne.NewSize(260, 160))

	controls := container.NewVBox(
		widget.NewLabel("Mosaic / Drizzle"),
		container.NewGridWithColumns(2, loadBtn, batchBtn),
		widget.NewForm(widget.NewFormItem("Scale", scaleEntry)),
		cleanCheck,
		savePreviewCheck,
		starAlignBtn,
		buildBtn,
		saveBtn,
		container.NewGridWithColumns(2, saveOffsetsBtn, loadOffsetsBtn),
		clearBtn,
		widget.NewSeparator(),
		widget.NewLabel("Per-Image Offsets (pixels)"),
		offsetScroll,
		widget.NewSeparator(),
		widget.NewLabel("Input Status"),
		statusScroll,
	)
	controlsScroll := container.NewVScroll(controls)
	controlsScroll.SetMinSize(fyne.NewSize(320, 220))

	rebuildOffsetControls()
	updateStatus()

	previewPane := container.NewBorder(statsLabel, nil, nil, nil, container.NewScroll(preview))
	split := container.NewHSplit(controlsScroll, previewPane)
	split.SetOffset(0.38)
	return split
}

func updateMosaicPreview(preview *canvas.Image, statsLabel *widget.Label, result *mosaic.Result) {
	img, stats := buildMosaicPreviewImage(result)
	preview.Image = img
	preview.Refresh()
	statsLabel.SetText(fmt.Sprintf("Mean: %.4f | Std: %.4f | Size: %dx%d", stats.Mean, stats.Std, result.Width, result.Height))
}

func buildMosaicPreviewImage(result *mosaic.Result) (*image.RGBA, histogram.Stats) {
	stats := histogram.Compute(result.Pixels)
	minV, maxV := processing.AutoLevels(result.Pixels)
	if maxV <= minV {
		maxV = minV + 1
	}

	normalized := make([]float32, len(result.Pixels))
	span := maxV - minV
	for i, v := range result.Pixels {
		fv := float64(v)
		if span <= 0 || fv != fv {
			normalized[i] = 0
			continue
		}
		if fv < minV {
			fv = minV
		}
		if fv > maxV {
			fv = maxV
		}
		normalized[i] = float32((fv - minV) / span)
	}

	stretched := stretch.Apply(normalized, stretch.Asinh)
	img := image.NewRGBA(image.Rect(0, 0, result.Width, result.Height))
	for i, v := range stretched {
		b := byte(v * 255)
		idx := i * 4
		img.Pix[idx] = b
		img.Pix[idx+1] = b
		img.Pix[idx+2] = b
		img.Pix[idx+3] = 255
	}
	return img, stats
}
