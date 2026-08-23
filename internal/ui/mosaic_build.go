package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/stretch"
)

func drizzleOptionsFromSettings(s models.DrizzleSettings, sky models.SkysubSettings, debugDir string, progress func(string, int, int), ctx context.Context) mosaic.Options {
	weightingMode := mosaic.WeightingMode(s.WeightingMode)
	if weightingMode == mosaic.WeightUniform && s.UseERRWeighting {
		weightingMode = mosaic.WeightERR
	}
	return mosaic.Options{
		Scale:                 s.Scale,
		FinalScale:            s.FinalScale,
		LockToReferenceFrame:  s.LockToReferenceFrame,
		PixFrac:               s.PixFrac,
		CRMethod:              mosaic.CRMethod(s.CRMethod),
		SepKernel:             mosaic.DrizzleKernel(s.SepKernel),
		FinalKernel:           mosaic.DrizzleKernel(s.FinalKernel),
		WeightingMode:         weightingMode,
		SurfaceBrightnessNorm: s.SurfaceBrightnessNorm,
		CRSeedSNR:             s.CRSeedSNR,
		CRDerivScale:          s.CRDerivScale,
		DebugOutputDir:        debugDir,
		Skysub:                skysubOptionsFromSettings(sky),
		Progress:              progress,
		Ctx:                   ctx,
	}
}

// autoAlignToReferenceBaseline aligns active inputs against a set reference
// baseline only. Currently unused (kept for the baseline-alignment workflow).
func (ws *mosaicWorkspace) autoAlignToReferenceBaseline() {
	if ws.state.referenceInput == nil || len(ws.state.inputs) == 0 {
		return
	}
	alignInputs, stateIndices := ws.alignmentWorkset()
	if len(alignInputs) < 2 {
		return
	}

	mode := mosaic.AlignmentMode(ws.state.alignmentSettings.AlignmentMode)
	searchRadius := ws.state.alignmentSettings.SearchRadiusArcsec
	debuglog.Log(fmt.Sprintf("buildDrizzlePreview: reference baseline set, auto-running AlignInputsByStarsWithMode against baseline only (mode=%d, searchRadius=%.2f)", int(mode), searchRadius))
	results, err := mosaic.AlignInputsByStarsWithMode(alignInputs, ws.alignmentNumRefs(), mode, searchRadius)
	if err != nil {
		debuglog.Log(fmt.Sprintf("buildDrizzlePreview: auto reference star alignment failed: %v", err))
		return
	}

	applied := 0
	failed := 0
	for ri := 1; ri < len(results); ri++ {
		if ri >= len(stateIndices) {
			continue
		}
		si := stateIndices[ri]
		if si < 0 || si >= len(ws.state.inputs) {
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
	debuglog.Log(fmt.Sprintf("buildDrizzlePreview: auto reference star alignment summary applied=%d failed=%d", applied, failed))
}

// buildDrizzlePreview runs a drizzle build and updates the preview UI.
// It must only be called from a goroutine (it shows a progress dialog and blocks).
func (ws *mosaicWorkspace) buildDrizzlePreview() {
	buildCtx, buildGeneration, started := ws.beginMosaicBuild()
	if !started {
		return
	}
	pt := newProgressTracker("Processing", "Aligning, cleaning, and drizzling selected inputs...", ws.win)
	queued := false
	defer func() {
		pt.hide()
		if !queued {
			ws.finishMosaicBuild(buildGeneration)
		}
	}()
	go func() {
		select {
		case <-pt.ctx.Done():
			ws.cancelMosaicBuild()
		case <-buildCtx.Done():
		}
	}()

	// Temporarily disabled while checking whether baseline auto-alignment causes the chip-edge regression.
	// ws.autoAlignToReferenceBaseline()

	// Capture settings and level state on the Fyne thread before doing any
	// background work. Widgets and mutable workspace fields must not be read
	// concurrently with callbacks that edit them.
	var s models.DrizzleSettings
	var buildSkysubSettings models.SkysubSettings
	var debugDir, projectPath string
	var levelsSet bool
	var black, white, bg, peak, scaledPeak float64
	var mode stretch.Mode
	var mtf float64
	var buildInputs []mosaic.Input
	var savePreview bool
	var previewFilter, previewDir string
	var previewDestinationOK bool
	fyne.DoAndWait(func() {
		s = ws.state.drizzleSettings
		buildSkysubSettings = ws.state.skysubSettings
		debugDir, projectPath = s.DebugOutputDir, ws.currentProjectPath
		levelsSet = ws.levelsSet
		black, white, bg, peak, scaledPeak = ws.parseLevelEntries()
		mode, mtf = ws.stretchMode, ws.mtfMidtone
		inputs := append([]mosaic.Input(nil), ws.state.inputs...)
		var ref *mosaic.Input
		if ws.state.referenceInput != nil {
			r := *ws.state.referenceInput
			ref = &r
		}
		buildInputs = inputsWithRefFor(inputs, ref)
		for i := range buildInputs {
			buildInputs[i].HDU.Data.Pixels = nil
			buildInputs[i].ERRPixels = nil
			buildInputs[i].WeightPixels = nil
		}
		savePreview = ws.state.savePreview
		if savePreview {
			previewFilter, previewDir, previewDestinationOK = ws.currentFilterAndDir()
		}
	})

	// Drop the full-resolution input arrays before drizzling. Build streams each
	// frame's pixels back from disk one at a time, so peak memory stays near
	// "one input + output" instead of holding all inputs at once. inputsWithRef
	// is captured after freeing, so it carries metadata only (nil pixels), which
	// triggers Build's on-demand disk loader.
	buildSkysubSettings = resolveSkysubSettingsForProject(buildSkysubSettings, projectPath)
	options := drizzleOptionsFromSettings(s, buildSkysubSettings, debugDir, pt.progress, buildCtx)
	options.Skysub = skysubOptionsFromSettings(buildSkysubSettings)
	result, err := mosaic.Build(buildInputs, options)

	pt.hide()

	if err != nil {
		if errors.Is(err, mosaic.ErrCancelled) {
			ws.finishMosaicBuild(buildGeneration)
			debuglog.Log("buildDrizzlePreview: cancelled by user")
			return
		}
		ws.finishMosaicBuild(buildGeneration)
		fyne.Do(func() { dialog.ShowError(err, ws.win) })
		return
	}

	debuglog.Log(fmt.Sprintf("buildDrizzlePreview: Build done, result %dx%d (%d pixels)", result.Width, result.Height, len(result.Pixels)))
	// Do CPU-heavy work off the main thread before touching any UI.
	var auto *models.LoadedImage
	if !levelsSet {
		debuglog.Log("buildDrizzlePreview: autoLevels (off main thread)")
		v := autoLevelsForPixels(result.Pixels)
		auto = &v
		black, white, bg, peak, scaledPeak = v.Black, v.White, v.Background, v.Peak, v.ScaledPeak
	}
	debuglog.Log("buildDrizzlePreview: buildMosaicPreviewImageWithLevels (off main thread)")
	previewImg := buildMosaicPreviewImageWithLevels(result, black, white, bg, peak, scaledPeak, mode, mtf)
	debuglog.Log("buildDrizzlePreview: histogram.Compute (off main thread)")
	stats := histogram.Compute(result.Pixels)

	debuglog.Log("buildDrizzlePreview: submitting UI update to main thread")
	queued = true
	fyne.Do(func() {
		if buildCtx.Err() != nil || !ws.finishMosaicBuild(buildGeneration) {
			return
		}
		ws.state.result = result
		ws.state.resultName = ""
		markMosaicArtifactMaskDocumentsStale(ws.state.artifactMasks)
		if auto != nil {
			ws.blackEntry.SetValue(auto.Black)
			ws.whiteEntry.SetValue(auto.White)
			ws.bgEntry.SetValue(auto.Background)
			ws.peakEntry.SetValue(auto.Peak)
			ws.scaledPeakEntry.SetValue(auto.ScaledPeak)
			ws.levelsSet = true
		}
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
		ws.statsLabel.SetText(mosaicStatsText(ws.state.resultName, stats.Mean, stats.Std, result.Width, result.Height))
		debuglog.Log("buildDrizzlePreview: updateZoom")
		ws.updateZoom()
		debuglog.Log("buildDrizzlePreview: UI update done")
	})

	if savePreview {
		debuglog.Log("buildDrizzlePreview: saving preview FITS")
		if !previewDestinationOK {
			fyne.Do(func() {
				dialog.ShowInformation("Preview Save Skipped", "Automatic preview save requires the loaded files to come from one directory and one filter.", ws.win)
			})
			return
		}
		previewPath := filepath.Join(previewDir, previewFilter+"_preview.fits")
		if err := mosaic.SaveResultFITS(previewPath, result); err != nil {
			fyne.Do(func() { dialog.ShowError(err, ws.win) })
			return
		}
		debuglog.Log("buildDrizzlePreview: preview FITS saved")
		fyne.Do(func() {
			ws.state.resultName = filepath.Base(previewPath)
			ws.statsLabel.SetText(mosaicStatsText(ws.state.resultName, stats.Mean, stats.Std, result.Width, result.Height))
			dialog.ShowInformation("Preview Saved", fmt.Sprintf("Saved preview FITS to %s.", filepath.Base(previewPath)), ws.win)
		})
	}
}
