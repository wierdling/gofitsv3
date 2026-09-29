package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

// Updated signature to pass the stats
func (ws *composeWorkspace) pushRGBHist(stats [3]histogram.Stats) {
	ws.latestRGBStats = stats
	if ws.levelsWin != nil {
		ws.levelsWin.setHistogram(stats)
	}
}

// startGeneration cancels any in-flight compose generation and starts a
// fresh one in the background. Triggering this repeatedly in quick
// succession (e.g. dragging a slider) kills the stale generation's work
// early via ctx rather than waiting for it to finish and discarding the
// result, since a single compose pass can take up to ~30s on large mosaics.
func (ws *composeWorkspace) startGeneration(onDone func()) {
	if ws.suspendRefresh {
		if onDone != nil {
			onDone()
		}
		return
	}
	// Attach each source's current gentler-star-stretch model (or none while
	// one is being prepared) before the snapshot is taken.
	ws.starTreatments.sync(ws.imgs, false)
	ws.previewMu.Lock()
	if ws.genCancel != nil {
		ws.genCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	ws.genCancel = cancel
	ws.previewSeq++
	seq := ws.previewSeq
	// Invalidate frames from the superseded generation immediately. Keep the
	// active ticker sequence intact: ordinary preview refreshes must resume
	// cycling as soon as replacement frames are applied. stopBlink remains
	// the explicit ticker invalidation path.
	ws.blinkMu.Lock()
	ws.blinkPrepared = nil
	ws.blinkMu.Unlock()
	ws.previewMu.Unlock()
	ws.lrgbMu.RLock()
	lrgbSnapshot := composeLRGBSnapshot{settings: ws.lrgbSettings, dedicated: ws.dedicatedL, generation: ws.lrgbGeneration}
	ws.lrgbMu.RUnlock()
	ctx = context.WithValue(ctx, composeLRGBContextKey{}, lrgbSnapshot)
	imgSnapshot := append([]*models.LoadedImage(nil), ws.imgs...)
	if !ws.largeMode {
		imgSnapshot = ws.renderImages()
	}
	blinkSourcesSnapshot := ws.composeBlinkSources()
	blinkSelectionSnapshot := append([]int(nil), ws.blinkChannels...)
	blinkEnabled := ws.blinkCheck.Checked && !ws.largeMode
	if blinkEnabled && ws.blinkChannels == nil {
		blinkSelectionSnapshot = resolveComposeBlinkSelection(blinkSourcesSnapshot, nil, true, ws.blinkExcludedIdx)
	}
	if blinkEnabled && len(filterComposeBlinkSelection(blinkSelectionSnapshot, blinkSourcesSnapshot)) < 2 {
		blinkSelectionSnapshot = nil
	}
	levelsSnapshot := *ws.levels
	sharedHistScale := ws.sharedHistCheck.Checked
	buildComposite := ws.buildCompositeCheck.Checked
	compositeRenderActive := ws.largeMode && buildComposite && len(imgSnapshot) >= 3 &&
		imgSnapshot[0] != nil && imgSnapshot[1] != nil && imgSnapshot[2] != nil
	priorCompositeImage, _ := ws.viewports[3].image.Image.(*image.RGBA)
	priorCompositeStats := ""
	if ws.viewports[3].StatsLabel != nil {
		priorCompositeStats = ws.viewports[3].StatsLabel.Text
	}
	priorCompositeW, priorCompositeH := ws.viewports[3].origW, ws.viewports[3].origH
	priorCompositeBins, priorCompositeHistMax := ws.viewports[3].bins, ws.viewports[3].histMax
	priorCompositeBlack, priorCompositeWhite := ws.viewports[3].blackBox.Value(), ws.viewports[3].whiteBox.Value()
	// Keep the indicator in the viewport header so the image and its
	// interaction layer remain usable while the disk compositor runs.
	if ws.largeMode {
		statusText := composeCompositeDisabledStatus(buildComposite, imgSnapshot)
		if compositeRenderActive {
			statusText = "Rendering disk-backed color composite…"
		}
		// Publish the in-progress/disabled state before the worker starts. This
		// deliberately changes only the tile label, retaining the previous
		// image until a current generation produces a replacement.
		fyne.Do(func() {
			ws.viewports[3].SetStatsText(statusText)
			ws.viewports[3].SetCompositeRendering(compositeRenderActive)
		})
	} else {
		fyne.Do(func() { ws.viewports[3].SetCompositeRendering(false) })
	}
	ws.largeMu.RLock()
	largePreviewSnapshot := make(map[int]*image.RGBA, len(ws.largePreviews))
	for i, p := range ws.largePreviews {
		largePreviewSnapshot[i] = p
	}
	ws.largeMu.RUnlock()
	go func() {
		start := time.Now()
		debuglog.Log("compose refresh async: starting preview computation")
		var data composePreviewData
		if ws.largeMode {
			for i := range data.Views {
				data.Views[i] = composeViewportPreview{Image: blankImg(), StatsText: "Disk-backed preview"}
			}
			data.Views[3] = composeViewportPreview{Image: priorCompositeImage, Bins: priorCompositeBins, HistMax: priorCompositeHistMax, OrigW: priorCompositeW, OrigH: priorCompositeH, Black: priorCompositeBlack, White: priorCompositeWhite, StatsText: priorCompositeStats}
			for i := range imgSnapshot {
				if i >= len(data.Views) {
					break
				}
				if p := largePreviewSnapshot[i]; p != nil {
					data.Views[i] = composeViewportPreview{Image: p, OrigW: imgSnapshot[i].HDU.Data.Width, OrigH: imgSnapshot[i].HDU.Data.Height, StatsText: "Disk-backed preview"}
				}
			}
			if buildComposite && composeHasAllBaseChannels(imgSnapshot) {
				b, w, h, s, e := ws.composeRGB(ctx)
				if e == nil && len(b) > 0 {
					data.RGBStats = s
					pw, ph := w, h
					if len(b) != w*h*4 {
						scale := math.Sqrt(float64(len(b)/4) / float64(w*h))
						pw, ph = int(math.Max(1, math.Round(float64(w)*scale))), int(math.Max(1, math.Round(float64(h)*scale)))
					}
					lumaStats := histogramRGBLuminance(b)
					data.Views[3] = composeViewportPreview{Image: image.NewRGBA(image.Rect(0, 0, pw, ph)), OrigW: w, OrigH: h, StatsText: fmt.Sprintf("Luma μ %.1f  σ %.1f", lumaStats.Mean, lumaStats.Std)}
					copy(data.Views[3].Image.Pix, b)
				} else {
					if e != nil {
						data.Views[3].StatsText = fmt.Sprintf("Composite render failed: %v", e)
					} else {
						data.Views[3].StatsText = "Composite render failed: empty result"
					}
				}
			} else {
				data.Views[3] = composeViewportPreview{Image: blankImg(), StatsText: composeCompositeDisabledStatus(buildComposite, imgSnapshot)}
			}
		} else {
			data = buildComposePreviewData(ctx, imgSnapshot, sharedHistScale, buildComposite, &levelsSnapshot, func(c context.Context) ([]byte, int, int, [3]histogram.Stats, error) {
				b, w, h, s, e := ws.composeRGB(c)
				return b, w, h, s, e
			})
		}
		if blinkEnabled {
			data.BlinkFrames = buildComposeBlinkFrames(ctx, imgSnapshot, blinkSourcesSnapshot, blinkSelectionSnapshot, data.Views)
		}
		debuglog.Log(fmt.Sprintf("compose refresh async: preview computation took %s", time.Since(start)))
		fyne.Do(func() {
			ws.previewMu.Lock()
			currentSeq := ws.previewSeq
			ws.previewMu.Unlock()
			if seq != currentSeq || ctx.Err() != nil {
				debuglog.Log("compose refresh async: skipped stale/canceled preview result")
				if seq == currentSeq && ctx.Err() != nil {
					ws.viewports[3].SetCompositeRendering(false)
				}
				if onDone != nil {
					onDone()
				}
				return
			}
			// The toggle can change while a disk-backed preview is being
			// assembled. Do not install a result that was computed for the
			// previous toggle state; previewSeq normally handles this, but this
			// additional UI-thread check also covers queued UI refreshes.
			if buildComposite != ws.buildCompositeCheck.Checked {
				debuglog.Log("compose refresh async: rerendering after composite toggle change")
				ws.refresh()
				if onDone != nil {
					onDone()
				}
				return
			}
			// This generation is now either applied or has produced its final
			// error result; a newer generation, if any, owns the indicator.
			ws.viewports[3].SetCompositeRendering(false)
			applyComposePreviewData(data, ws.viewports, ws.pushRGBHist)
			ws.recomputeStarDiagnostics()
			ws.blinkMu.Lock()
			ws.blinkPrepared = append([]composeBlinkFrame(nil), data.BlinkFrames...)
			ws.blinkMu.Unlock()
			ws.refreshBlinkFrame()
			debuglog.Log("compose refresh async: applied preview result")
			if onDone != nil {
				onDone()
			}
		})
	}()
}

func (ws *composeWorkspace) refresh() {
	ws.startGeneration(nil)
}

func (ws *composeWorkspace) refreshAsync(onDone func()) {
	ws.startGeneration(onDone)
}

func (ws *composeWorkspace) withSuspendedRefresh(fn func()) {
	prev := ws.suspendRefresh
	ws.suspendRefresh = true
	defer func() { ws.suspendRefresh = prev }()
	fn()
}

func (ws *composeWorkspace) composeRGB(ctx context.Context) ([]byte, int, int, [3]histogram.Stats, error) {
	if ws.largeMode {
		if ws.largeStore == nil || len(ws.imgs) < 3 || ws.imgs[0] == nil || ws.imgs[1] == nil || ws.imgs[2] == nil {
			return nil, 0, 0, [3]histogram.Stats{}, errors.New("all three channels are required for disk-backed Compose")
		}
		ws.largeMu.RLock()
		expectedCompositeGeneration := uint64(0)
		if currentComposite, ok := ws.largeStore.Composite(); ok {
			expectedCompositeGeneration = currentComposite.Generation
		}
		var channels [3]processing.DiskChannel
		outW, outH := 0, 0
		for i := 0; i < 3; i++ {
			d, ok := ws.largeArtifacts[i]
			if !ok || d.Path == "" {
				ws.largeMu.RUnlock()
				return nil, 0, 0, [3]histogram.Stats{}, fmt.Errorf("missing disk artifact for channel %d", i)
			}
			if i == 1 {
				outW, outH = d.Width, d.Height // Channel 2 defines the output grid
			}
			dx, dy, rot, _ := ws.composeChannelOffsetFields(i)
			channels[i] = processing.DiskChannel{ArtifactPath: d.Path, Image: *ws.imgs[i], OffsetX: dx, OffsetY: dy, OffsetRot: rot}
		}
		ws.largeMu.RUnlock()
		// Every render owns a unique output set. This prevents a cancelled or
		// superseded job from reopening/overwriting the currently displayed
		// composite while another job is still preparing its rows.
		renderID := time.Now().UnixNano()
		outs := [3]string{
			filepath.Join(ws.largeStore.root, fmt.Sprintf("composite-r-%d.bin", renderID)),
			filepath.Join(ws.largeStore.root, fmt.Sprintf("composite-g-%d.bin", renderID)),
			filepath.Join(ws.largeStore.root, fmt.Sprintf("composite-b-%d.bin", renderID)),
		}
		ovs := make([]processing.DiskOverlay, 0, len(ws.overlayLayers))
		ws.largeMu.RLock()
		for _, l := range ws.overlayLayers {
			if l == nil || l.idx >= len(ws.imgs) || ws.imgs[l.idx] == nil {
				continue
			}
			if d, ok := ws.largeArtifacts[l.idx]; ok {
				dx, dy, rot, _ := ws.composeChannelOffsetFields(l.idx)
				ovs = append(ovs, processing.DiskOverlay{Channel: processing.DiskChannel{ArtifactPath: d.Path, Image: *ws.imgs[l.idx], OffsetX: dx, OffsetY: dy, OffsetRot: rot}, Settings: l.settings})
			}
		}
		ws.largeMu.RUnlock()
		lrgbSnapshot := composeLRGBSnapshot{}
		ws.lrgbMu.RLock()
		lrgbSnapshot.settings = ws.lrgbSettings
		ws.lrgbMu.RUnlock()
		whitening, err := ws.buildStarNeutralizer(outW, outH)
		if err != nil {
			return nil, 0, 0, [3]histogram.Stats{}, err
		}
		result, err := processing.ComposeDisk(ctx, processing.DiskComposeRequest{Channels: channels, Overlays: ovs, Output: outs, PreviewMax: 1600, RGBLevels: ws.levels, CompositionMode: ws.compositionMode, MixWeights: append([]models.ComposeMixWeight(nil), ws.mixWeights...), LRGB: lrgbSnapshot.settings, StarWhitening: whitening})
		if err != nil {
			for _, p := range outs {
				_ = os.Remove(p)
			}
			return nil, 0, 0, [3]histogram.Stats{}, err
		}
		if _, err := ws.largeStore.PublishCompositeIfCurrent(expectedCompositeGeneration, outs, result.Width, result.Height); err != nil {
			for _, p := range outs {
				_ = os.Remove(p)
			}
			return nil, 0, 0, [3]histogram.Stats{}, err
		}
		return result.Preview, result.Width, result.Height, result.Stats, nil
	}
	ws.lrgbMu.RLock()
	lrgbSnapshot, hasLRGBSnapshot := ctx.Value(composeLRGBContextKey{}).(composeLRGBSnapshot)
	if !hasLRGBSnapshot {
		lrgbSnapshot = composeLRGBSnapshot{settings: ws.lrgbSettings, dedicated: ws.dedicatedL, generation: ws.lrgbGeneration}
	}
	ws.lrgbMu.RUnlock()
	var weightedOverlays []processing.OverlayLayer
	var artisticOverlays []processing.OverlayLayer
	dedicated := lrgbSnapshot.dedicated
	if dedicated == nil && lrgbSnapshot.settings.DedicatedLPath != "" {
		img, err := loadImageFromPath(lrgbSnapshot.settings.DedicatedLPath)
		if err != nil {
			return nil, 0, 0, [3]histogram.Stats{}, fmt.Errorf("load dedicated luminance: %w", err)
		}
		applyChannelStateToImage(img, lrgbSnapshot.settings.DedicatedLState)
		dedicated = img
		ws.lrgbMu.Lock()
		if composeLRGBPublishAllowed(ws.lrgbGeneration, lrgbSnapshot.generation, ws.lrgbSettings.DedicatedLPath, lrgbSnapshot.settings.DedicatedLPath) {
			ws.dedicatedL = img
		}
		ws.lrgbMu.Unlock()
	}
	renderedSources := ws.renderImages()
	composeSources := renderedSources
	if ws.psfSettings.Enabled {
		composeSources = processing.ApplyPSFMatching(composeSources, processing.PSFTarget{FWHMX: ws.psfSettings.TargetFWHMX, FWHMY: ws.psfSettings.TargetFWHMY}, ws.psfSettings.ProtectSaturated, ws.psfSettings.Saturation)
	}
	weightedOverlays, artisticOverlays = composeOverlaySources(renderedSources, composeSources, ws.overlayLayers, func(layer *overlayLayer) bool {
		return layer.win != nil
	})
	weightedCount := 0
	for i := 0; i < 3 && i < len(composeSources); i++ {
		if composeSources[i] != nil {
			weightedCount++
		}
	}
	for _, overlay := range weightedOverlays {
		if overlay.Image != nil {
			weightedCount++
		}
	}
	if (models.ComposeProject{CompositionMode: ws.compositionMode}).ResolveComposeMode(weightedCount) == models.ComposeModeWeighted {
		rgb, w, h, err := processing.ComposeWeightedRGBPlanes(ctx, composeSources, weightedOverlays, ws.mixWeights)
		if err != nil {
			return nil, 0, 0, [3]histogram.Stats{}, err
		}
		if whitening, err := ws.buildStarNeutralizer(w, h); err != nil {
			return nil, 0, 0, [3]histogram.Stats{}, err
		} else if whitening != nil {
			if err := whitening.Apply(ctx, rgb); err != nil {
				return nil, 0, 0, [3]histogram.Stats{}, err
			}
		}
		if dedicated != nil && lrgbSnapshot.settings.Enabled && lrgbSnapshot.settings.LuminanceWeight > 0 {
			ld := processing.StretchedImageDataForReferenceGrid(dedicated, composeSources[1])
			planes, err := processing.ComposeLRGB(ctx, rgb[0], rgb[1], rgb[2], ld.Pixels, w, h, processing.LRGBConfig{
				LuminanceWeight: lrgbSnapshot.settings.LuminanceWeight, ChrominanceSmoothing: lrgbSnapshot.settings.ChrominanceSmoothing,
				SyntheticWeights: lrgbSnapshot.settings.SyntheticWeights, UseDedicatedLuminance: true,
			})
			if err != nil {
				return nil, 0, 0, [3]histogram.Stats{}, err
			}
			rgb = [3][]float32{planes[:w*h], planes[w*h : 2*w*h], planes[2*w*h:]}
		} else if lrgbSnapshot.settings.Enabled {
			planes, err := processing.ComposeLRGB(ctx, rgb[0], rgb[1], rgb[2], nil, w, h, processing.LRGBConfig{
				LuminanceWeight: lrgbSnapshot.settings.LuminanceWeight, ChrominanceSmoothing: lrgbSnapshot.settings.ChrominanceSmoothing,
				SyntheticWeights: lrgbSnapshot.settings.SyntheticWeights,
			})
			if err != nil {
				return nil, 0, 0, [3]histogram.Stats{}, err
			}
			rgb = [3][]float32{planes[:w*h], planes[w*h : 2*w*h], planes[2*w*h:]}
		}
		b, err := processing.Float32RGBToRGBA(rgb, w, h)
		if err != nil {
			return nil, 0, 0, [3]histogram.Stats{}, err
		}
		return b, w, h, processing.HistogramRGB(b), nil
	}
	if len(artisticOverlays) > 0 {
		b, w, h, s := processing.ComposeRGBWithOverlays(ctx, composeSources, artisticOverlays)
		var err error
		if b, s, err = whitenComposeRGBA(ctx, ws.buildStarNeutralizer, b, w, h, s); err != nil {
			return nil, 0, 0, [3]histogram.Stats{}, err
		}
		if dedicated != nil {
			b, _ = ws.applyDedicatedL(b, w, h, lrgbSnapshot.settings, dedicated)
		} else if lrgbSnapshot.settings.Enabled {
			b = processing.ApplyLRGBToRGBA(b, w, h, processing.LRGBConfig{LuminanceWeight: lrgbSnapshot.settings.LuminanceWeight, ChrominanceSmoothing: lrgbSnapshot.settings.ChrominanceSmoothing, SyntheticWeights: lrgbSnapshot.settings.SyntheticWeights})
		}
		return b, w, h, s, nil
	}
	b, w, h, s := processing.ComposeRGB(ctx, composeSources)
	var err error
	if b, s, err = whitenComposeRGBA(ctx, ws.buildStarNeutralizer, b, w, h, s); err != nil {
		return nil, 0, 0, [3]histogram.Stats{}, err
	}
	if dedicated != nil {
		b, _ = ws.applyDedicatedL(b, w, h, lrgbSnapshot.settings, dedicated)
	} else if lrgbSnapshot.settings.Enabled {
		b = processing.ApplyLRGBToRGBA(b, w, h, processing.LRGBConfig{LuminanceWeight: lrgbSnapshot.settings.LuminanceWeight, ChrominanceSmoothing: lrgbSnapshot.settings.ChrominanceSmoothing, SyntheticWeights: lrgbSnapshot.settings.SyntheticWeights})
	}
	return b, w, h, s, nil
}

// renderImage returns imgs[idx] with the channel's Manual Offset applied at
// render time (or imgs[idx] unchanged when there is no offset). It never
// mutates imgs[idx].
func (ws *composeWorkspace) renderImage(idx int) *models.LoadedImage {
	if idx < 0 || idx >= len(ws.imgs) || ws.imgs[idx] == nil {
		return nil
	}
	dx, dy, rot, ok := ws.composeChannelOffsetFields(idx)
	align, hasAlign := channelAlignTransform(ws.imgs[idx])
	if (!ok || (dx == 0 && dy == 0 && rot == 0)) && !hasAlign {
		return ws.imgs[idx]
	}
	src := ws.imgs[idx].HDU.Data.Pixels
	// A gentler star stretch is defined on the source grid, so a treated
	// channel is stretched first and its stretched result is warped; the
	// clone then carries an identity stretch so the compositor does not
	// stretch it again. Untreated channels warp linear data as before.
	treatment := ws.imgs[idx].StarTreatment
	treated := false
	if treatment != nil {
		if data, err := processing.TreatedStretchForSource(context.Background(), ws.imgs[idx]); err == nil {
			src, treated = data.Pixels, true
		} else {
			treatment = nil
			debuglog.Log(fmt.Sprintf("renderImage: star treatment ignored: %v", err))
		}
	}
	var warpedPixels []float32
	if idx < len(ws.renderCache) {
		ws.renderMu.Lock()
		c := ws.renderCache[idx]
		if c.pixels != nil && c.dx == dx && c.dy == dy && c.rot == rot && c.hasAlign == hasAlign && c.align == align && c.treatment == treatment && (treated || sameFloatSlice(c.src, src)) {
			warpedPixels = c.pixels
		}
		ws.renderMu.Unlock()
	}
	if warpedPixels == nil {
		w := ws.imgs[idx].HDU.Data.Width
		h := ws.imgs[idx].HDU.Data.Height
		// Backward sampling: output → manual nudge → star-alignment affine → source.
		t := composeManualOffsetTransform(w, h, dx, dy, rot)
		if hasAlign {
			t = processing.ComposeAffineTransforms(align, t)
		}
		warpedPixels = processing.WarpImage(src, w, h, t)
		if idx < len(ws.renderCache) {
			ws.renderMu.Lock()
			ws.renderCache[idx] = composeRenderCache{dx: dx, dy: dy, rot: rot, hasAlign: hasAlign, align: align, src: src, pixels: warpedPixels, treatment: treatment}
			ws.renderMu.Unlock()
		}
	}
	warped := *ws.imgs[idx]
	warped.HDU.Data.Pixels = warpedPixels
	if treated {
		warped.Mode, warped.Background, warped.Peak, warped.ScaledPeak = stretch.Linear, 0, 1, 1
		warped.StarTreatment = nil
	}
	return &warped
}

func (ws *composeWorkspace) renderImages() []*models.LoadedImage {
	out := make([]*models.LoadedImage, len(ws.imgs))
	for i := range ws.imgs {
		out[i] = ws.renderImage(i)
	}
	return out
}
