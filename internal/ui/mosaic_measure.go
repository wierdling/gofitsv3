package ui

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/histogram"
	"gofitsv3/internal/mosaic"
)

func (ws *mosaicWorkspace) exitMeasureMode() {
	ws.activeMeasure = nil
	ws.previewSwap.Objects = []fyne.CanvasObject{ws.previewScroll}
	ws.previewSwap.Refresh()

	if ws.state.result != nil {
		black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
		img := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode, ws.mtfMidtone)
		ws.preview.Image = img
		ws.preview.Refresh()
		stats := histogram.Compute(ws.state.result.Pixels)
		ws.mosaicBins = stats.Hist
		ws.mosaicHistogram.Refresh()
	}

	ws.measurePanelScroll.Hide()
	ws.controlsScroll.Show()
	ws.leftStack.Refresh()
}

func (ws *mosaicWorkspace) showMeasureResultDialog(ptA, ptB measurePoint) {
	if ws.state.result == nil {
		return
	}
	scale := ws.state.result.Scale
	if scale <= 0 {
		scale = 1
	}
	dxOrig := (ptB.X - ptA.X) / scale
	dyOrig := (ptB.Y - ptA.Y) / scale

	// Build drizzle-order map: path → 1-based rank (1 = reference).
	drizzleRank := make(map[string]int, len(ws.state.inputs))
	if len(ws.state.inputs) > 0 {
		drizzleRank[mosaic.InputKey(ws.state.inputs[0])] = 1
		if len(ws.state.inputs) > 1 {
			for rank, idx := range mosaic.DrizzleOrder(ws.state.inputs) {
				if idx < len(ws.state.inputs) {
					drizzleRank[mosaic.InputKey(ws.state.inputs[idx])] = rank + 2
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

	pathsA := ws.pathsContainingPoint(ptA.X, ptA.Y)
	pathsB := ws.pathsContainingPoint(ptB.X, ptB.Y)

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
	entries := make([]inputEntry, len(ws.state.inputs))
	for i, inp := range ws.state.inputs {
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

	dlgHeight := ws.win.Canvas().Size().Height * 0.75
	if dlgHeight < 400 {
		dlgHeight = 400
	}
	dlg := dialog.NewCustomConfirm(
		"Measure Distance",
		"Apply & Drizzle",
		"Cancel",
		content,
		func(ok bool) {
			ws.exitMeasureMode()
			if !ok {
				return
			}
			applied := 0
			for _, e := range entries {
				if !e.checked {
					continue
				}
				idx := e.idx
				if idx >= len(ws.state.inputs) {
					continue
				}
				ws.state.inputs[idx].OffsetX += dxApply
				ws.state.inputs[idx].OffsetY += dyApply
				applied++
			}
			if applied == 0 {
				return
			}
			ws.rebuildOffsetControls()
			ws.updateStatus()
			// Start drizzle in background; progress dialog shows via fyne.DoAndWait.
			go ws.buildDrizzlePreview()
		},
		ws.win,
	)
	dlg.Resize(fyne.NewSize(460, dlgHeight))
	dlg.Show()
}

func (ws *mosaicWorkspace) enterMeasureMode() {
	if ws.state.result == nil {
		dialog.ShowInformation("No Drizzle Result", "Build a drizzle preview before measuring.", ws.win)
		return
	}

	// Capture scroll position and zoom before switching modes (item 6).
	savedScrollOffset := ws.previewScroll.Offset

	black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
	refImg := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode, ws.mtfMidtone)

	ws.activeMeasure = newMeasurePickerWidget(refImg, ws.state.result.Width, ws.state.result.Height)
	ws.activeMeasure.SetZoom(ws.zoomLevel)

	// Wire centroiding on the stretched preview image. The display-ready RGBA
	// has background compressed and stars as sharp bright peaks, giving much
	// better centroid accuracy than the raw float32 drizzle data.
	capturedMeasure := ws.activeMeasure
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
	applyCentroidFn(ws.measureCentroidCheck.Checked)
	ws.measureCentroidCheck.OnChanged = func(checked bool) {
		if capturedMeasure == ws.activeMeasure {
			applyCentroidFn(checked)
		}
	}

	ws.activeMeasure.OnStatus = func(msg string) {
		ws.measureStatusLabel.SetText(msg)
	}
	ws.activeMeasure.OnCancel = func() {
		ws.exitMeasureMode()
	}
	ws.activeMeasure.OnComplete = func(ptA, ptB measurePoint) {
		ws.showMeasureResultDialog(ptA, ptB)
	}

	ws.measureStatusLabel.SetText("Click point A on the image.")

	ws.pickerScroll.Content = ws.activeMeasure
	ws.pickerScroll.Offset = savedScrollOffset
	ws.pickerScroll.Refresh()
	ws.previewSwap.Objects = []fyne.CanvasObject{ws.pickerScroll}
	ws.previewSwap.Refresh()

	ws.controlsScroll.Hide()
	ws.measurePanelScroll.Show()
	ws.leftStack.Refresh()

	// Give focus to the widget so Escape key events are delivered.
	if c, ok := ws.win.Canvas().(interface{ Focus(fyne.Focusable) }); ok {
		c.Focus(ws.activeMeasure)
	}
}
