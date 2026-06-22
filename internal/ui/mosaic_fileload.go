package ui

import (
	"image/color"
	"fmt"
	"math"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

func (ws *mosaicWorkspace) rebuildOffsetControls() {
	ws.offsetControls.Objects = nil
	ws.offsetHeader.Objects = nil
	if len(ws.state.inputs) == 0 {
		ws.offsetControls.Add(widget.NewLabel("No FITS files loaded."))
		ws.offsetControls.Refresh()
		ws.offsetHeader.Refresh()
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
	ws.offsetHeader.Add(header)
	ws.offsetHeader.Refresh()

	for idx := range ws.state.inputs {
		name := mosaic.InputLabel(ws.state.inputs[idx])
		xEntry := NewNumberEntry(1, 2)
		xEntry.MinWidth = 1
		xEntry.SetValue(ws.state.inputs[idx].OffsetX)
		yEntry := NewNumberEntry(1, 2)
		yEntry.MinWidth = 1
		yEntry.SetValue(ws.state.inputs[idx].OffsetY)
		rotEntry := NewNumberEntry(0.01, 4)
		rotEntry.MinWidth = 1
		currentRot := 0.0
		if ws.state.inputs[idx].HasManualTransform {
			t := ws.state.inputs[idx].ManualTransform
			currentRot = math.Atan2(t.D, t.A) * 180 / math.Pi
		}
		rotEntry.SetValue(currentRot)

		includeCheck := widget.NewCheck("", func(index int) func(bool) {
			return func(included bool) {
				ws.state.inputs[index].Excluded = !included
			}
		}(idx))
		includeCheck.SetChecked(!ws.state.inputs[idx].Excluded)

		lockCheck := widget.NewCheck("", func(index int) func(bool) {
			return func(locked bool) {
				ws.state.inputs[index].OffsetLocked = locked
			}
		}(idx))
		lockCheck.SetChecked(ws.state.inputs[idx].OffsetLocked)

		applyBtn := widget.NewButton("Apply", func(index int, xBox, yBox, rotBox *NumberEntry) func() {
			return func() {
				xVal := xBox.Value()
				yVal := yBox.Value()
				rotVal := rotBox.Value()
				ws.state.inputs[index].OffsetX = xVal
				ws.state.inputs[index].OffsetY = yVal
				if rotVal != 0 {
					rad := rotVal * math.Pi / 180
					cx := float64(ws.state.inputs[index].HDU.Data.Width-1) / 2
					cy := float64(ws.state.inputs[index].HDU.Data.Height-1) / 2
					ws.state.inputs[index].ManualTransform = processing.RotationAround(cx, cy, rad)
					ws.state.inputs[index].HasManualTransform = true
				} else {
					ws.state.inputs[index].ManualTransform = processing.IdentityTransform()
					ws.state.inputs[index].HasManualTransform = false
				}
				if index < len(ws.state.statuses) && ws.state.statuses[index].Status == "loaded" {
					ws.state.statuses[index].Status = "manual offset set"
				}
				ws.resetPreview()
				ws.updateStatus()
			}
		}(idx, xEntry, yEntry, rotEntry))

		flashBtn := widget.NewButton("Flash", func(index int) func() {
			return func() {
				if ws.state.result == nil || ws.activePicker != nil || ws.activeMeasure != nil {
					return
				}
				inputPath := mosaic.InputKey(ws.state.inputs[index])
				var fps [][4][2]float64
				for fi, fp := range ws.state.result.InputFootprints {
					if fi < len(ws.state.result.InputFootprintPaths) && ws.state.result.InputFootprintPaths[fi] == inputPath {
						fps = append(fps, fp)
					}
				}
				if len(fps) == 0 {
					return
				}
				black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
				go func() {
					baseImg := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode)
					flashImg := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode)
					salmon := color.RGBA{R: 250, G: 128, B: 114, A: 255}
					pairs := [4][2]int{{0, 1}, {1, 3}, {3, 2}, {2, 0}}
					for _, fp := range fps {
						for _, p := range pairs {
							drawThickLine(flashImg, fp[p[0]], fp[p[1]], 5, salmon)
						}
					}
					for i := 0; i < 3; i++ {
						fyne.DoAndWait(func() {
							ws.preview.Image = flashImg
							ws.preview.Refresh()
						})
						time.Sleep(500 * time.Millisecond)
						fyne.DoAndWait(func() {
							ws.preview.Image = baseImg
							ws.preview.Refresh()
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
				ws.state.inputs[index-1], ws.state.inputs[index] = ws.state.inputs[index], ws.state.inputs[index-1]
				if index < len(ws.state.statuses) && index-1 < len(ws.state.statuses) {
					ws.state.statuses[index-1], ws.state.statuses[index] = ws.state.statuses[index], ws.state.statuses[index-1]
				}
				ws.resetPreview()
				ws.rebuildOffsetControls()
			}
		}(idx))
		downBtn := widget.NewButton("↓", func(index int) func() {
			return func() {
				if index >= len(ws.state.inputs)-1 {
					return
				}
				ws.state.inputs[index], ws.state.inputs[index+1] = ws.state.inputs[index+1], ws.state.inputs[index]
				if index < len(ws.state.statuses) && index+1 < len(ws.state.statuses) {
					ws.state.statuses[index], ws.state.statuses[index+1] = ws.state.statuses[index+1], ws.state.statuses[index]
				}
				ws.resetPreview()
				ws.rebuildOffsetControls()
			}
		}(idx))
		if idx == 0 {
			upBtn.Disable()
		}
		if idx == len(ws.state.inputs)-1 {
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
		ws.offsetControls.Add(row)
	}
	ws.offsetControls.Refresh()
}

func (ws *mosaicWorkspace) openInputFramesPopup() {
	if len(ws.state.inputs) == 0 {
		dialog.ShowInformation("Input Frames", "Load at least one FITS file first.", ws.win)
		return
	}

	desc := widget.NewLabel("Select records by exposure or date. Changes update the Input Frames tab immediately.")

	nameCell := func(obj fyne.CanvasObject) fyne.CanvasObject {
		return container.New(&minWidthLayout{160}, obj)
	}
	hdrLabel := func(text string) fyne.CanvasObject {
		return widget.NewLabelWithStyle(text, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	}
	hdrCell := func(text string, w float32) fyne.CanvasObject {
		return container.New(&minWidthLayout{w: w}, hdrLabel(text))
	}
	hdrSpace := func(w float32) fyne.CanvasObject {
		r := canvas.NewRectangle(color.Transparent)
		r.SetMinSize(fyne.NewSize(w, 1))
		return r
	}

	compactEntry := NewNumberEntry(1, 2)
	compactEntry.MinWidth = 1
	entryColW := compactEntry.MinSize().Width
	btnUpW := widget.NewButton("↑", nil).MinSize().Width
	flashBtnW := widget.NewButton("Flash", nil).MinSize().Width
	applyBtnW := widget.NewButton("Apply", nil).MinSize().Width
	checkNatW := container.NewCenter(widget.NewCheck("", nil)).MinSize().Width
	lockColW := hdrLabel("Lock").MinSize().Width
	if lockColW < checkNatW {
		lockColW = checkNatW
	}
	inclColW := hdrLabel("Incl.").MinSize().Width
	if inclColW < checkNatW {
		inclColW = checkNatW
	}
	dateColW := hdrLabel("Date").MinSize().Width
	expColW := hdrLabel("Exp").MinSize().Width

	header := container.NewHBox(
		hdrSpace(btnUpW), hdrSpace(btnUpW),
		nameCell(hdrLabel("Name")),
		hdrCell("X", entryColW),
		hdrCell("Y", entryColW),
		hdrCell("Rot°", entryColW),
		hdrCell("Date", dateColW),
		hdrCell("Exp", expColW),
		hdrCell("Flash", flashBtnW),
		hdrCell("Apply", applyBtnW),
		hdrCell("Lock", lockColW),
		hdrCell("Incl.", inclColW),
	)
	rows := container.NewVBox()
	for idx := range ws.state.inputs {
		input := ws.state.inputs[idx]
		name := mosaic.InputLabel(input)
		xEntry := NewNumberEntry(1, 2)
		xEntry.MinWidth = 1
		xEntry.SetValue(input.OffsetX)
		yEntry := NewNumberEntry(1, 2)
		yEntry.MinWidth = 1
		yEntry.SetValue(input.OffsetY)
		rotEntry := NewNumberEntry(0.01, 4)
		rotEntry.MinWidth = 1
		if input.HasManualTransform {
			t := input.ManualTransform
			rotEntry.SetValue(math.Atan2(t.D, t.A) * 180 / math.Pi)
		}
		dateLabel := widget.NewLabel(input.DateObs)
		if dateLabel.Text == "" {
			dateLabel.SetText("-")
		}
		expLabel := widget.NewLabel("")
		if input.ExposureTime > 0 {
			expLabel.SetText(strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", input.ExposureTime), "0"), "."))
		} else {
			expLabel.SetText("-")
		}

		includeCheck := widget.NewCheck("", func(index int) func(bool) {
			return func(included bool) {
				ws.state.inputs[index].Excluded = !included
			}
		}(idx))
		includeCheck.SetChecked(!input.Excluded)

		lockCheck := widget.NewCheck("", func(index int) func(bool) {
			return func(locked bool) {
				ws.state.inputs[index].OffsetLocked = locked
			}
		}(idx))
		lockCheck.SetChecked(input.OffsetLocked)

		applyBtn := widget.NewButton("Apply", func(index int, xBox, yBox, rotBox *NumberEntry) func() {
			return func() {
				xVal := xBox.Value()
				yVal := yBox.Value()
				rotVal := rotBox.Value()
				ws.state.inputs[index].OffsetX = xVal
				ws.state.inputs[index].OffsetY = yVal
				if rotVal != 0 {
					rad := rotVal * math.Pi / 180
					cx := float64(ws.state.inputs[index].HDU.Data.Width-1) / 2
					cy := float64(ws.state.inputs[index].HDU.Data.Height-1) / 2
					ws.state.inputs[index].ManualTransform = processing.RotationAround(cx, cy, rad)
					ws.state.inputs[index].HasManualTransform = true
				} else {
					ws.state.inputs[index].ManualTransform = processing.IdentityTransform()
					ws.state.inputs[index].HasManualTransform = false
				}
				if index < len(ws.state.statuses) && ws.state.statuses[index].Status == "loaded" {
					ws.state.statuses[index].Status = "manual offset set"
				}
				ws.resetPreview()
				ws.updateStatus()
			}
		}(idx, xEntry, yEntry, rotEntry))

		flashBtn := widget.NewButton("Flash", func(index int) func() {
			return func() {
				if ws.state.result == nil || ws.activePicker != nil || ws.activeMeasure != nil {
					return
				}
				inputPath := mosaic.InputKey(ws.state.inputs[index])
				var fps [][4][2]float64
				for fi, fp := range ws.state.result.InputFootprints {
					if fi < len(ws.state.result.InputFootprintPaths) && ws.state.result.InputFootprintPaths[fi] == inputPath {
						fps = append(fps, fp)
					}
				}
				if len(fps) == 0 {
					return
				}
				black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
				go func() {
					baseImg := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode)
					flashImg := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode)
					salmon := color.RGBA{R: 250, G: 128, B: 114, A: 255}
					pairs := [4][2]int{{0, 1}, {1, 3}, {3, 2}, {2, 0}}
					for _, fp := range fps {
						for _, p := range pairs {
							drawThickLine(flashImg, fp[p[0]], fp[p[1]], 5, salmon)
						}
					}
					for i := 0; i < 3; i++ {
						fyne.DoAndWait(func() {
							ws.preview.Image = flashImg
							ws.preview.Refresh()
						})
						time.Sleep(500 * time.Millisecond)
						fyne.DoAndWait(func() {
							ws.preview.Image = baseImg
							ws.preview.Refresh()
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
				ws.state.inputs[index-1], ws.state.inputs[index] = ws.state.inputs[index], ws.state.inputs[index-1]
				if index < len(ws.state.statuses) && index-1 < len(ws.state.statuses) {
					ws.state.statuses[index-1], ws.state.statuses[index] = ws.state.statuses[index], ws.state.statuses[index-1]
				}
				ws.resetPreview()
				ws.rebuildOffsetControls()
			}
		}(idx))
		downBtn := widget.NewButton("↓", func(index int) func() {
			return func() {
				if index >= len(ws.state.inputs)-1 {
					return
				}
				ws.state.inputs[index], ws.state.inputs[index+1] = ws.state.inputs[index+1], ws.state.inputs[index]
				if index < len(ws.state.statuses) && index+1 < len(ws.state.statuses) {
					ws.state.statuses[index], ws.state.statuses[index+1] = ws.state.statuses[index+1], ws.state.statuses[index]
				}
				ws.resetPreview()
				ws.rebuildOffsetControls()
			}
		}(idx))
		if idx == 0 {
			upBtn.Disable()
		}
		if idx == len(ws.state.inputs)-1 {
			downBtn.Disable()
		}

		row := container.NewHBox(
			upBtn, downBtn,
			nameCell(widget.NewLabel(name)),
			xEntry, yEntry, rotEntry,
			dateLabel, expLabel,
			flashBtn, applyBtn,
			container.New(&minWidthLayout{w: lockColW}, container.NewCenter(lockCheck)),
			container.New(&minWidthLayout{w: inclColW}, container.NewCenter(includeCheck)),
		)
		rows.Add(row)
	}
	scroll := container.NewVScroll(rows)
	scroll.SetMinSize(fyne.NewSize(900, 360))
	table := container.NewBorder(header, nil, nil, nil, scroll)
	content := container.NewVBox(desc, widget.NewSeparator(), table)

	d := dialog.NewCustom("Input Frames", "Close", content, ws.win)
	d.Resize(fyne.NewSize(960, 520))
	d.Show()
}

func (ws *mosaicWorkspace) loadPaths(paths []string, title string) {
	// Filter out paths already loaded.
	existingPaths := make(map[string]bool, len(ws.state.inputs))
	for _, inp := range ws.state.inputs {
		existingPaths[inp.Path] = true
	}
	filtered := paths[:0:0]
	for _, p := range paths {
		if !existingPaths[p] {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == 0 {
		dialog.ShowInformation("Already Added", "All selected files are already in the mosaic.", ws.win)
		return
	}
	skipped := len(paths) - len(filtered)
	paths = filtered

	progressDialog := dialog.NewCustom(title, "Reading FITS data...", widget.NewProgressBarInfinite(), ws.win)
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
					status.Status = "loaded (warning: not _flc/_flt)"
					warnings++
				}
				newStatuses = append(newStatuses, status)
			}
		}

		offsetMessages := ws.applyAutoLoadedOffsets(newInputs)
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
			messages = append(messages, "Some loaded files are not standard _flc/_flt inputs. They were kept, but this workflow is tuned for HST calibrated science files.")
		}
		messages = append(messages, offsetMessages...)
		fyne.Do(func() {
			progressDialog.Hide()
			ws.state.inputs = append(ws.state.inputs, newInputs...)
			ws.state.statuses = append(ws.state.statuses, newStatuses...)
			// Re-sort the full inputs list so overall drizzle order is correct.
			if len(ws.state.inputs) > 1 {
				mosaic.SortInputsByWCSDistance(ws.state.inputs, ws.state.statuses)
			}
			ws.resetPreview()
			ws.rebuildOffsetControls()
			ws.updateStatus()
			ws.updateActionButtons()
			if len(messages) > 0 {
				dialog.ShowInformation("Mosaic Load", strings.Join(messages, "\n"), ws.win)
			}
		})
	}()
}

func (ws *mosaicWorkspace) configureLastDir(fd *dialog.FileDialog) {
	if last := ws.app.Preferences().String("lastDir"); last != "" {
		uri := storage.NewFileURI(last)
		if l, err := storage.ListerForURI(uri); err == nil {
			fd.SetLocation(l)
		}
	}
}
