package ui

import (
	"fmt"
	"image/color"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

// mosaicFixedWidthLayout keeps a table column at exactly the requested width.
// It is intentionally local to the Mosaic input-frame table; other min-width
// layouts allow content to grow and are used for different UI behavior.
type mosaicFixedWidthLayout struct{ w float32 }

const mosaicInputNameColumnWidth = 320

func (l *mosaicFixedWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, object := range objects {
		object.Move(fyne.NewPos(0, 0))
		object.Resize(fyne.NewSize(l.w, size.Height))
	}
}

func (l *mosaicFixedWidthLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var height float32
	for _, object := range objects {
		if object.MinSize().Height > height {
			height = object.MinSize().Height
		}
	}
	return fyne.NewSize(l.w, height)
}

func (ws *mosaicWorkspace) rebuildOffsetControls() {
	ws.offsetControls.Objects = nil
	ws.offsetHeader.Objects = nil
	if len(ws.state.inputs) == 0 {
		ws.offsetControls.Add(widget.NewLabel("No FITS files loaded."))
		ws.offsetControls.Refresh()
		ws.offsetHeader.Refresh()
		return
	}

	// nameCell keeps the header and row name columns aligned even for long names.
	nameCell := func(obj fyne.CanvasObject) fyne.CanvasObject {
		return container.New(&mosaicFixedWidthLayout{mosaicInputNameColumnWidth}, obj)
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
		xEntry.MinWidth = entryColW
		xEntry.SetValue(ws.state.inputs[idx].OffsetX)
		yEntry := NewNumberEntry(1, 2)
		yEntry.MinWidth = entryColW
		yEntry.SetValue(ws.state.inputs[idx].OffsetY)
		rotEntry := NewNumberEntry(0.01, 4)
		rotEntry.MinWidth = entryColW
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
					baseImg := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode, ws.mtfMidtone)
					flashImg := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode, ws.mtfMidtone)
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

		nameLabel := widget.NewLabel(name)
		nameLabel.Truncation = fyne.TextTruncateEllipsis
		row := container.NewHBox(
			upBtn, downBtn,
			nameCell(nameLabel),
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
	if ws.queueRunning {
		return
	}
	if ws.inputFramesWindow != nil {
		ws.inputFramesWindow.Show()
		ws.inputFramesWindow.RequestFocus()
		return
	}
	if len(ws.state.inputs) == 0 {
		dialog.ShowInformation("Input Frames", "Load at least one FITS file first.", ws.win)
		return
	}

	desc := widget.NewLabel("Select records by exposure or date. Changes update the Input Frames tab immediately.")

	nameCell := func(obj fyne.CanvasObject) fyne.CanvasObject {
		return container.New(&mosaicFixedWidthLayout{160}, obj)
	}
	hdrLabel := func(text string) fyne.CanvasObject {
		return widget.NewLabelWithStyle(text, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	}
	hdrCell := func(text string, w float32) fyne.CanvasObject {
		return container.New(&mosaicFixedWidthLayout{w: w}, hdrLabel(text))
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
		xEntry.MinWidth = entryColW
		xEntry.SetValue(input.OffsetX)
		yEntry := NewNumberEntry(1, 2)
		yEntry.MinWidth = entryColW
		yEntry.SetValue(input.OffsetY)
		rotEntry := NewNumberEntry(0.01, 4)
		rotEntry.MinWidth = entryColW
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
		dateLabel.Truncation = fyne.TextTruncateEllipsis
		expLabel.Truncation = fyne.TextTruncateEllipsis

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
					baseImg := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode, ws.mtfMidtone)
					flashImg := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode, ws.mtfMidtone)
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

		nameLabel := widget.NewLabel(name)
		nameLabel.Truncation = fyne.TextTruncateEllipsis
		row := container.NewHBox(
			upBtn, downBtn,
			nameCell(nameLabel),
			xEntry, yEntry, rotEntry,
			container.New(&mosaicFixedWidthLayout{w: dateColW}, dateLabel),
			container.New(&mosaicFixedWidthLayout{w: expColW}, expLabel),
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

	var inputWindow fyne.Window
	closeBtn := widget.NewButton("Close", func() {
		inputWindow.Close()
	})
	inputWindow = ws.app.NewWindow("Input Frames")
	ws.inputFramesWindow = inputWindow
	inputWindow.SetContent(container.NewBorder(nil, closeBtn, nil, nil, content))
	inputWindow.SetFixedSize(true)
	inputWindow.SetOnClosed(func() {
		ws.inputFramesWindow = nil
		ws.rebuildOffsetControls()
	})
	inputWindow.Resize(fyne.NewSize(960, 520))
	inputWindow.CenterOnScreen()
	inputWindow.Show()
}

func (ws *mosaicWorkspace) loadPaths(paths []string, title string) {
	if ws.queueRunning {
		return
	}
	// Filter out paths already loaded, matching on both the input's own Path and
	// (for combined inputs) the original SourcePath so re-adding a source file
	// whose chips were already combined is recognized as a duplicate.
	existingPaths := make(map[string]bool, len(ws.state.inputs)*2)
	for _, inp := range ws.state.inputs {
		existingPaths[inp.Path] = true
		if inp.SourcePath != "" {
			existingPaths[inp.SourcePath] = true
		}
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

	go func() {
		_ = skipped // available for future status reporting

		pt := newProgressTracker(title, "Reading FITS metadata...", ws.win)

		startAll := time.Now()
		debuglog.Log(fmt.Sprintf("loadPaths: reading metadata for %d file(s) with %d workers", len(paths), loadWorkers(len(paths))))

		// Pass 1 (concurrent): metadata-only load per path. Reads headers,
		// dimensions, WCS, and distortion tables but NOT the large SCI/ERR pixel
		// arrays. This detects multi-chip exposures and yields the per-chip
		// inputs used both for single-chip files and as the combine fallback.
		type metaResult struct {
			meta []mosaic.Input
			err  error
		}
		results := make([]metaResult, len(paths))
		workers := loadWorkers(len(paths))
		var next int64 = -1
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					i := int(atomic.AddInt64(&next, 1))
					if i >= len(paths) {
						return
					}
					meta, err := mosaic.LoadInputsMetadataFromPath(paths[i])
					results[i] = metaResult{meta: meta, err: err}
				}
			}()
		}
		wg.Wait()

		// Pass 2 (bounded parallel): each multi-chip exposure is drizzled into one
		// working file, and combines dominate load time while each combine is
		// essentially single-threaded, so we run them across a worker pool to use
		// all cores. Concurrency is capped by a memory budget (a combine loads a
		// full exposure and builds an output canvas + weights) so large datasets
		// stay bounded. Results are written per-path and flattened in order so the
		// input/status ordering stays deterministic.
		multiTotal := 0
		var maxCombineBytes int64
		for i := range results {
			if results[i].err != nil || len(results[i].meta) <= 1 {
				continue
			}
			multiTotal++
			var b int64
			for _, in := range results[i].meta {
				b += int64(in.HDU.Data.Width) * int64(in.HDU.Data.Height) * 4 * 4
			}
			if b > maxCombineBytes {
				maxCombineBytes = b
			}
		}

		combineWorkers := runtime.NumCPU()
		if multiTotal > 0 {
			const combineMemBudget = int64(4) << 30 // 4 GiB across concurrent combines
			if maxCombineBytes > 0 {
				if byBudget := int(combineMemBudget / maxCombineBytes); byBudget < combineWorkers {
					combineWorkers = byBudget
				}
			}
			if combineWorkers > multiTotal {
				combineWorkers = multiTotal
			}
		}
		if combineWorkers < 1 {
			combineWorkers = 1
		}

		type preparedInputs struct {
			inputs      []mosaic.Input
			statuses    []mosaic.InputStatus
			combineWarn string
		}
		prep := make([]preparedInputs, len(paths))

		// buildPerChip returns the per-chip inputs for a single- or fallback-mode
		// file. It touches no shared state so it is safe to call from any worker.
		buildPerChip := func(path string, meta []mosaic.Input, status string) preparedInputs {
			var p preparedInputs
			for _, input := range meta {
				st := mosaic.InputStatus{Path: mosaic.InputKey(input), Included: true, Status: status}
				if !mosaic.LooksLikeCalibratedInput(path) {
					st.Status = "loaded (warning: not _flc/_flt/_cal)"
				}
				p.inputs = append(p.inputs, input)
				p.statuses = append(p.statuses, st)
			}
			return p
		}

		combineOne := func(path string, meta []mosaic.Input) preparedInputs {
			workingPath, cached, cerr := mosaic.EnsureCombinedExposure(path, mosaic.CombineOptions{Ctx: pt.ctx})
			if cerr != nil {
				if cerr == mosaic.ErrCancelled {
					return preparedInputs{}
				}
				debuglog.Log(fmt.Sprintf("loadPaths: combine %s failed, using per-chip mode: %v", filepath.Base(path), cerr))
				p := buildPerChip(path, meta, "loaded (combine failed: per-chip mode)")
				p.combineWarn = fmt.Sprintf("%s: %v", filepath.Base(path), cerr)
				return p
			}
			combinedMeta, lerr := mosaic.LoadInputsMetadataFromPath(workingPath)
			if lerr != nil {
				debuglog.Log(fmt.Sprintf("loadPaths: reload combined %s failed, using per-chip mode: %v", filepath.Base(path), lerr))
				p := buildPerChip(path, meta, "loaded (combine failed: per-chip mode)")
				p.combineWarn = fmt.Sprintf("%s: %v", filepath.Base(path), lerr)
				return p
			}
			var p preparedInputs
			for j := range combinedMeta {
				combinedMeta[j].SourcePath = path
				st := mosaic.InputStatus{Path: mosaic.InputKey(combinedMeta[j]), Included: true, Status: fmt.Sprintf("combined (%d chips)", len(meta))}
				if !mosaic.LooksLikeCalibratedInput(path) {
					st.Status = "combined (warning: not _flc/_flt/_cal)"
				}
				p.inputs = append(p.inputs, combinedMeta[j])
				p.statuses = append(p.statuses, st)
			}
			debuglog.Log(fmt.Sprintf("loadPaths: %s combined %d chips (cached=%t)", filepath.Base(path), len(meta), cached))
			return p
		}

		debuglog.Log(fmt.Sprintf("loadPaths: combining %d multi-chip exposure(s) with %d worker(s)", multiTotal, combineWorkers))
		var combinesDone int64
		var next2 int64 = -1
		var wg2 sync.WaitGroup
		for w := 0; w < combineWorkers; w++ {
			wg2.Add(1)
			go func() {
				defer wg2.Done()
				for {
					i := int(atomic.AddInt64(&next2, 1))
					if i >= len(paths) {
						return
					}
					if pt.ctx.Err() != nil {
						return
					}
					path := paths[i]
					r := results[i]
					switch {
					case r.err != nil:
						debuglog.Log(fmt.Sprintf("loadPaths: %s FAILED: %v", filepath.Base(path), r.err))
						prep[i] = preparedInputs{statuses: []mosaic.InputStatus{{Path: path, Status: "failed", Error: r.err.Error()}}}
					case len(r.meta) <= 1:
						prep[i] = buildPerChip(path, r.meta, "loaded")
					default:
						prep[i] = combineOne(path, r.meta)
						n := atomic.AddInt64(&combinesDone, 1)
						pt.progress("Combining exposures", int(n), multiTotal)
					}
				}
			}()
		}
		wg2.Wait()

		cancelled := pt.ctx.Err() != nil

		// Flatten per-path results in order so ordering is deterministic, and
		// tally warnings from the status text (as the previous single-threaded
		// path did).
		newInputs := make([]mosaic.Input, 0, len(paths))
		newStatuses := make([]mosaic.InputStatus, 0, len(paths))
		warnings := 0
		combineWarnings := make([]string, 0)
		if !cancelled {
			for i := range prep {
				newInputs = append(newInputs, prep[i].inputs...)
				newStatuses = append(newStatuses, prep[i].statuses...)
				for _, st := range prep[i].statuses {
					if strings.Contains(st.Status, "warning") {
						warnings++
					}
				}
				if prep[i].combineWarn != "" {
					combineWarnings = append(combineWarnings, prep[i].combineWarn)
				}
			}
		}

		if cancelled {
			debuglog.Log("loadPaths: cancelled by user")
			fyne.Do(func() {
				pt.hide()
				dialog.ShowInformation("Mosaic Load", "Loading was cancelled. No files were added.", ws.win)
			})
			return
		}
		debuglog.Log(fmt.Sprintf("loadPaths: prepared %d input(s) from %d file(s) in %s", len(newInputs), len(paths), time.Since(startAll).Round(time.Millisecond)))

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

		messages := make([]string, 0, len(offsetMessages)+3)
		if warnings > 0 {
			messages = append(messages, "Some loaded files are not standard _flc/_flt/_cal inputs. They were kept, but this workflow is tuned for calibrated science exposures.")
		}
		if len(combineWarnings) > 0 {
			messages = append(messages, "Some exposures could not be combined and were loaded per-chip instead:\n  "+strings.Join(combineWarnings, "\n  "))
		}
		messages = append(messages, offsetMessages...)
		fyne.Do(func() {
			pt.hide()
			ws.state.inputs = append(ws.state.inputs, newInputs...)
			ws.state.statuses = append(ws.state.statuses, newStatuses...)
			// Re-sort the full inputs list so overall drizzle order is correct.
			if len(ws.state.inputs) > 1 {
				mosaic.SortInputsByWCSDistance(ws.state.inputs, ws.state.statuses)
			}
			// Saved transforms are applied only after the full input set has
			// been assembled and sorted, so the effective reference is known.
			sidecars := loadMosaicAlignmentSidecars(ws.state.inputs, ws.state.statuses, ws.state.referenceInput)
			if sidecars.ReferenceChangedNotice != "" {
				messages = append(messages, sidecars.ReferenceChangedNotice)
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

// loadWorkers picks a concurrency level for reading FITS files: one per CPU,
// but never more than the number of files to read.
func loadWorkers(n int) int {
	w := runtime.NumCPU()
	if w > n {
		w = n
	}
	if w < 1 {
		w = 1
	}
	return w
}

func (ws *mosaicWorkspace) configureLastDir(fd *dialog.FileDialog) {
	if last := ws.app.Preferences().String("lastDir"); last != "" {
		uri := storage.NewFileURI(last)
		if l, err := storage.ListerForURI(uri); err == nil {
			fd.SetLocation(l)
		}
	}
}

// sizeFileDialog gives file open/save dialogs a tall default so long file
// lists are visible without heavy scrolling. Fyne clamps this to the parent
// window if it is smaller.
func sizeFileDialog(fd *dialog.FileDialog) {
	fd.Resize(fyne.NewSize(1000, 800))
}
