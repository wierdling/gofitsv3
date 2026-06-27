package ui

import (
	"errors"
	"fmt"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/mosaic"
)

// autoAlignToReferenceBaseline aligns active inputs against a set reference
// baseline only. Currently unused (kept for the baseline-alignment workflow).
func (ws *mosaicWorkspace) autoAlignToReferenceBaseline() {
	if ws.state.referenceInput == nil || len(ws.state.inputs) == 0 {
		return
	}
	alignInputs := ws.inputsWithRef()
	if len(alignInputs) < 2 {
		return
	}

	mode := mosaic.AlignmentMode(ws.state.alignmentSettings.AlignmentMode)
	searchRadius := ws.state.alignmentSettings.SearchRadiusArcsec
	debuglog.Log(fmt.Sprintf("buildDrizzlePreview: reference baseline set, auto-running AlignInputsByStarsWithMode against baseline only (mode=%d, searchRadius=%.2f)", int(mode), searchRadius))
	results, err := mosaic.AlignInputsByStarsWithMode(alignInputs, 1, mode, searchRadius)
	if err != nil {
		debuglog.Log(fmt.Sprintf("buildDrizzlePreview: auto reference star alignment failed: %v", err))
		return
	}

	activeIndices := make([]int, 0, len(ws.state.inputs))
	for i, inp := range ws.state.inputs {
		if !inp.Excluded {
			activeIndices = append(activeIndices, i)
		}
	}

	applied := 0
	failed := 0
	locked := 0
	for ri := 1; ri < len(results); ri++ {
		ai := ri - 1
		if ai >= len(activeIndices) {
			continue
		}
		si := activeIndices[ai]
		if si >= len(ws.state.inputs) {
			continue
		}
		if ws.state.inputs[si].OffsetLocked {
			locked++
			continue
		}
		if !results[ri].Applied {
			failed++
			debuglog.Log(fmt.Sprintf("buildDrizzlePreview: auto reference star alignment failed for %s: %s", mosaic.InputLabel(ws.state.inputs[si]), results[ri].Error))
			continue
		}

		ws.state.inputs[si].OffsetX = results[ri].OffsetX
		ws.state.inputs[si].OffsetY = results[ri].OffsetY
		ws.state.inputs[si].ManualTransform = results[ri].ManualTransform
		ws.state.inputs[si].HasManualTransform = results[ri].HasManualTransform
		applied++
		debuglog.Log(fmt.Sprintf("buildDrizzlePreview: auto reference star alignment applied to %s (x=%.2f, y=%.2f, affine=%v)", mosaic.InputLabel(ws.state.inputs[si]), results[ri].OffsetX, results[ri].OffsetY, results[ri].HasManualTransform))
	}
	debuglog.Log(fmt.Sprintf("buildDrizzlePreview: auto reference star alignment summary applied=%d failed=%d locked=%d", applied, failed, locked))
}

// buildDrizzlePreview runs a drizzle build and updates the preview UI.
// It must only be called from a goroutine (it shows a progress dialog and blocks).
func (ws *mosaicWorkspace) buildDrizzlePreview() {
	// Release previous result before building the new one so the old pixel
	// arrays can be collected before the new ones are allocated.
	ws.state.result = nil

	pt := newProgressTracker("Processing", "Aligning, cleaning, and drizzling selected inputs...", ws.win)

	// Temporarily disabled while checking whether baseline auto-alignment causes the chip-edge regression.
	// ws.autoAlignToReferenceBaseline()

	s := ws.state.drizzleSettings
	weightingMode := mosaic.WeightingMode(s.WeightingMode)
	if weightingMode == mosaic.WeightUniform && s.UseERRWeighting {
		weightingMode = mosaic.WeightERR
	}

	// Drop the full-resolution input arrays before drizzling. Build streams each
	// frame's pixels back from disk one at a time, so peak memory stays near
	// "one input + output" instead of holding all inputs at once. inputsWithRef
	// is captured after freeing, so it carries metadata only (nil pixels), which
	// triggers Build's on-demand disk loader.
	ws.freeInputPixels()
	buildInputs := ws.inputsWithRef()

	result, err := mosaic.Build(buildInputs, mosaic.Options{
		Scale:                 s.Scale,
		FinalScale:            s.FinalScale,
		PixFrac:               s.PixFrac,
		CRMethod:              mosaic.CRMethod(s.CRMethod),
		SepKernel:             mosaic.DrizzleKernel(s.SepKernel),
		FinalKernel:           mosaic.DrizzleKernel(s.FinalKernel),
		WeightingMode:         weightingMode,
		SurfaceBrightnessNorm: s.SurfaceBrightnessNorm,
		CRSeedSNR:             s.CRSeedSNR,
		CRDerivScale:          s.CRDerivScale,
		DebugOutputDir:        s.DebugOutputDir,
		Skysub:                skysubOptionsFromSettings(ws.state.skysubSettings),
		Progress:              pt.progress,
		Ctx:                   pt.ctx,
	})

	pt.hide()

	if err != nil {
		if errors.Is(err, mosaic.ErrCancelled) {
			debuglog.Log("buildDrizzlePreview: cancelled by user")
			return
		}
		fyne.Do(func() { dialog.ShowError(err, ws.win) })
		return
	}

	debuglog.Log(fmt.Sprintf("buildDrizzlePreview: Build done, result %dx%d (%d pixels)", result.Width, result.Height, len(result.Pixels)))
	ws.state.result = result

	// Do CPU-heavy work off the main thread before touching any UI.
	if !ws.levelsSet {
		debuglog.Log("buildDrizzlePreview: autoLevels (off main thread)")
		ws.autoLevels(result.Pixels)
	}
	debuglog.Log("buildDrizzlePreview: buildMosaicPreviewImageWithLevels (off main thread)")
	black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
	previewImg := buildMosaicPreviewImageWithLevels(result, black, white, bg, peak, scaledPeak, ws.stretchMode)
	debuglog.Log("buildDrizzlePreview: histogram.Compute (off main thread)")
	stats := histogram.Compute(result.Pixels)

	debuglog.Log("buildDrizzlePreview: submitting UI update to main thread")
	fyne.Do(func() {
		debuglog.Log("buildDrizzlePreview: UI update start (on main thread)")
		ws.state.statuses = result.Inputs
		ws.saveBtn.Enable()
		ws.sendToExamineBtn.Enable()
		debuglog.Log("buildDrizzlePreview: rebuildOffsetControls")
		ws.rebuildOffsetControls()
		debuglog.Log("buildDrizzlePreview: updateStatus")
		ws.updateStatus()
		debuglog.Log("buildDrizzlePreview: preview.Refresh")
		ws.preview.Image = previewImg
		ws.preview.Refresh()
		ws.mosaicBins = stats.Hist
		ws.mosaicHistogram.Refresh()
		ws.statsLabel.SetText(fmt.Sprintf("Mean: %.4f | Std: %.4f | Size: %dx%d", stats.Mean, stats.Std, result.Width, result.Height))
		debuglog.Log("buildDrizzlePreview: updateZoom")
		ws.updateZoom()
		debuglog.Log("buildDrizzlePreview: UI update done")
	})

	if ws.state.savePreview {
		debuglog.Log("buildDrizzlePreview: saving preview FITS")
		filter, dir, ok := ws.currentFilterAndDir()
		if !ok {
			fyne.Do(func() {
				dialog.ShowInformation("Preview Save Skipped", "Automatic preview save requires the loaded files to come from one directory and one filter.", ws.win)
			})
			return
		}
		previewPath := filepath.Join(dir, filter+"_preview.fits")
		if err := mosaic.SaveResultFITS(previewPath, result); err != nil {
			fyne.Do(func() { dialog.ShowError(err, ws.win) })
			return
		}
		debuglog.Log("buildDrizzlePreview: preview FITS saved")
		fyne.Do(func() {
			dialog.ShowInformation("Preview Saved", fmt.Sprintf("Saved preview FITS to %s.", filepath.Base(previewPath)), ws.win)
		})
	}
}
