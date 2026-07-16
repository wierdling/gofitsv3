package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"gofitsv3/internal/histogram"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

func (ws *mosaicWorkspace) exitStarMode() {
	ws.activePicker = nil
	ws.starModeRefResult = nil
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
		ws.statsLabel.SetText(fmt.Sprintf("Mean: %.4f | Std: %.4f | Size: %dx%d", stats.Mean, stats.Std, ws.state.result.Width, ws.state.result.Height))
	} else {
		ws.statsLabel.SetText("Mean: -- | Std: -- | Size: --")
		ws.mosaicBins = [256]int{}
		ws.mosaicHistogram.Refresh()
	}

	ws.starPanelScroll.Hide()
	ws.controlsScroll.Show()
	ws.leftStack.Refresh()
}

func (ws *mosaicWorkspace) enterStarMode() {
	if len(ws.state.inputs) == 0 {
		dialog.ShowInformation("No Files", "Load at least one FITS file before selecting stars.", ws.win)
		return
	}
	var refResult *mosaic.Result
	if ws.state.result != nil {
		refResult = ws.state.result
	} else {
		// No drizzle result yet: the reference preview comes from input[0]'s
		// pixels, which may be unloaded (metadata-only load) or freed by a prior
		// build. Reload only that one frame so the whole dataset stays on disk.
		if err := ws.ensureInputPixelsLoadedAt(0); err != nil {
			dialog.ShowError(err, ws.win)
			return
		}
		ref := ws.state.inputs[0]
		refResult = &mosaic.Result{
			Pixels: ref.HDU.Data.Pixels,
			Width:  ref.HDU.Data.Width,
			Height: ref.HDU.Data.Height,
		}
	}
	ws.starModeRefResult = refResult
	if !ws.levelsSet {
		ws.autoLevels(refResult.Pixels)
	}
	black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
	refImg := buildMosaicPreviewImageWithLevels(refResult, black, white, bg, peak, scaledPeak, ws.stretchMode, ws.mtfMidtone)

	ws.activePicker = newStarPickerWidget(refImg, refResult.Width, refResult.Height)
	ws.activePicker.SetZoom(ws.zoomLevel)
	ws.activePicker.OnChanged = func() {
		n := len(ws.activePicker.Stars)
		ws.starCountLabel.SetText(fmt.Sprintf("Selected: %d / %d stars", n, ws.activePicker.MaxStars))
	}
	// Capture pixels for centroiding (copy slice header; pixels are not modified).
	centPixels := refResult.Pixels
	centW, centH := refResult.Width, refResult.Height
	ws.activePicker.CentroidFn = func(x, y float64) (float64, float64, bool) {
		return processing.CentroidNear(centPixels, centW, centH, x, y, 15)
	}

	ws.starCountLabel.SetText("Selected: 0 / 50 stars")

	ws.pickerScroll.Content = ws.activePicker
	ws.pickerScroll.Refresh()
	ws.previewSwap.Objects = []fyne.CanvasObject{ws.pickerScroll}
	ws.previewSwap.Refresh()

	ws.statsLabel.SetText("Click on stars in the reference image. Right-click to remove.")

	ws.controlsScroll.Hide()
	ws.starPanelScroll.Show()
	ws.leftStack.Refresh()
}
