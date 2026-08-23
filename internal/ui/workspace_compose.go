package ui

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/wierdling/gofiledialog"

	"gofitsv3/internal/catalog/gaia"
	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/export"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
	"gofitsv3/internal/utils"
)

var composeBlinkFilterNames = []string{"Blue", "Green", "Red"}

const composeLargeFilesPreferenceKey = "compose.largeFiles"

// globalSendToChannel is registered by newComposeWorkspace and called by the
// preview window to load an image directly into a compose channel with all
// stretch settings already applied.
var globalSendToChannel func(channelIdx int, img *models.LoadedImage)

// globalSelectComposeTab is registered by app.go and called by loadProject in
// workspace_compose.go to switch to the Compose tab when loading a Compose project.
var globalSelectComposeTab func()

// globalComposeLargeCleanup is invoked by the application close handler.
var globalComposeLargeCleanup func()
var composeLargeModeActive func() bool
var globalComposeGaiaRefinementInvalidate func()

// maxOverlayLayers bounds how many colored overlay layers can exist at once.
// imgs/origPixels are pre-allocated with room for the 3 RGB base channels plus
// this many overlay slots so that appending an overlay never reallocates the
// backing array — the RGB channelControls capture the imgs slice header by
// value and must keep pointing at the same array.
const maxOverlayLayers = 16

// overlayLayer is one user-added colored layer (formerly the fixed Orange/Yellow
// windows): a grayscale image assigned a tint, screen/additively blended onto the
// base RGB composite. idx is its stable index into imgs/origPixels.
type overlayLayer struct {
	idx                    int
	name                   string
	settings               models.OrangeLayerState
	win                    fyne.Window
	viewport               *viewport
	control                *models.ChannelControl
	calibrationStatus      models.CalibrationStatus
	calibrationStatusLabel *widget.Label
}

// artifactRowReader adapts a runtime float artifact to the bounded Gaia
// aperture reader contract. A handle is opened only for the requested row.
type artifactRowReader struct{ path string }

func (r artifactRowReader) Dimensions() (int, int) {
	a, err := fitsio.OpenFloat32ArtifactReadOnly(r.path)
	if err != nil {
		return 0, 0
	}
	defer a.Close()
	return a.Width, a.Height
}

func (r artifactRowReader) ReadRow(y int, dst []float32) error {
	a, err := fitsio.OpenFloat32ArtifactReadOnly(r.path)
	if err != nil {
		return err
	}
	defer a.Close()
	return a.ReadRow(y, dst)
}

// alignedArtifactRowReader presents a source artifact on the reference
// output grid. It uses the same affine/WCS/manual-offset mapping as the disk
// compositor and returns NaN for invalid footprints so Gaia aperture
// photometry excludes fill pixels.
type alignedArtifactRowReader struct {
	path         string
	source       models.LoadedImage
	reference    models.LoadedImage
	width        int
	height       int
	offsetX      float64
	offsetY      float64
	offsetRot    float64
	a            *fitsio.Float32Artifact
	rows         map[int][]float32
	order        []int
	validScratch []float32
}

// ReadValidRow reports the same footprint validity as the aligned samples.
// Invalid affine footprints and non-finite source pixels are excluded from
// neutral-background statistics rather than being treated as zero-valued data.
func (r *alignedArtifactRowReader) ReadValidRow(y int, dst []bool) error {
	if len(dst) < r.width {
		return fmt.Errorf("invalid aligned validity row %d", y)
	}
	if cap(r.validScratch) < r.width {
		r.validScratch = make([]float32, r.width)
	} else {
		r.validScratch = r.validScratch[:r.width]
	}
	if err := r.ReadRow(y, r.validScratch); err != nil {
		return err
	}
	for x, v := range r.validScratch {
		dst[x] = !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0)
	}
	return nil
}

func (r alignedArtifactRowReader) Dimensions() (int, int) { return r.width, r.height }

func (r *alignedArtifactRowReader) ReadRow(y int, dst []float32) error {
	if len(dst) < r.width {
		return fmt.Errorf("invalid aligned artifact row %d", y)
	}
	return r.ReadWindow(y, 0, r.width, dst[:r.width])
}

// ReadWindow maps and samples only the requested output range. Source rows are
// fetched with ReadRange over the minimum contiguous span needed by the
// mapped pixels; no full output row (or full source row) is materialized.
func (r *alignedArtifactRowReader) ReadWindow(y, x0, x1 int, dst []float32) error {
	if y < 0 || y >= r.height {
		return fmt.Errorf("invalid aligned artifact row %d", y)
	}
	if x0 < 0 {
		x0 = 0
	}
	if x1 > r.width {
		x1 = r.width
	}
	if x1 < x0 || len(dst) < x1-x0 {
		return fmt.Errorf("invalid aligned artifact window")
	}
	if r.a == nil {
		a, err := fitsio.OpenFloat32ArtifactReadOnly(r.path)
		if err != nil {
			return err
		}
		r.a = a
		r.rows = make(map[int][]float32, 8)
	}
	a := r.a
	type mapped struct {
		fx, fy float64
		valid  bool
	}
	mappedPixels := make([]mapped, x1-x0)
	rowRanges := make(map[int][2]int, 2)
	for x := x0; x < x1; x++ {
		fx, fy := processing.MapDiskCoordinate(r.source, r.reference, r.offsetX, r.offsetY, r.offsetRot, x, y, r.width, r.height, a.Width, a.Height)
		m := &mappedPixels[x-x0]
		m.fx, m.fy = fx, fy
		if fx < 0 || fy < 0 || fx > float64(a.Width-1) || fy > float64(a.Height-1) {
			continue
		}
		m.valid = true
		sx0, sy := int(math.Floor(fx)), int(math.Floor(fy))
		sx1, sy1 := sx0+1, sy+1
		if sx1 >= a.Width {
			sx1 = sx0
		}
		if sy1 >= a.Height {
			sy1 = sy
		}
		for _, ry := range []int{sy, sy + 1} {
			if ry >= a.Height {
				ry = sy1
			}
			if old, ok := rowRanges[ry]; ok {
				if sx0 < old[0] {
					old[0] = sx0
				}
				if sx1+1 > old[1] {
					old[1] = sx1 + 1
				}
				rowRanges[ry] = old
			} else {
				rowRanges[ry] = [2]int{sx0, sx1 + 1}
			}
		}
	}
	rows := make(map[int]struct {
		x0     int
		values []float32
	}, len(rowRanges))
	for ry, span := range rowRanges {
		vals := make([]float32, span[1]-span[0])
		if err := a.ReadRange(ry, span[0], span[1], vals); err != nil {
			return err
		}
		rows[ry] = struct {
			x0     int
			values []float32
		}{span[0], vals}
	}
	for i, m := range mappedPixels {
		if !m.valid {
			dst[i] = float32(math.NaN())
			continue
		}
		sx, sy := int(math.Floor(m.fx)), int(math.Floor(m.fy))
		sx1, sy1 := sx+1, sy+1
		wx, wy := m.fx-float64(sx), m.fy-float64(sy)
		if sx1 >= a.Width {
			sx1, wx = sx, 0
		}
		if sy1 >= a.Height {
			sy1, wy = sy, 0
		}
		r0, r1 := rows[sy], rows[sy1]
		v00, v10 := r0.values[sx-r0.x0], r0.values[sx1-r0.x0]
		v01, v11 := r1.values[sx-r1.x0], r1.values[sx1-r1.x0]
		if math.IsNaN(float64(v00)) || math.IsNaN(float64(v10)) || math.IsNaN(float64(v01)) || math.IsNaN(float64(v11)) || math.IsInf(float64(v00), 0) || math.IsInf(float64(v10), 0) || math.IsInf(float64(v01), 0) || math.IsInf(float64(v11), 0) {
			dst[i] = float32(math.NaN())
			continue
		}
		dst[i] = float32(float64(v00)*(1-wx)*(1-wy) + float64(v10)*wx*(1-wy) + float64(v01)*(1-wx)*wy + float64(v11)*wx*wy)
	}
	return nil
}

func (r *alignedArtifactRowReader) Close() error {
	if r.a == nil {
		return nil
	}
	err := r.a.Close()
	r.a = nil
	r.rows = nil
	r.order = nil
	return err
}

type composeOverlayPreviewData struct {
	image      *image.RGBA
	bins       [256]int
	width      int
	height     int
	sky        float64
	mean       float64
	std        float64
	filterText string
}

func buildComposeOverlayPreviewData(ctx context.Context, img *models.LoadedImage) (*composeOverlayPreviewData, error) {
	if err := composeMagicCanceled(ctx); err != nil {
		return nil, err
	}
	stretched, mask := processing.ApplyStretchParallel(img)
	if err := composeMagicCanceled(ctx); err != nil {
		return nil, err
	}
	stats := histogram.Compute(stretched.Pixels)
	sky, _ := processing.EstimateBackground(stretched.Pixels)
	return &composeOverlayPreviewData{
		image:      processing.ToGrayRGBA(stretched, mask),
		bins:       stats.Hist,
		width:      stretched.Width,
		height:     stretched.Height,
		sky:        sky,
		mean:       stats.Mean,
		std:        stats.Std,
		filterText: fitsio.FilterString(img.Primary),
	}, nil
}

func newComposeWorkspace(app fyne.App, win fyne.Window) (fyne.CanvasObject, []*fyne.Menu) {
	imgs := make([]*models.LoadedImage, 3, 3+maxOverlayLayers)
	origPixels := make([][]float32, 3, 3+maxOverlayLayers)
	viewports := []*viewport{newViewport(), newViewport(), newViewport(), newViewport()}
	// Channel histograms: black background, channel-colored bars; compose: white bars.
	viewports[0].histColor = [4]uint8{100, 149, 237, 255} // blue
	viewports[1].histColor = [4]uint8{80, 200, 80, 255}   // green
	viewports[2].histColor = [4]uint8{237, 80, 80, 255}   // red
	viewports[3].histColor = [4]uint8{255, 255, 255, 255} // white (compose)
	headerWins := make([]fyne.Window, 3)
	levels := defaultRGBLevels()
	var levelsWin *rgbLevelsWindow
	var overlayLayers []*overlayLayer
	colorCalibration := models.ColorCalibrationState{Status: models.CalibrationDisabled}
	var refinedGaiaResidual *processing.AffineTransform
	var refinedGaiaSources []gaia.Source
	var gaiaRefinementGeneration uint64
	clearGaiaRefinement := func() {
		refinedGaiaResidual = nil
		refinedGaiaSources = nil
		gaiaRefinementGeneration++
	}
	globalComposeGaiaRefinementInvalidate = clearGaiaRefinement
	// Any source, alignment, stretch, or overlay edit invalidates a previously
	// calculated result. The persisted transforms remain inspectable but are
	// never silently applied to changed pixels.
	invalidateCalibration := func() { clearGaiaRefinement(); markComposeCalibrationStale(&colorCalibration) }
	ensureOverlayCalibration := func(n int) {
		for len(colorCalibration.Overlays) <= n {
			colorCalibration.Overlays = append(colorCalibration.Overlays, models.OverlayCalibrationState{Mode: models.OverlayArtistic, Status: models.CalibrationDisabled, Strength: 1})
		}
	}

	// Updated to track the new struct
	var latestRGBStats [3]histogram.Stats
	suspendRefresh := false
	var composeRGB func(ctx context.Context, calibrationSnapshot *models.ColorCalibrationState) ([]byte, int, int, [3]histogram.Stats, *processing.ComposeRenderResult, error)
	var composeChannelOffsetFields func(int) (float64, float64, float64, bool)
	// renderImages returns an offset-applied view of imgs (Manual Offsets applied
	// at render time). Forward-declared so refresh/compose can use it; assigned
	// once controlSets exists.
	var renderImages func() []*models.LoadedImage
	var previewMu sync.Mutex
	previewSeq := 0
	var genCancel context.CancelFunc
	var calibrationJob composeCalibrationJob
	var calibrationGeneration uint64
	var editSnapshotCancel context.CancelFunc
	var editSnapshotMu sync.Mutex
	// SaveColorCalibration controls the normal render/export gate and whether
	// calibration is included in the next project save.  The live state is
	// retained when disabled so it can be compared or re-enabled immediately.
	saveColorCalibration := true
	var calibrationPreviewOverride *bool
	var blinkMu sync.Mutex
	var blinkPrepared []composeBlinkFrame
	blinkSeq := 0

	sharedHistCheck := NewToggle(nil)
	sharedHistCheck.SetChecked(false)
	buildCompositeCheck := NewToggle(nil)
	buildCompositeCheck.SetChecked(false)
	blinkCheck := NewToggle(nil)
	blinkCheck.SetChecked(false)
	largeFilesCheck := NewToggle(nil)
	largeFilesCheck.SetChecked(app.Preferences().Bool(composeLargeFilesPreferenceKey))
	var largeStore *composeLargeStore
	largeMode := largeFilesCheck.Checked
	composeLargeModeActive = func() bool { return largeMode }
	largePreviews := make(map[int]*image.RGBA)
	largeArtifacts := make(map[int]composeArtifactDescriptor)
	var largeMu sync.RWMutex
	largeRuntime := &largeChannelRuntime{store: nil, artifacts: largeArtifacts, previews: largePreviews, mu: &largeMu, refresh: nil, syncWidgets: make(map[int]func(*models.LoadedImage))}
	largeLoadGenerations := make(map[string]uint64)
	largeSessionGeneration := uint64(1)
	nextLargeLoadGeneration := func(slot string) (uint64, uint64, *composeLargeStore) {
		largeMu.Lock()
		defer largeMu.Unlock()
		largeLoadGenerations[slot]++
		return largeLoadGenerations[slot], largeSessionGeneration, largeStore
	}
	largeLoadStillCurrent := func(slot string, request, session uint64, d composeArtifactDescriptor) bool {
		largeMu.RLock()
		defer largeMu.RUnlock()
		store := largeStore
		if store == nil || largeSessionGeneration != session || largeLoadGenerations[slot] != request || d.Slot != slot {
			return false
		}
		current, ok := store.Descriptor(slot)
		return ok && current.Generation == d.Generation && current.Path == d.Path
	}
	cleanupLargeArtifactIfCurrent := func(d composeArtifactDescriptor) {
		largeMu.RLock()
		store := largeStore
		largeMu.RUnlock()
		if d.Slot == "" || store == nil {
			return
		}
		_, _ = store.RemoveSlotIfCurrent(d)
	}
	invalidateLargeSlot := func(idx int) {
		largeMu.Lock()
		defer largeMu.Unlock()
		slot := fmt.Sprintf("channel-%d", idx)
		if idx >= 3 {
			slot = fmt.Sprintf("overlay-%d", idx)
		}
		largeLoadGenerations[slot]++
	}
	if largeMode {
		largeStore, _ = newComposeLargeStore("")
		if largeStore == nil {
			largeMode = false
			largeFilesCheck.SetChecked(false)
			app.Preferences().SetBool(composeLargeFilesPreferenceKey, false)
		}
	}
	blinkExcludedIdx := 0
	composeMagicPreset := widget.NewSelect([]string{"Balanced", "Nebula", "Galaxy"}, nil)
	composeMagicPreset.SetSelected("Balanced")
	// nil means no generalized selection was persisted; this preserves legacy
	// project migration and lets future defaults select all loaded sources.
	var blinkChannels []int
	var chooseBlinkChannels func()
	var updateHistScaleLabel func()
	var updateBlinkStatus func()
	var startBlink func()
	var stopBlink func()
	var refreshBlinkFrame func()
	var refresh func()
	var measureEnabled bool
	var measureCheck *Toggle
	var measureStart *imagePoint
	var measureEnd *imagePoint
	var updateMeasurement func()
	composeBlinkSources := func() []composeBlinkSource {
		overlays := make([]composeBlinkOverlaySource, 0, len(overlayLayers))
		for _, layer := range overlayLayers {
			path := ""
			if layer.idx < len(imgs) && imgs[layer.idx] != nil {
				path = imgs[layer.idx].Path
			}
			overlays = append(overlays, composeBlinkOverlaySource{RuntimeIndex: layer.idx, Name: layer.name, Path: path, BlinkID: layer.settings.BlinkID})
		}
		return enumerateComposeBlinkSources(imgs, overlays)
	}

	// Updated signature to pass the stats
	pushRGBHist := func(stats [3]histogram.Stats) {
		latestRGBStats = stats
		if levelsWin != nil {
			levelsWin.setHistogram(stats)
		}
	}

	cloneCalibration := func() *models.ColorCalibrationState {
		return composeCalibrationSnapshot(colorCalibration, !saveColorCalibration)
	}
	cloneCalibrationForPreview := func() *models.ColorCalibrationState {
		return composeCalibrationPreviewSnapshot(colorCalibration, saveColorCalibration, calibrationPreviewOverride)
	}
	composeRenderWithCalibration := func(ctx context.Context, composeImgs []*models.LoadedImage, calibrationSnapshot *models.ColorCalibrationState) (processing.ComposeRenderResult, error) {
		var overlays []processing.OverlayLayer
		for _, l := range overlayLayers {
			if l.win != nil && l.idx < len(imgs) && imgs[l.idx] != nil {
				overlays = append(overlays, processing.OverlayLayer{Image: imgs[l.idx], Settings: l.settings})
			}
		}
		if len(overlays) > 0 {
			rendered, err := processing.ComposeRender(ctx, processing.ComposeRenderRequest{Images: composeImgs, Overlays: overlays, Calibration: calibrationSnapshot})
			return rendered, err
		}
		return processing.ComposeRender(ctx, processing.ComposeRenderRequest{Images: composeImgs, Calibration: calibrationSnapshot})
	}
	// startGeneration cancels any in-flight compose generation and starts a
	// fresh one in the background. Triggering this repeatedly in quick
	// succession (e.g. dragging a slider) kills the stale generation's work
	// early via ctx rather than waiting for it to finish and discarding the
	// result, since a single compose pass can take up to ~30s on large mosaics.
	startGeneration := func(onDone func()) {
		if suspendRefresh {
			if onDone != nil {
				onDone()
			}
			return
		}
		previewMu.Lock()
		if genCancel != nil {
			genCancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		genCancel = cancel
		previewSeq++
		seq := previewSeq
		// Invalidate frames from the superseded generation immediately. Keep the
		// active ticker sequence intact: ordinary preview refreshes must resume
		// cycling as soon as replacement frames are applied. stopBlink remains
		// the explicit ticker invalidation path.
		blinkMu.Lock()
		blinkPrepared = nil
		blinkMu.Unlock()
		previewMu.Unlock()
		imgSnapshot := append([]*models.LoadedImage(nil), imgs...)
		if !largeMode {
			imgSnapshot = renderImages()
		}
		blinkSourcesSnapshot := composeBlinkSources()
		blinkSelectionSnapshot := append([]int(nil), blinkChannels...)
		blinkEnabled := blinkCheck.Checked && !largeMode
		if blinkEnabled && blinkChannels == nil {
			blinkSelectionSnapshot = resolveComposeBlinkSelection(blinkSourcesSnapshot, nil, true, blinkExcludedIdx)
		}
		if blinkEnabled && len(filterComposeBlinkSelection(blinkSelectionSnapshot, blinkSourcesSnapshot)) < 2 {
			blinkSelectionSnapshot = nil
		}
		levelsSnapshot := *levels
		sharedHistScale := sharedHistCheck.Checked
		buildComposite := buildCompositeCheck.Checked
		compositeRenderActive := largeMode && buildComposite && len(imgSnapshot) >= 3 &&
			imgSnapshot[0] != nil && imgSnapshot[1] != nil && imgSnapshot[2] != nil
		priorCompositeImage, _ := viewports[3].image.Image.(*image.RGBA)
		priorCompositeStats := ""
		if viewports[3].StatsLabel != nil {
			priorCompositeStats = viewports[3].StatsLabel.Text
		}
		priorCompositeW, priorCompositeH := viewports[3].origW, viewports[3].origH
		priorCompositeBins, priorCompositeHistMax := viewports[3].bins, viewports[3].histMax
		priorCompositeBlack, priorCompositeWhite := viewports[3].blackBox.Value(), viewports[3].whiteBox.Value()
		// Keep the indicator in the viewport header so the image and its
		// interaction layer remain usable while the disk compositor runs.
		if largeMode {
			statusText := composeCompositeDisabledStatus(buildComposite, imgSnapshot)
			if compositeRenderActive {
				statusText = "Rendering disk-backed color composite…"
			}
			// Publish the in-progress/disabled state before the worker starts. This
			// deliberately changes only the tile label, retaining the previous
			// image until a current generation produces a replacement.
			fyne.Do(func() {
				viewports[3].SetStatsText(statusText)
				viewports[3].SetCompositeRendering(compositeRenderActive)
			})
		} else {
			fyne.Do(func() { viewports[3].SetCompositeRendering(false) })
		}
		largeMu.RLock()
		largePreviewSnapshot := make(map[int]*image.RGBA, len(largePreviews))
		for i, p := range largePreviews {
			largePreviewSnapshot[i] = p
		}
		largeMu.RUnlock()
		for i := range overlayLayers {
			ensureOverlayCalibration(i)
		}
		calibrationSnapshot := cloneCalibrationForPreview()
		go func() {
			start := time.Now()
			debuglog.Log("compose refresh async: starting preview computation")
			var renderedResult *processing.ComposeRenderResult
			var data composePreviewData
			if largeMode {
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
					b, w, h, s, rendered, e := composeRGB(ctx, calibrationSnapshot)
					if e == nil && len(b) > 0 {
						renderedResult = rendered
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
					b, w, h, s, rendered, e := composeRGB(c, calibrationSnapshot)
					renderedResult = rendered
					return b, w, h, s, e
				})
			}
			data.Rendered = renderedResult
			if blinkEnabled {
				data.BlinkFrames = buildComposeBlinkFrames(ctx, imgSnapshot, blinkSourcesSnapshot, blinkSelectionSnapshot, data.Views)
			}
			debuglog.Log(fmt.Sprintf("compose refresh async: preview computation took %s", time.Since(start)))
			fyne.Do(func() {
				previewMu.Lock()
				currentSeq := previewSeq
				previewMu.Unlock()
				if seq != currentSeq || ctx.Err() != nil {
					debuglog.Log("compose refresh async: skipped stale/canceled preview result")
					if seq == currentSeq && ctx.Err() != nil {
						viewports[3].SetCompositeRendering(false)
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
				if buildComposite != buildCompositeCheck.Checked {
					debuglog.Log("compose refresh async: rerendering after composite toggle change")
					refresh()
					if onDone != nil {
						onDone()
					}
					return
				}
				// This generation is now either applied or has produced its final
				// error result; a newer generation, if any, owns the indicator.
				viewports[3].SetCompositeRendering(false)
				applyComposePreviewData(data, viewports, pushRGBHist)
				if data.Rendered != nil {
					for i, l := range overlayLayers {
						if i >= len(data.Rendered.OverlayStatus) {
							continue
						}
						l.calibrationStatus = data.Rendered.OverlayStatus[i]
						if l.calibrationStatusLabel != nil {
							reason := ""
							if i < len(data.Rendered.OverlayDiagnostics) {
								reason = data.Rendered.OverlayDiagnostics[i].Message
							}
							if reason != "" {
								reason = " — " + reason
							}
							l.calibrationStatusLabel.SetText("Calibration: " + string(l.calibrationStatus) + reason)
						}
					}
				}
				blinkMu.Lock()
				blinkPrepared = append([]composeBlinkFrame(nil), data.BlinkFrames...)
				blinkMu.Unlock()
				if refreshBlinkFrame != nil {
					refreshBlinkFrame()
				}
				debuglog.Log("compose refresh async: applied preview result")
				if onDone != nil {
					onDone()
				}
			})
		}()
	}
	refresh = func() {
		startGeneration(nil)
	}
	sharedHistCheck.OnChanged = func(bool) {
		if updateHistScaleLabel != nil {
			updateHistScaleLabel()
		}
		refresh()
	}
	buildCompositeCheck.OnChanged = func(bool) {
		refresh()
	}
	refreshAsync := func(onDone func()) {
		startGeneration(onDone)
	}
	withSuspendedRefresh := func(fn func()) {
		prev := suspendRefresh
		suspendRefresh = true
		defer func() { suspendRefresh = prev }()
		fn()
	}

	type composePicker struct {
		channel int
		target  string
	}
	activePicker := composePicker{channel: -1}
	clearPicker := func() {
		activePicker = composePicker{channel: -1}
		for i := 0; i < 3; i++ {
			if viewports[i] == nil || viewports[i].overlay == nil {
				continue
			}
			viewports[i].overlay.pickerActive = false
			viewports[i].overlay.Refresh()
			viewports[i].SetPickerValueText("Value: --")
		}
	}
	setPicker := func(channel int, target string) {
		if activePicker.channel == channel && activePicker.target == target {
			clearPicker()
			return
		}
		clearPicker()
		activePicker = composePicker{channel: channel, target: target}
		if viewports[channel] != nil && viewports[channel].overlay != nil {
			viewports[channel].overlay.pickerActive = true
			viewports[channel].overlay.Refresh()
		}
		viewports[channel].SetPickerValueText(fmt.Sprintf("Pick %s: --", target))
	}
	updatePickerValue := func(channel int, point imagePoint) {
		// While picking a level, report the median of a small region so the
		// readout matches the value that will be committed (see onTapped) and is
		// stable against single noisy pixels. A plain hover stays a single-pixel
		// probe.
		var (
			value float64
			ok    bool
		)
		if activePicker.channel == channel {
			value, ok = composeRegionMedianAt(imgs[channel], point, composePickRadius)
		} else {
			value, ok = composePixelValueAt(imgs[channel], point)
		}
		if !ok {
			if activePicker.channel == channel {
				viewports[channel].SetPickerValueText(fmt.Sprintf("Pick %s: --", activePicker.target))
			} else {
				viewports[channel].SetPickerValueText("Value: --")
			}
			return
		}
		if activePicker.channel == channel {
			viewports[channel].SetPickerValueText(fmt.Sprintf("Pick %s: %.6g", activePicker.target, value))
			return
		}
		viewports[channel].SetPickerValueText(fmt.Sprintf("Value: %.6g", value))
	}
	for i := 0; i < 3; i++ {
		idx := i
		viewports[idx].SetLevelPickers(
			func() { setPicker(idx, "Black") },
			func() { setPicker(idx, "White") },
		)
		viewports[idx].overlay.onPointerMove = func(pos fyne.Position) {
			point, ok := viewports[idx].imagePointAtPosition(pos, false)
			if !ok {
				if activePicker.channel == idx {
					viewports[idx].SetPickerValueText(fmt.Sprintf("Pick %s: --", activePicker.target))
				} else {
					viewports[idx].SetPickerValueText("Value: --")
				}
				return
			}
			updatePickerValue(idx, point)
		}
		viewports[idx].overlay.onPointerOut = func() {
			if activePicker.channel == idx {
				viewports[idx].SetPickerValueText(fmt.Sprintf("Pick %s: --", activePicker.target))
				return
			}
			viewports[idx].SetPickerValueText("Value: --")
		}
		viewports[idx].overlay.onTapped = func(pos fyne.Position) {
			if activePicker.channel != idx {
				return
			}
			point, ok := viewports[idx].imagePointAtPosition(pos, false)
			if !ok {
				return
			}
			value, ok := composeRegionMedianAt(imgs[idx], point, composePickRadius)
			if !ok {
				return
			}
			if activePicker.target == "Black" {
				imgs[idx].Black = value
				viewports[idx].blackBox.SetValue(value)
			} else {
				imgs[idx].White = value
				viewports[idx].whiteBox.SetValue(value)
			}
			clearPicker()
			refresh()
		}
	}

	detectExportFormat := func(path string) export.Format {
		switch {
		case strings.HasSuffix(path, ".webp"):
			return export.WEBP
		case strings.HasSuffix(path, ".png"):
			return export.PNG
		case strings.HasSuffix(path, ".tif"), strings.HasSuffix(path, ".tiff"):
			return export.TIFF
		case strings.HasSuffix(path, ".jpg"), strings.HasSuffix(path, ".jpeg"):
			return export.JPEG
		default:
			return export.PNG
		}
	}

	composeRGB = func(ctx context.Context, calibrationSnapshot *models.ColorCalibrationState) ([]byte, int, int, [3]histogram.Stats, *processing.ComposeRenderResult, error) {
		if largeMode {
			if largeStore == nil || len(imgs) < 3 || imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
				return nil, 0, 0, [3]histogram.Stats{}, nil, errors.New("all three channels are required for disk-backed Compose")
			}
			largeMu.RLock()
			expectedCompositeGeneration := uint64(0)
			if currentComposite, ok := largeStore.Composite(); ok {
				expectedCompositeGeneration = currentComposite.Generation
			}
			var channels [3]processing.DiskChannel
			for i := 0; i < 3; i++ {
				d, ok := largeArtifacts[i]
				if !ok || d.Path == "" {
					largeMu.RUnlock()
					return nil, 0, 0, [3]histogram.Stats{}, nil, fmt.Errorf("missing disk artifact for channel %d", i)
				}
				dx, dy, rot, _ := composeChannelOffsetFields(i)
				channels[i] = processing.DiskChannel{ArtifactPath: d.Path, Image: *imgs[i], OffsetX: dx, OffsetY: dy, OffsetRot: rot}
			}
			largeMu.RUnlock()
			// Every render owns a unique output set. This prevents a cancelled or
			// superseded job from reopening/overwriting the currently displayed
			// composite while another job is still preparing its rows.
			renderID := time.Now().UnixNano()
			outs := [3]string{
				filepath.Join(largeStore.root, fmt.Sprintf("composite-r-%d.bin", renderID)),
				filepath.Join(largeStore.root, fmt.Sprintf("composite-g-%d.bin", renderID)),
				filepath.Join(largeStore.root, fmt.Sprintf("composite-b-%d.bin", renderID)),
			}
			ovs := make([]processing.DiskOverlay, 0, len(overlayLayers))
			largeMu.RLock()
			for _, l := range overlayLayers {
				if l == nil || l.idx >= len(imgs) || imgs[l.idx] == nil {
					continue
				}
				if d, ok := largeArtifacts[l.idx]; ok {
					ovs = append(ovs, processing.DiskOverlay{Channel: processing.DiskChannel{ArtifactPath: d.Path, Image: *imgs[l.idx]}, Settings: l.settings})
				}
			}
			largeMu.RUnlock()
			result, err := processing.ComposeDisk(ctx, processing.DiskComposeRequest{Channels: channels, Overlays: ovs, Calibration: calibrationSnapshot, Output: outs, PreviewMax: 1600, RGBLevels: levels})
			if err != nil {
				for _, p := range outs {
					_ = os.Remove(p)
				}
				return nil, 0, 0, [3]histogram.Stats{}, nil, err
			}
			if _, err := largeStore.PublishCompositeIfCurrent(expectedCompositeGeneration, outs, result.Width, result.Height); err != nil {
				for _, p := range outs {
					_ = os.Remove(p)
				}
				return nil, 0, 0, [3]histogram.Stats{}, nil, err
			}
			rendered := &processing.ComposeRenderResult{Preview: result.Preview, Width: result.Width, Height: result.Height, Stats: result.Stats, Status: result.Status}
			return result.Preview, result.Width, result.Height, result.Stats, rendered, nil
		}
		rendered, err := composeRenderWithCalibration(ctx, renderImages(), calibrationSnapshot)
		return rendered.Preview, rendered.Width, rendered.Height, rendered.Stats, &rendered, err
	}

	saveChannelGray := func(idx int) {
		if largeMode {
			if imgs[idx] == nil {
				dialog.ShowInformation("Missing", fmt.Sprintf("Load Channel %d first", idx+1), win)
				return
			}
			d, ok := largeArtifacts[idx]
			if !ok {
				dialog.ShowInformation("Missing", "The channel artifact is unavailable.", win)
				return
			}
			save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
				if err != nil || uc == nil {
					return
				}
				path := uc.URI().Path()
				_ = uc.Close()
				format := detectExportFormat(path)
				showExportOptionsDialog(format, win, func(opts export.Options) {
					if err := export.FromFloat32Artifact(context.Background(), path, d.Path, d.Width, d.Height, format, opts, imgs[idx]); err != nil {
						dialog.ShowError(err, win)
					}
				})
			}, win)
			save.SetFileName(fmt.Sprintf("channel_%d_gray.png", idx+1))
			save.Show()
			return
		}
		if imgs[idx] == nil {
			dialog.ShowInformation("Missing", fmt.Sprintf("Load Channel %d first", idx+1), win)
			return
		}

		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()

			format := detectExportFormat(path)
			stretched, _ := processing.ApplyStretchParallel(imgs[idx])
			gray := processing.ToGrayRGBA(stretched, make([]byte, len(stretched.Pixels)))
			showExportOptionsDialog(format, win, func(opts export.Options) {
				if err := export.FromImage(path, gray, format, opts); err != nil {
					dialog.ShowError(err, win)
				}
			})
		}, win)
		save.SetFileName(fmt.Sprintf("channel_%d_gray.png", idx+1))
		save.Show()
	}
	normalizeScale := func() {
		if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
			dialog.ShowInformation("Missing Channels", "Load all three FITS channels before scaling.", win)
			return
		}
		if largeMode && largeStore != nil {
			linesG := utils.FormatHeadersLines(imgs[1].Primary, imgs[1].HDU.Header)
			targetScale := processing.GetPixelScale(linesG)
			progressDialog := dialog.NewCustom("Normalizing", "Resampling arrays to match Channel 2 scale...", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()
			go func() {
				type result struct {
					idx, width, height int
					d                  composeArtifactDescriptor
					preview            *image.RGBA
					err                error
				}
				results := make([]result, 0, 2)
				for _, idx := range []int{0, 2} {
					d, ok := largeArtifacts[idx]
					if !ok {
						results = append(results, result{idx: idx, err: fmt.Errorf("missing disk artifact for channel %d", idx+1)})
						continue
					}
					lines := utils.FormatHeadersLines(imgs[idx].Primary, imgs[idx].HDU.Header)
					sourceScale := processing.GetPixelScale(lines)
					if targetScale == 0 || math.Abs(sourceScale-targetScale)/targetScale < 0.01 {
						results = append(results, result{idx: idx, width: d.Width, height: d.Height, d: d})
						continue
					}
					ratio := sourceScale / targetScale
					newW, newH := int(float64(d.Width)*ratio), int(float64(d.Height)*ratio)
					nd, err := largeStore.ResizeArtifact(d, newW, newH)
					var preview *image.RGBA
					if err == nil {
						preview, _, _, err = composeLargeStretchedPreview(nd.Path, imgs[idx])
					}
					results = append(results, result{idx: idx, width: newW, height: newH, d: nd, preview: preview, err: err})
				}
				fyne.Do(func() {
					progressDialog.Hide()
					count := 0
					for _, r := range results {
						if r.err != nil {
							dialog.ShowError(r.err, win)
							continue
						}
						if r.width == 0 {
							continue
						}
						largeArtifacts[r.idx] = r.d
						if r.preview != nil {
							largePreviews[r.idx] = r.preview
						}
						imgs[r.idx].HDU.Data.Width, imgs[r.idx].HDU.Data.Height = r.width, r.height
						imgs[r.idx].HDU.Data.Pixels = nil
						clearComposeOrigPixels(&origPixels, r.idx)
						count++
					}
					refresh()
					dialog.ShowInformation("Complete", fmt.Sprintf("Rescaled %d channel(s) to match Channel 2 pixel scale.", count), win)
				})
			}()
			return
		}

		// Calculate the absolute physical scale of the reference channel
		linesG := utils.FormatHeadersLines(imgs[1].Primary, imgs[1].HDU.Header)
		targetScale := processing.GetPixelScale(linesG)

		progressDialog := dialog.NewCustom("Normalizing", "Resampling arrays to match Channel 2 scale...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			resizedCount := 0
			for i := 0; i < 3; i++ {
				if i == 1 {
					continue // Channel 2 is the reference
				}

				lines := utils.FormatHeadersLines(imgs[i].Primary, imgs[i].HDU.Header)
				sourceScale := processing.GetPixelScale(lines)

				// Skip if scales match within a 1% margin of error to prevent destructive sub-pixel resampling
				if math.Abs(sourceScale-targetScale)/targetScale < 0.01 {
					continue
				}

				ratio := sourceScale / targetScale
				newW := int(float64(imgs[i].HDU.Data.Width) * ratio)
				newH := int(float64(imgs[i].HDU.Data.Height) * ratio)

				resized := processing.ResizeChannel(imgs[i].HDU.Data.Pixels, imgs[i].HDU.Data.Width, imgs[i].HDU.Data.Height, newW, newH)

				imgs[i].HDU.Data.Pixels = resized
				imgs[i].HDU.Data.Width = newW
				imgs[i].HDU.Data.Height = newH
				clearComposeOrigPixels(&origPixels, i)
				resizedCount++
			}

			fyne.Do(func() {
				progressDialog.Hide()
				refresh()
				dialog.ShowInformation("Complete", fmt.Sprintf("Rescaled %d channel(s) to match Channel 2 pixel scale.", resizedCount), win)
			})
		}()
	}

	closeHeaderWindow := func(idx int) {
		if headerWins[idx] != nil {
			headerWins[idx].SetCloseIntercept(nil)
			headerWins[idx].Close()
			headerWins[idx] = nil
		}
	}

	showHeader := func(idx int) {
		if imgs[idx] == nil {
			return
		}
		closeHeaderWindow(idx)
		lines := utils.FormatHeadersLines(imgs[idx].Primary, imgs[idx].HDU.Header)
		list := widget.NewList(
			func() int { return len(lines) },
			func() fyne.CanvasObject {
				lbl := widget.NewLabel("")
				lbl.Wrapping = fyne.TextWrapOff
				lbl.TextStyle = fyne.TextStyle{Monospace: true}
				return lbl
			},
			func(id widget.ListItemID, co fyne.CanvasObject) {
				lbl := co.(*widget.Label)
				lbl.SetText(lines[id])
			},
		)
		w := app.NewWindow(fmt.Sprintf("Channel %d Headers", idx+1))
		w.SetContent(list)
		w.Resize(fyne.NewSize(700, 500))
		w.SetCloseIntercept(func() {
			w.SetCloseIntercept(nil)
			w.Close()
			headerWins[idx] = nil
		})
		headerWins[idx] = w
		w.Show()
	}

	saveHeader := func(idx int) {
		if imgs[idx] == nil {
			dialog.ShowInformation("Missing", fmt.Sprintf("Load Channel %d first", idx+1), win)
			return
		}

		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			defer uc.Close()

			lines := utils.FormatHeadersLines(imgs[idx].Primary, imgs[idx].HDU.Header)
			content := strings.Join(lines, "\n") + "\n"
			if _, err := uc.Write([]byte(content)); err != nil {
				dialog.ShowError(err, win)
			}
		}, win)
		save.SetFileName(fmt.Sprintf("channel_%d_headers.txt", idx+1))
		save.SetFilter(storage.NewExtensionFileFilter([]string{".txt"}))
		save.Show()
	}

	var updateMenus func()
	var controlSets []*models.ChannelControl

	loadChannel := func(idx int) {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}

			path := r.URI().Path()
			app.Preferences().SetString("lastDir", filepath.Dir(path))

			progressDialog := dialog.NewCustom(
				fmt.Sprintf("Loading Channel %d", idx+1),
				"Reading FITS data...",
				widget.NewProgressBarInfinite(),
				win,
			)
			progressDialog.Show()

			diskLoad := largeMode
			go func() {
				slot := fmt.Sprintf("channel-%d", idx)
				request, session, store := nextLargeLoadGeneration(slot)
				var img *models.LoadedImage
				var preview *image.RGBA
				var artifact composeArtifactDescriptor
				var loadErr error
				if diskLoad {
					if largeStore == nil {
						loadErr = errors.New("disk-backed Compose store is unavailable")
					} else {
						if store == nil {
							loadErr = errors.New("disk-backed Compose store is unavailable")
						} else {
							img, preview, artifact, loadErr = loadLargeComposeImage(path, store, slot)
						}
					}
				} else {
					img, loadErr = loadImageFromPath(path)
				}

				if loadErr != nil {
					progressDialog.Hide()
					dialog.ShowError(loadErr, win)
					return
				}

				if diskLoad && !largeLoadStillCurrent(slot, request, session, artifact) {
					cleanupLargeArtifactIfCurrent(artifact)
					return
				}
				if !diskLoad {
					replaceComposeChannelImage(imgs, idx, img)
				}
				if diskLoad {
					largeMu.Lock()
					current, currentOK := store.Descriptor(slot)
					if largeStore != store || largeSessionGeneration != session || largeLoadGenerations[slot] != request || !currentOK || current.Generation != artifact.Generation || current.Path != artifact.Path {
						largeMu.Unlock()
						cleanupLargeArtifactIfCurrent(artifact)
						return
					}
					replaceComposeChannelImage(imgs, idx, img)
					largePreviews[idx] = preview
					largeArtifacts[idx] = artifact
					largeMu.Unlock()
				}
				clearComposeOrigPixels(&origPixels, idx)

				fyne.Do(func() {
					invalidateCalibration()
					if controlSets != nil {
						applyChannelState(idx, channelStateFromImage(img), imgs, viewports, controlSets)
					}
					progressDialog.Hide()
					refresh()
					closeHeaderWindow(idx)
					if updateMenus != nil {
						updateMenus()
					}
				})
			}()

		}, win)

		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		if last := app.Preferences().String("lastDir"); last != "" {
			uri := storage.NewFileURI(last)
			if l, err := storage.ListerForURI(uri); err == nil {
				fd.SetLocation(l)
			}
		}
		fd.SetView(dialog.ListView)
		sizeFileDialog(fd)
		fd.Show()
	}

	nextLayerNumber := 0

	// layerViews / layerControls build the sparse (idx+1)-length slices that
	// channelControls and applyChannelState expect: the layer's viewport/control
	// sits at its own idx and every earlier slot is nil.
	layerViews := func(l *overlayLayer) []*viewport {
		v := make([]*viewport, l.idx+1)
		v[l.idx] = l.viewport
		return v
	}
	layerControls := func(l *overlayLayer) []*models.ChannelControl {
		c := make([]*models.ChannelControl, l.idx+1)
		c[l.idx] = l.control
		return c
	}
	applyLayerPreview := func(l *overlayLayer, data *composeOverlayPreviewData) {
		l.viewport.image.Image = data.image
		l.viewport.bins = data.bins
		l.viewport.histMax = 0
		l.viewport.origW = data.width
		l.viewport.origH = data.height
		l.viewport.blackBox.SetValue(imgs[l.idx].Black)
		l.viewport.whiteBox.SetValue(imgs[l.idx].White)
		if l.viewport.StatsLabel != nil {
			l.viewport.StatsLabel.SetText(fmt.Sprintf("Sky %.3f  μ %.3f  σ %.3f", data.sky, data.mean, data.std))
		}
		l.viewport.SetFilterText(data.filterText)
		l.viewport.histogram.Refresh()
		if l.viewport.zoomLabel.Selected == "fit" {
			l.viewport.zoom = l.viewport.fitZoom()
		}
		l.viewport.applyZoom()
		l.viewport.image.Refresh()
	}

	refreshLayerPreview := func(l *overlayLayer) {
		if suspendRefresh {
			return
		}
		if l == nil || l.viewport == nil {
			refresh()
			return
		}
		if l.idx >= len(imgs) || imgs[l.idx] == nil {
			l.viewport.image.Image = blankImg()
			l.viewport.bins = [256]int{}
			l.viewport.histMax = 0
			l.viewport.blackBox.SetValue(0)
			l.viewport.whiteBox.SetValue(0)
			if l.viewport.StatsLabel != nil {
				l.viewport.StatsLabel.SetText("Sky --  μ --  σ --")
			}
			l.viewport.SetFilterText("")
			l.viewport.histogram.Refresh()
			l.viewport.image.Refresh()
			refresh()
			return
		}
		if composeLargeModeActive != nil && composeLargeModeActive() {
			largeMu.RLock()
			p := largePreviews[l.idx]
			largeMu.RUnlock()
			if p != nil {
				l.viewport.image.Image = p
				l.viewport.image.Refresh()
			}
			refresh()
			return
		}
		data, err := buildComposeOverlayPreviewData(context.Background(), imgs[l.idx])
		if err != nil {
			return
		}
		applyLayerPreview(l, data)
		refresh()
	}

	saveLayerGray := func(l *overlayLayer) {
		if largeMode {
			if l == nil || l.idx >= len(imgs) || imgs[l.idx] == nil {
				dialog.ShowInformation("Missing", "Load the layer image first", win)
				return
			}
			d, ok := largeArtifacts[l.idx]
			if !ok {
				dialog.ShowInformation("Missing", "The layer artifact is unavailable.", win)
				return
			}
			save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
				if err != nil || uc == nil {
					return
				}
				path := uc.URI().Path()
				_ = uc.Close()
				format := detectExportFormat(path)
				showExportOptionsDialog(format, win, func(opts export.Options) {
					if err := export.FromFloat32Artifact(context.Background(), path, d.Path, d.Width, d.Height, format, opts, imgs[l.idx]); err != nil {
						dialog.ShowError(err, win)
					}
				})
			}, win)
			save.SetFileName("layer_gray.png")
			save.Show()
			return
		}
		if l.idx >= len(imgs) || imgs[l.idx] == nil {
			dialog.ShowInformation("Missing", "Load the layer image first", win)
			return
		}
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()
			format := detectExportFormat(path)
			stretched, _ := processing.ApplyStretchParallel(imgs[l.idx])
			gray := processing.ToGrayRGBA(stretched, make([]byte, len(stretched.Pixels)))
			showExportOptionsDialog(format, win, func(opts export.Options) {
				if err := export.FromImage(path, gray, format, opts); err != nil {
					dialog.ShowError(err, win)
				}
			})
		}, win)
		save.SetFileName("layer_gray.png")
		save.Show()
	}

	loadLayer := func(l *overlayLayer) {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			progressDialog := dialog.NewCustom("Loading Layer Image", "Reading FITS data...", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()
			diskLoad := largeMode
			go func() {
				slot := fmt.Sprintf("overlay-%d", l.idx)
				request, session, store := nextLargeLoadGeneration(slot)
				var img *models.LoadedImage
				var preview *image.RGBA
				var artifact composeArtifactDescriptor
				var loadErr error
				if diskLoad {
					if store == nil {
						loadErr = errors.New("disk-backed Compose store is unavailable")
					} else {
						img, preview, artifact, loadErr = loadLargeComposeImage(path, store, slot)
					}
				} else {
					img, loadErr = loadImageFromPath(path)
				}
				if loadErr != nil {
					progressDialog.Hide()
					dialog.ShowError(loadErr, win)
					return
				}
				if diskLoad && !largeLoadStillCurrent(slot, request, session, artifact) {
					cleanupLargeArtifactIfCurrent(artifact)
					return
				}
				if diskLoad {
					largeMu.Lock()
					current, currentOK := store.Descriptor(slot)
					if largeStore != store || largeSessionGeneration != session || largeLoadGenerations[slot] != request || !currentOK || current.Generation != artifact.Generation || current.Path != artifact.Path {
						largeMu.Unlock()
						cleanupLargeArtifactIfCurrent(artifact)
						return
					}
					largePreviews[l.idx] = preview
					largeArtifacts[l.idx] = artifact
					imgs[l.idx] = img
					largeMu.Unlock()
				} else {
					imgs[l.idx] = img
				}
				clearComposeOrigPixels(&origPixels, l.idx)
				fyne.Do(func() {
					if l.control != nil {
						applyChannelState(l.idx, channelStateFromImage(img), imgs, layerViews(l), layerControls(l))
					}
					progressDialog.Hide()
					refreshLayerPreview(l)
					if updateMenus != nil {
						updateMenus()
					}
				})
			}()
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		if last := app.Preferences().String("lastDir"); last != "" {
			uri := storage.NewFileURI(last)
			if l, err := storage.ListerForURI(uri); err == nil {
				fd.SetLocation(l)
			}
		}
		fd.SetView(dialog.ListView)
		sizeFileDialog(fd)
		fd.Show()
	}

	removeLayer := func(l *overlayLayer) {
		oldSources := composeBlinkSources()
		for i, x := range overlayLayers {
			if x == l {
				overlayLayers = append(overlayLayers[:i], overlayLayers[i+1:]...)
				break
			}
		}
		if l.idx < len(imgs) {
			imgs[l.idx] = nil
			if largeMode && largeStore != nil {
				invalidateLargeSlot(l.idx)
				largeMu.Lock()
				store := largeStore
				d := largeArtifacts[l.idx]
				delete(largeArtifacts, l.idx)
				delete(largePreviews, l.idx)
				largeMu.Unlock()
				if d.Slot != "" && store != nil {
					_, _ = store.RemoveSlotIfCurrent(d)
				}
			}
			clearComposeOrigPixels(&origPixels, l.idx)
		}
		l.win = nil
		l.viewport = nil
		l.control = nil
		if blinkChannels != nil {
			blinkChannels = remapComposeBlinkSelection(blinkChannels, oldSources, composeBlinkSources())
		}
	}

	openOverlayLayerWindowWithPreview := func(l *overlayLayer, preparedPreview *composeOverlayPreviewData) {
		if l.win != nil {
			l.win.Show()
			l.win.RequestFocus()
			refresh()
			return
		}
		l.viewport = newViewport()
		l.viewport.histColor = [4]uint8{l.settings.ColorR, l.settings.ColorG, l.settings.ColorB, 255}

		activePicker := ""
		clearPicker := func() {
			activePicker = ""
			l.viewport.overlay.pickerActive = false
			l.viewport.overlay.Refresh()
			l.viewport.SetPickerValueText("Value: --")
		}
		setPicker := func(target string) {
			if activePicker == target {
				clearPicker()
				return
			}
			activePicker = target
			l.viewport.overlay.pickerActive = true
			l.viewport.overlay.Refresh()
			l.viewport.SetPickerValueText(fmt.Sprintf("Pick %s: --", target))
		}
		l.viewport.SetLevelPickers(
			func() { setPicker("Black") },
			func() { setPicker("White") },
		)
		l.viewport.overlay.onPointerMove = func(pos fyne.Position) {
			point, ok := l.viewport.imagePointAtPosition(pos, false)
			if !ok {
				if activePicker != "" {
					l.viewport.SetPickerValueText(fmt.Sprintf("Pick %s: --", activePicker))
				} else {
					l.viewport.SetPickerValueText("Value: --")
				}
				return
			}
			var value float64
			var okv bool
			if activePicker != "" {
				value, okv = composeRegionMedianAt(imgs[l.idx], point, composePickRadius)
			} else {
				value, okv = composePixelValueAt(imgs[l.idx], point)
			}
			if !okv {
				if activePicker != "" {
					l.viewport.SetPickerValueText(fmt.Sprintf("Pick %s: --", activePicker))
				} else {
					l.viewport.SetPickerValueText("Value: --")
				}
				return
			}
			if activePicker != "" {
				l.viewport.SetPickerValueText(fmt.Sprintf("Pick %s: %.6g", activePicker, value))
				return
			}
			l.viewport.SetPickerValueText(fmt.Sprintf("Value: %.6g", value))
		}
		l.viewport.overlay.onPointerOut = func() {
			if activePicker != "" {
				l.viewport.SetPickerValueText(fmt.Sprintf("Pick %s: --", activePicker))
				return
			}
			l.viewport.SetPickerValueText("Value: --")
		}
		l.viewport.overlay.onTapped = func(pos fyne.Position) {
			if activePicker == "" {
				return
			}
			point, ok := l.viewport.imagePointAtPosition(pos, false)
			if !ok {
				return
			}
			value, ok := composeRegionMedianAt(imgs[l.idx], point, composePickRadius)
			if !ok {
				return
			}
			if activePicker == "Black" {
				imgs[l.idx].Black = value
				l.viewport.blackBox.SetValue(value)
			} else {
				imgs[l.idx].White = value
				l.viewport.whiteBox.SetValue(value)
			}
			clearPicker()
			refreshLayerPreview(l)
		}

		l.viewport.SetLoadSave(l.name, "L", color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255},
			func() { loadLayer(l) }, func() { saveLayerGray(l) })
		largeRuntime.store = largeStore
		l.control = channelControls(l.name+" Image", color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255}, l.idx, imgs, &origPixels, layerViews(l), func() { refreshLayerPreview(l) }, composeMagicPreset, false, largeRuntime)

		swatch := canvas.NewRectangle(color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255})
		swatch.SetMinSize(fyne.NewSize(36, 18))
		updateSwatch := func() {
			col := color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255}
			swatch.FillColor = col
			swatch.Refresh()
			l.viewport.histColor = [4]uint8{l.settings.ColorR, l.settings.ColorG, l.settings.ColorB, 255}
			l.viewport.histogram.Refresh()
		}
		colorLabelWidth := float32(0)
		for _, s := range []string{"Red", "Green", "Blue"} {
			if w := widget.NewLabel(s).MinSize().Width; w > colorLabelWidth {
				colorLabelWidth = w
			}
		}
		colorLabel := func(text string) fyne.CanvasObject {
			lb := widget.NewLabel(text)
			return container.New(layout.NewGridWrapLayout(fyne.NewSize(colorLabelWidth, lb.MinSize().Height)), lb)
		}
		colorSlider := func(label string, value uint8, set func(uint8)) fyne.CanvasObject {
			slider := widget.NewSlider(0, 255)
			slider.Step = 1
			slider.Value = float64(value)
			valueEntry := NewNumberEntry(1, 0)
			valueEntry.Min = 0
			valueEntry.Max = 255
			valueEntry.MinWidth = 70
			valueEntry.SetValue(float64(value))
			slider.OnChanged = func(v float64) {
				n := uint8(math.Round(v))
				set(n)
				invalidateCalibration()
				valueEntry.SetValue(float64(n))
				updateSwatch()
			}
			valueEntry.OnChanged = func(v float64) {
				n := uint8(math.Round(v))
				set(n)
				invalidateCalibration()
				slider.Value = float64(n)
				slider.Refresh()
				updateSwatch()
			}
			return container.NewBorder(nil, nil, colorLabel(label), valueEntry, slider)
		}
		opacitySlider := widget.NewSlider(0, 100)
		opacitySlider.Step = 1
		opacitySlider.Value = l.settings.Opacity * 100
		opacityValue := NewNumberEntry(1, 0)
		opacityValue.Min = 0
		opacityValue.Max = 100
		opacityValue.MinWidth = 70
		opacityValue.SetValue(opacitySlider.Value)
		opacitySlider.OnChanged = func(v float64) {
			l.settings.Opacity = v / 100
			invalidateCalibration()
			opacityValue.SetValue(v)
			refresh()
		}
		opacityValue.OnChanged = func(v float64) {
			l.settings.Opacity = v / 100
			invalidateCalibration()
			opacitySlider.Value = v
			opacitySlider.Refresh()
			refresh()
		}
		protectSlider := widget.NewSlider(0, 100)
		protectSlider.Step = 1
		protectSlider.Value = l.settings.HighlightProtect * 100
		protectValue := NewNumberEntry(1, 0)
		protectValue.Min = 0
		protectValue.Max = 100
		protectValue.MinWidth = 70
		protectValue.SetValue(protectSlider.Value)
		protectSlider.OnChanged = func(v float64) {
			l.settings.HighlightProtect = v / 100
			invalidateCalibration()
			protectValue.SetValue(v)
			refresh()
		}
		protectValue.OnChanged = func(v float64) {
			l.settings.HighlightProtect = v / 100
			invalidateCalibration()
			protectSlider.Value = v
			protectSlider.Refresh()
			refresh()
		}

		if l.idx < len(imgs) && imgs[l.idx] != nil {
			applyChannelState(l.idx, channelStateFromImage(imgs[l.idx]), imgs, layerViews(l), layerControls(l))
		}
		overlayStateIndex := 0
		for i, candidate := range overlayLayers {
			if candidate == l {
				overlayStateIndex = i
				break
			}
		}
		ensureOverlayCalibration(overlayStateIndex)
		modeSelect := NewSafeSelect([]string{"Artistic", "Calibrated Linear"}, nil)
		if colorCalibration.Overlays[overlayStateIndex].Mode == models.OverlayCalibratedLinear {
			modeSelect.SetSelected("Calibrated Linear")
		} else {
			modeSelect.SetSelected("Artistic")
		}
		calibratedMode := colorCalibration.Overlays[overlayStateIndex].Mode == models.OverlayCalibratedLinear
		modeSelect.OnChanged = func(v string) {
			invalidateCalibration()
			state := &colorCalibration.Overlays[overlayStateIndex]
			if v == "Calibrated Linear" {
				state.Mode = models.OverlayCalibratedLinear
			} else {
				state.Mode = models.OverlayArtistic
			}
			state.Status = models.CalibrationStale
			calibratedMode = state.Mode == models.OverlayCalibratedLinear
			if calibratedMode {
				opacitySlider.Disable()
				protectSlider.Disable()
			} else {
				opacitySlider.Enable()
				protectSlider.Enable()
			}
			refresh()
		}
		neutralizeCheck := NewToggle(nil)
		neutralizeCheck.SetChecked(colorCalibration.Overlays[overlayStateIndex].NeutralizeBackground)
		neutralizeCheck.OnChanged = func(v bool) {
			invalidateCalibration()
			colorCalibration.Overlays[overlayStateIndex].NeutralizeBackground = v
			colorCalibration.Overlays[overlayStateIndex].Status = models.CalibrationStale
			refresh()
		}
		strengthSlider := widget.NewSlider(0, 2)
		strengthSlider.Step = 0.01
		strengthSlider.Value = colorCalibration.Overlays[overlayStateIndex].Strength
		strengthValue := NewNumberEntry(2, 0)
		strengthValue.Min, strengthValue.Max = 0, 2
		strengthValue.SetValue(strengthSlider.Value)
		strengthSlider.OnChanged = func(v float64) {
			invalidateCalibration()
			colorCalibration.Overlays[overlayStateIndex].Strength = v
			colorCalibration.Overlays[overlayStateIndex].Status = models.CalibrationStale
			strengthValue.SetValue(v)
			refresh()
		}
		strengthValue.OnChanged = func(v float64) {
			invalidateCalibration()
			colorCalibration.Overlays[overlayStateIndex].Strength = v
			colorCalibration.Overlays[overlayStateIndex].Status = models.CalibrationStale
			strengthSlider.Value = v
			strengthSlider.Refresh()
			refresh()
		}
		l.calibrationStatusLabel = widget.NewLabel("Calibration: " + string(colorCalibration.Overlays[overlayStateIndex].Status))
		if calibratedMode {
			opacitySlider.Disable()
			protectSlider.Disable()
		}
		colorControls := container.NewVBox(
			canvas.NewText("Overlay", color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255}),
			container.NewHBox(widget.NewLabel("Color"), swatch),
			colorSlider("Red", l.settings.ColorR, func(v uint8) { l.settings.ColorR = v }),
			colorSlider("Green", l.settings.ColorG, func(v uint8) { l.settings.ColorG = v }),
			colorSlider("Blue", l.settings.ColorB, func(v uint8) { l.settings.ColorB = v }),
			container.NewBorder(nil, nil, widget.NewLabel("Opacity"), container.NewHBox(opacityValue, widget.NewLabel("%")), opacitySlider),
			container.NewBorder(nil, nil, widget.NewLabel("Highlight protect"), container.NewHBox(protectValue, widget.NewLabel("%")), protectSlider),
			container.NewBorder(nil, nil, widget.NewLabel("Mix mode"), nil, modeSelect),
			container.NewHBox(neutralizeCheck, widget.NewLabel("Neutralize background")),
			container.NewBorder(nil, nil, widget.NewLabel("Linear strength"), strengthValue, strengthSlider),
			l.calibrationStatusLabel,
		)
		controls := container.NewVScroll(container.NewVBox(l.control.Content, colorControls))
		controls.SetMinSize(fyne.NewSize(300, 200))
		l.win = app.NewWindow(l.name + " Image")
		shield := newTapShield()
		l.win.SetContent(container.NewStack(container.NewBorder(nil, nil, controls, nil, l.viewport.container), shield))
		l.win.Resize(fyne.NewSize(900, 600))
		l.win.SetCloseIntercept(func() {
			l.win.SetCloseIntercept(nil)
			l.win.Close()
			removeLayer(l)
			refresh()
			if updateMenus != nil {
				updateMenus()
			}
		})
		if preparedPreview != nil {
			applyLayerPreview(l, preparedPreview)
			refresh()
		} else {
			refreshLayerPreview(l)
		}
		l.win.Show()
		// Newly created floating windows have their controls tapped before Fyne's
		// canvas cache is populated by the first paint pass, which crashes any
		// widget.Select tapped that early (CanvasForObject returns nil). The
		// shield eats input for a couple of frames to close that race.
		go func() {
			time.Sleep(200 * time.Millisecond)
			fyne.Do(func() { shield.Hide() })
		}()
	}
	openOverlayLayerWindow := func(l *overlayLayer) {
		openOverlayLayerWindowWithPreview(l, nil)
	}

	// freeOverlaySlot returns an idx for a new layer, reusing a freed hole when
	// available, else growing imgs/origPixels (bounded by maxOverlayLayers).
	freeOverlaySlot := func() (int, bool) {
		used := make(map[int]bool, len(overlayLayers))
		for _, l := range overlayLayers {
			used[l.idx] = true
		}
		for i := 3; i < len(imgs); i++ {
			if !used[i] && imgs[i] == nil {
				return i, true
			}
		}
		if len(imgs) >= 3+maxOverlayLayers {
			return 0, false
		}
		idx := len(imgs)
		imgs = append(imgs, nil)
		origPixels = append(origPixels, nil)
		return idx, true
	}

	createOverlayLayerAt := func(idx int, settings models.OrangeLayerState) *overlayLayer {
		for len(imgs) <= idx {
			imgs = append(imgs, nil)
			origPixels = append(origPixels, nil)
		}
		nextLayerNumber++
		if settings.BlinkID == "" {
			settings.BlinkID = fmt.Sprintf("overlay-%d", nextLayerNumber)
		}
		l := &overlayLayer{
			idx:      idx,
			name:     fmt.Sprintf("Layer %d", nextLayerNumber),
			settings: settings,
		}
		overlayLayers = append(overlayLayers, l)
		return l
	}

	createOverlayLayer := func(settings models.OrangeLayerState) (*overlayLayer, bool) {
		idx, ok := freeOverlaySlot()
		if !ok {
			return nil, false
		}
		return createOverlayLayerAt(idx, settings), true
	}

	addColoredLayer := func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			progressDialog := dialog.NewCustom("Loading Layer Image", "Reading FITS data...", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()
			diskLoad := largeMode
			go func() {
				// The staging slot is unique per request and becomes the runtime
				// descriptor slot once the overlay is created below.
				slot := fmt.Sprintf("overlay-stage-%d", time.Now().UnixNano())
				request, session, store := nextLargeLoadGeneration(slot)
				var img *models.LoadedImage
				var preview *image.RGBA
				var artifact composeArtifactDescriptor
				var loadErr error
				if diskLoad {
					// The runtime slot index is assigned only after the load succeeds;
					// use a unique staging slot and publish it under the assigned index.
					if store == nil {
						loadErr = errors.New("disk-backed Compose store is unavailable")
					} else {
						img, preview, artifact, loadErr = loadLargeComposeImage(path, store, slot)
					}
				} else {
					img, loadErr = loadImageFromPath(path)
				}
				fyne.Do(func() {
					progressDialog.Hide()
					if loadErr != nil {
						dialog.ShowError(loadErr, win)
						return
					}
					if diskLoad && !largeLoadStillCurrent(slot, request, session, artifact) {
						cleanupLargeArtifactIfCurrent(artifact)
						return
					}
					l, ok := createOverlayLayer(defaultOverlayLayerSettings(len(overlayLayers)))
					if !ok {
						if diskLoad {
							cleanupLargeArtifactIfCurrent(artifact)
						}
						dialog.ShowInformation("Layer limit", fmt.Sprintf("A maximum of %d colored layers is supported.", maxOverlayLayers), win)
						return
					}
					imgs[l.idx] = img
					if diskLoad {
						largeMu.Lock()
						current, currentOK := store.Descriptor(artifact.Slot)
						if largeStore != store || largeSessionGeneration != session || largeLoadGenerations[artifact.Slot] != request || !currentOK || current.Generation != artifact.Generation || current.Path != artifact.Path {
							largeMu.Unlock()
							cleanupLargeArtifactIfCurrent(artifact)
							return
						}
						largePreviews[l.idx] = preview
						largeArtifacts[l.idx] = artifact
						largeMu.Unlock()
					}
					invalidateCalibration()
					openOverlayLayerWindow(l)
					if updateMenus != nil {
						updateMenus()
					}
				})
			}()
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		if last := app.Preferences().String("lastDir"); last != "" {
			uri := storage.NewFileURI(last)
			if l, err := storage.ListerForURI(uri); err == nil {
				fd.SetLocation(l)
			}
		}
		fd.SetView(dialog.ListView)
		sizeFileDialog(fd)
		fd.Show()
	}

	// gatherLegendEntries snapshots the currently loaded base channels and the
	// active overlay layers into color/name rows for the Compose color legend.
	gatherLegendEntries := func() []legendEntry {
		var entries []legendEntry
		base := []struct {
			name string
			col  color.RGBA
		}{
			{"Blue", color.RGBA{R: 100, G: 149, B: 237, A: 255}},
			{"Green", color.RGBA{R: 80, G: 200, B: 80, A: 255}},
			{"Red", color.RGBA{R: 237, G: 80, B: 80, A: 255}},
		}
		for i, b := range base {
			if i < len(imgs) && imgs[i] != nil {
				filter := fitsio.FilterString(imgs[i].Primary)
				name := filter
				if name == "" {
					name = b.name
				}
				entries = append(entries, legendEntry{name: name, filter: filter, path: imgs[i].Path, color: b.col})
			}
		}
		for _, l := range overlayLayers {
			if l == nil || l.win == nil {
				continue
			}
			path, filter := "", ""
			if l.idx < len(imgs) && imgs[l.idx] != nil {
				path = imgs[l.idx].Path
				filter = fitsio.FilterString(imgs[l.idx].Primary)
			}
			name := filter
			if name == "" {
				name = l.name
			}
			entries = append(entries, legendEntry{
				name:   name,
				filter: filter,
				path:   path,
				color:  color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255},
			})
		}
		sortLegendEntriesByHue(entries)
		return entries
	}
	showColorLegend := func() {
		showLegendNameDialog(win, gatherLegendEntries(), func(named []legendEntry) {
			showColorLegendWindow(app, win, named)
		})
	}

	largeRuntime.store = largeStore
	controlSets = []*models.ChannelControl{
		channelControls("Channel 1 (Blue)", color.RGBA{R: 100, G: 149, B: 237, A: 255}, 0, imgs, &origPixels, viewports, refresh, composeMagicPreset, true, largeRuntime),
		channelControls("Channel 2 (Green)", color.RGBA{R: 80, G: 200, B: 80, A: 255}, 1, imgs, &origPixels, viewports, refresh, composeMagicPreset, true, largeRuntime),
		channelControls("Channel 3 (Red)", color.RGBA{R: 237, G: 80, B: 80, A: 255}, 2, imgs, &origPixels, viewports, refresh, composeMagicPreset, true, largeRuntime),
	}

	// The Manual Offset (X/Y/Rot) fields are the source of truth for each channel's
	// placement, applied at RENDER time only — imgs[idx] always holds the original
	// loaded pixels and is never warped/baked. renderImages() returns an
	// offset-applied view used for the composite (merge) and the per-channel
	// previews the blink shows. Results are cached so an unchanged offset isn't
	// re-warped on every refresh.
	composeChannelOffsetFields = func(idx int) (dx, dy, rot float64, ok bool) {
		if idx < 0 || idx >= len(controlSets) || controlSets[idx] == nil {
			return 0, 0, 0, false
		}
		cc := controlSets[idx]
		if cc.XOffsetEntry == nil || cc.YOffsetEntry == nil {
			return 0, 0, 0, false
		}
		dx = cc.XOffsetEntry.Value()
		dy = cc.YOffsetEntry.Value()
		if cc.RotOffsetEntry != nil {
			rot = cc.RotOffsetEntry.Value()
		}
		return dx, dy, rot, true
	}
	renderCache := make([]composeRenderCache, len(imgs))
	var renderMu sync.Mutex
	// renderImage returns imgs[idx] with the channel's Manual Offset applied at
	// render time (or imgs[idx] unchanged when there is no offset). It never
	// mutates imgs[idx].
	renderImage := func(idx int) *models.LoadedImage {
		if idx < 0 || idx >= len(imgs) || imgs[idx] == nil {
			return nil
		}
		dx, dy, rot, ok := composeChannelOffsetFields(idx)
		align, hasAlign := channelAlignTransform(imgs[idx])
		if (!ok || (dx == 0 && dy == 0 && rot == 0)) && !hasAlign {
			return imgs[idx]
		}
		src := imgs[idx].HDU.Data.Pixels
		var warpedPixels []float32
		if idx < len(renderCache) {
			renderMu.Lock()
			c := renderCache[idx]
			if c.pixels != nil && c.dx == dx && c.dy == dy && c.rot == rot && c.hasAlign == hasAlign && c.align == align && sameFloatSlice(c.src, src) {
				warpedPixels = c.pixels
			}
			renderMu.Unlock()
		}
		if warpedPixels == nil {
			w := imgs[idx].HDU.Data.Width
			h := imgs[idx].HDU.Data.Height
			// Backward sampling: output → manual nudge → star-alignment affine → source.
			t := composeManualOffsetTransform(w, h, dx, dy, rot)
			if hasAlign {
				t = processing.ComposeAffineTransforms(align, t)
			}
			warpedPixels = processing.WarpImage(src, w, h, t)
			if idx < len(renderCache) {
				renderMu.Lock()
				renderCache[idx] = composeRenderCache{dx: dx, dy: dy, rot: rot, hasAlign: hasAlign, align: align, src: src, pixels: warpedPixels}
				renderMu.Unlock()
			}
		}
		warped := *imgs[idx]
		warped.HDU.Data.Pixels = warpedPixels
		return &warped
	}
	renderImages = func() []*models.LoadedImage {
		out := make([]*models.LoadedImage, len(imgs))
		for i := range imgs {
			out[i] = renderImage(i)
		}
		return out
	}

	viewports[0].SetLoadSave("Blue", "B", color.RGBA{R: 100, G: 149, B: 237, A: 255},
		func() { loadChannel(0) }, func() { saveChannelGray(0) })
	viewports[1].SetLoadSave("Green", "G", color.RGBA{R: 80, G: 200, B: 80, A: 255},
		func() { loadChannel(1) }, func() { saveChannelGray(1) })
	viewports[2].SetLoadSave("Red", "R", color.RGBA{R: 237, G: 80, B: 80, A: 255},
		func() { loadChannel(2) }, func() { saveChannelGray(2) })
	compositeImageForEdit := func() *image.RGBA {
		// Always render from the save-gated snapshot. The cached composite
		// viewport may have been produced while a temporary Before/After
		// comparison override was active and must never leak into Export to Edit.
		buf, w, h, _, _, err := composeRGB(context.Background(), cloneCalibration())
		if err != nil || buf == nil {
			return nil
		}
		buf = processing.ApplyRGBLevels(buf, levels)
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		copy(img.Pix, buf)
		return img
	}
	startLargeEditSnapshot := func(d composeCompositeDescriptor, levelSnapshot models.RgbLevels) {
		// Capture the session owner before starting any asynchronous work. The
		// Compose session may be cleared or replaced while the copy is running;
		// the job must continue to use the immutable store pointer it started
		// with, rather than dereferencing the mutable outer largeStore variable.
		store := largeStore
		if store == nil {
			dialog.ShowInformation("Nothing to send", "The large-file Compose session is no longer available.", win)
			return
		}
		editSnapshotMu.Lock()
		if editSnapshotCancel != nil {
			editSnapshotMu.Unlock()
			dialog.ShowInformation("Send to Edit", "A composite snapshot is already in progress.", win)
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		editSnapshotCancel = cancel
		editSnapshotMu.Unlock()
		cancelButton := widget.NewButton("Cancel", cancel)
		progressDialog := dialog.NewCustomWithoutButtons("Sending to Edit", container.NewBorder(nil, cancelButton, nil, nil, container.NewVBox(widget.NewLabel("Copying the published composite…"), widget.NewProgressBarInfinite())), win)
		finished := false
		progressDialog.SetOnClosed(func() {
			if !finished {
				cancel()
			}
		})
		progressDialog.Show()
		go func() {
			ed, err := store.SnapshotCompositeForEdit(ctx, d)
			if err == nil {
				ed.levels = levelSnapshot
			}
			fyne.Do(func() {
				finished = true
				progressDialog.Hide()
				editSnapshotMu.Lock()
				editSnapshotCancel = nil
				editSnapshotMu.Unlock()
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						dialog.ShowInformation("Nothing to send", err.Error(), win)
					}
					return
				}
				if cur, ok := store.Composite(); !ok || cur.Generation != d.Generation {
					ed.cleanup()
					dialog.ShowInformation("Nothing to send", "The composite changed before it could be sent to Edit.", win)
					return
				}
				if globalExportToEdit != nil {
					if installErr := globalExportToEdit(editImageHandoff{disk: ed}); installErr != nil {
						ed.cleanup()
						dialog.ShowError(installErr, win)
					}
				}
			})
		}()
	}
	viewports[3].SetCenterAction("Composite", "C", color.RGBA{R: 200, G: 110, B: 30, A: 255},
		"Export to Edit", func() {
			if globalExportToEdit == nil {
				return
			}
			if largeMode {
				if largeStore != nil {
					if d, ok := largeStore.Composite(); ok {
						startLargeEditSnapshot(d, *levels)
						return
					}
				}
				dialog.ShowInformation("Build Composite first", "Build the composite before sending it to Edit.", win)
				return
			}
			img := compositeImageForEdit()
			if img == nil {
				dialog.ShowInformation("Nothing to export", "Compose all three channels first.", win)
				return
			}
			if err := globalExportToEdit(editImageHandoff{memory: img}); err != nil {
				dialog.ShowError(err, win)
			}
		})

	copySettings := func() {
		if imgs[0] == nil {
			dialog.ShowInformation("Missing", "Load Channel 1 first", win)
			return
		}
		missing := make([]string, 0, 2)
		for _, idx := range []int{1, 2} {
			if imgs[idx] == nil {
				missing = append(missing, fmt.Sprintf("Channel %d", idx+1))
			}
		}
		if len(missing) == 2 {
			dialog.ShowInformation("Missing", "Load Channel 2 and Channel 3 to copy settings", win)
			return
		}
		if len(missing) == 1 {
			dialog.ShowInformation("Missing", fmt.Sprintf("Load %s to copy settings", missing[0]), win)
		}
		src := imgs[0]
		withSuspendedRefresh(func() {
			for _, idx := range []int{1, 2} {
				if imgs[idx] == nil {
					continue
				}
				dst := imgs[idx]
				dst.Mode = src.Mode
				dst.Black = src.Black
				dst.White = src.White
				dst.Background = src.Background
				dst.Peak = src.Peak
				dst.ScaledPeak = src.ScaledPeak
				dst.ShowClip = src.ShowClip
				dst.AsinhScale = src.AsinhScale
				dst.MTFMidtone = src.MTFMidtone
				dst.GHSStretch = src.GHSStretch
				dst.GHSLocal = src.GHSLocal
				dst.GHSSymmetry = src.GHSSymmetry

				controlSets[idx].ModeSelect.SetSelected(modeToLabel(src.Mode))
				controlSets[idx].BackgroundEntry.SetValue(src.Background)
				controlSets[idx].PeakEntry.SetValue(src.Peak)
				controlSets[idx].ScaledPeakEntry.SetValue(src.ScaledPeak)
				setStretchParamEntries(controlSets[idx], src)
				controlSets[idx].ShowClip.SetChecked(src.ShowClip)
				viewports[idx].blackBox.SetValue(src.Black)
				viewports[idx].whiteBox.SetValue(src.White)
			}
		})
		refresh()
	}

	matchChannelStretch := func(refIdx int, matchStarCores bool) {
		if refIdx < 0 || refIdx >= 3 || imgs[refIdx] == nil {
			dialog.ShowInformation("Missing", "Load the reference channel first", win)
			return
		}
		refSnapshot := cloneLoadedImageForStretchMatch(imgs[refIdx])
		targetSnapshots := make([]*models.LoadedImage, 3)
		targetCount := 0
		for idx := 0; idx < 3; idx++ {
			if idx == refIdx || imgs[idx] == nil {
				continue
			}
			targetSnapshots[idx] = cloneLoadedImageForStretchMatch(imgs[idx])
			targetCount++
		}
		if targetCount == 0 {
			dialog.ShowInformation("No Targets", "Load at least one other channel to match.", win)
			return
		}
		if largeMode && largeStore != nil {
			progressDialog := dialog.NewCustom("Matching Channel Stretch", "Matching channel levels...", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()
			largeMu.RLock()
			refD, ok := largeArtifacts[refIdx]
			expectedTargets := make(map[int]composeArtifactDescriptor)
			for idx := range targetSnapshots {
				if d, exists := largeArtifacts[idx]; exists {
					expectedTargets[idx] = d
				}
			}
			largeMu.RUnlock()
			if !ok {
				progressDialog.Hide()
				dialog.ShowError(fmt.Errorf("missing disk artifact for reference channel"), win)
				return
			}
			go func(expected composeArtifactDescriptor) {
				refSummary, err := largeArtifactStretchSummary(expected.Path, refSnapshot, matchStarCores)
				type result struct {
					idx     int
					state   models.ChannelState
					preview *image.RGBA
					err     error
				}
				results := make([]result, 0, targetCount)
				if err == nil {
					for idx := range targetSnapshots {
						if targetSnapshots[idx] == nil {
							continue
						}
						largeMu.RLock()
						d, exists := largeArtifacts[idx]
						largeMu.RUnlock()
						if !exists {
							results = append(results, result{idx: idx, err: fmt.Errorf("missing disk artifact")})
							continue
						}
						anchors := refSummary.coreAnchors
						if anchors == nil {
							// A non-nil empty slice marks this as a target pass even
							// when the reference had too few usable anchors.
							anchors = []processing.Star{}
						}
						targetSummary, e := largeArtifactStretchSummaryWithAnchors(d.Path, targetSnapshots[idx], matchStarCores, anchors, expected.Width, expected.Height)
						st := channelStateFromImage(targetSnapshots[idx])
						if e == nil {
							e = applyLargeStretchMatch(&st, refSnapshot, refSummary, targetSummary)
						}
						var p *image.RGBA
						if e == nil {
							clone := *targetSnapshots[idx]
							applyChannelStateToImage(&clone, st)
							p, _, _, e = composeLargeStretchedPreview(d.Path, &clone)
						}
						results = append(results, result{idx: idx, state: st, preview: p, err: e})
					}
				}
				if err != nil {
					results = append(results, result{err: err})
				}
				fyne.Do(func() {
					progressDialog.Hide()
					largeMu.RLock()
					currentRef, refCurrent := largeArtifacts[refIdx]
					largeMu.RUnlock()
					if !refCurrent || currentRef.Generation != expected.Generation || currentRef.Path != expected.Path {
						return
					}
					for _, r := range results {
						if r.err != nil {
							dialog.ShowError(r.err, win)
							continue
						}
						largeMu.RLock()
						cur, current := largeArtifacts[r.idx]
						largeMu.RUnlock()
						expected, expectedOK := expectedTargets[r.idx]
						if !expectedOK || !current || cur.Generation != expected.Generation || cur.Path != expected.Path {
							continue
						}
						applyChannelState(r.idx, r.state, imgs, viewports, controlSets)
						if r.preview != nil {
							largePreviews[r.idx] = r.preview
						}
					}
					refresh()
				})
			}(refD)
			return
		}

		progressDialog := dialog.NewCustom("Matching Channel Stretch", "Matching channel levels...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()
		go func() {
			type matchResult struct {
				idx   int
				state models.ChannelState
				err   error
			}
			results := make([]matchResult, 0, targetCount)
			for idx, target := range targetSnapshots {
				if target == nil {
					continue
				}
				err := matchComposeChannelStretch(refSnapshot, target, matchStarCores)
				results = append(results, matchResult{idx: idx, state: channelStateFromImage(target), err: err})
			}
			fyne.Do(func() {
				progressDialog.Hide()
				applied := 0
				failed := make([]string, 0, len(results))
				withSuspendedRefresh(func() {
					for _, res := range results {
						if res.err != nil {
							failed = append(failed, fmt.Sprintf("Channel %d: %v", res.idx+1, res.err))
							continue
						}
						if imgs[res.idx] == nil {
							failed = append(failed, fmt.Sprintf("Channel %d: no longer loaded", res.idx+1))
							continue
						}
						applyChannelState(res.idx, res.state, imgs, viewports, controlSets)
						applied++
					}
				})
				refresh()
				if len(failed) > 0 {
					dialog.ShowError(fmt.Errorf("%s", strings.Join(failed, "\n")), win)
					return
				}
				if applied == 0 {
					dialog.ShowInformation("No Targets", "No channels were matched.", win)
				}
			})
		}()
	}

	showMatchStretchDialog := func() {
		options := []string{}
		optionIdx := []int{}
		for i, name := range composeBlinkFilterNames {
			if imgs[i] == nil {
				continue
			}
			options = append(options, name)
			optionIdx = append(optionIdx, i)
		}
		if len(options) == 0 {
			dialog.ShowInformation("Missing", "Load a reference channel first.", win)
			return
		}
		refIdx := optionIdx[0]
		refSelect := NewSafeSelect(options, func(s string) {
			for i, name := range options {
				if name == s {
					refIdx = optionIdx[i]
					return
				}
			}
		})
		refSelect.SetSelected(options[0])
		starCoreCheck := NewToggle(nil)
		starCoreCheck.SetChecked(true)

		content := container.NewVBox(
			widget.NewForm(widget.NewFormItem("Reference", refSelect)),
			container.NewHBox(starCoreCheck, widget.NewLabel("Match star cores")),
			widget.NewLabel("Saturated star cores are ignored automatically."),
		)
		d := dialog.NewCustomConfirm("Match Channel Stretch", "Apply", "Cancel", content, func(ok bool) {
			if !ok {
				return
			}
			matchChannelStretch(refIdx, starCoreCheck.Checked)
		}, win)
		d.Show()
	}

	saveProject := func() {
		hasChannel := false
		project := models.ComposeProject{
			SharedHistogramScale: sharedHistCheck.Checked,
			DisableComposite:     !buildCompositeCheck.Checked,
			MeasureComposite:     measureEnabled,
			BlinkFilters:         blinkCheck.Checked,
			BlinkExcludedFilter:  blinkExcludedIdx,
			// New saves use ColorCalibration pointer presence as the canonical
			// persisted indicator; retain the legacy field only for decoding.
			DisableColorCalibration: false,
		}
		if saveColorCalibration {
			calibrationCopy := colorCalibration
			calibrationCopy.Overlays = append([]models.OverlayCalibrationState(nil), colorCalibration.Overlays...)
			project.ColorCalibration = &calibrationCopy
		}
		if blinkChannels != nil {
			selection := append([]int(nil), blinkChannels...)
			project.BlinkChannels = &selection
			keys := make([]string, 0, len(selection))
			for _, index := range selection {
				for _, source := range composeBlinkSources() {
					if source.ProjectIndex == index {
						keys = append(keys, source.Key)
						break
					}
				}
			}
			project.BlinkChannelKeys = &keys
		}
		for i := 0; i < 3; i++ {
			if imgs[i] == nil {
				continue
			}
			hasChannel = true
			dx, dy, rot, _ := composeChannelOffsetFields(i)
			project.Channels[i] = models.ChannelState{
				Path:       imgs[i].Path,
				Mode:       modeToLabel(imgs[i].Mode),
				Black:      imgs[i].Black,
				White:      imgs[i].White,
				Background: imgs[i].Background,
				Peak:       imgs[i].Peak,
				ScaledPeak: imgs[i].ScaledPeak,
				ShowClip:   imgs[i].ShowClip,
				OffsetX:    dx,
				OffsetY:    dy,
				OffsetRot:  rot,
				Rotation90: imgs[i].Rotation90,

				HasAlign: imgs[i].HasAlignTransform,
				AlignA:   imgs[i].AlignA,
				AlignB:   imgs[i].AlignB,
				AlignC:   imgs[i].AlignC,
				AlignD:   imgs[i].AlignD,
				AlignE:   imgs[i].AlignE,
				AlignF:   imgs[i].AlignF,

				AsinhScale:  imgs[i].AsinhScale,
				MTFMidtone:  imgs[i].MTFMidtone,
				GHSStretch:  imgs[i].GHSStretch,
				GHSLocal:    imgs[i].GHSLocal,
				GHSSymmetry: imgs[i].GHSSymmetry,
			}
		}
		for _, l := range overlayLayers {
			if l.win == nil {
				continue
			}
			layer := l.settings
			layer.Open = true
			if l.idx < len(imgs) && imgs[l.idx] != nil {
				hasChannel = true
				layer.Channel = channelStateFromImage(imgs[l.idx])
			}
			project.OverlayLayers = append(project.OverlayLayers, layer)
		}
		if !hasChannel {
			dialog.ShowInformation("Nothing to save", "Load at least one channel before saving", win)
			return
		}
		if err := gofiledialog.ShowSave(func(paths []string, err error) {
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			if len(paths) == 0 {
				return
			}
			data, err := json.MarshalIndent(project, "", "  ")
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			if err := os.WriteFile(paths[0], data, 0644); err != nil {
				dialog.ShowError(err, win)
				return
			}
		}, win,
			gofiledialog.WithFileName("project.gfprj"),
			gofiledialog.WithFilters(gofiledialog.Filter{Name: "Compose projects", Extensions: []string{".gfprj"}}),
		); err != nil {
			dialog.ShowError(err, win)
		}
	}

	loadProject := func() {
		if err := gofiledialog.ShowOpen(func(paths []string, err error) {
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			if len(paths) == 0 {
				return
			}
			data, err := os.ReadFile(paths[0])
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			var project models.ComposeProject
			if err := json.Unmarshal(data, &project); err != nil {
				dialog.ShowError(err, win)
				return
			}
			clearGaiaRefinement()
			previousCalibration := colorCalibration
			previousSaveCalibration := saveColorCalibration
			if project.ColorCalibration != nil {
				colorCalibration = *project.ColorCalibration
				colorCalibration.Overlays = append([]models.OverlayCalibrationState(nil), project.ColorCalibration.Overlays...)
			} else {
				colorCalibration = models.ColorCalibrationState{Status: models.CalibrationDisabled}
			}
			saveColorCalibration = project.ColorCalibration != nil && !project.DisableColorCalibration

			if globalSelectComposeTab != nil {
				globalSelectComposeTab()
			}

			progressDialog := dialog.NewCustom("Loading Project", "Reading FITS files and restoring saved stretch settings...", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()

			// Normalize overlay layers: new projects carry OverlayLayers; migrate
			// legacy Orange/Yellow layers from older projects into the same list.
			layerStates := append([]models.OrangeLayerState(nil), project.OverlayLayers...)
			if project.OrangeLayer.Open {
				layerStates = append(layerStates, project.OrangeLayer)
			}
			if project.YellowLayer.Open {
				layerStates = append(layerStates, project.YellowLayer)
			}
			if len(layerStates) > maxOverlayLayers {
				layerStates = layerStates[:maxOverlayLayers]
			}
			// Keep a detached snapshot until every incoming source has staged
			// successfully; large-mode failure restores these runtime references.
			previousImgs := append([]*models.LoadedImage(nil), imgs...)
			previousOrig := append([][]float32(nil), origPixels...)
			previousOverlays := append([]*overlayLayer(nil), overlayLayers...)
			previousArtifacts := make(map[int]composeArtifactDescriptor)
			previousPreviews := make(map[int]*image.RGBA)
			if largeMode {
				largeMu.RLock()
				for idx, d := range largeArtifacts {
					previousArtifacts[idx] = d
				}
				for idx, p := range largePreviews {
					previousPreviews[idx] = p
				}
				largeMu.RUnlock()
			}

			// Tear down existing overlay windows and rebuild imgs/origPixels to hold
			// the 3 base RGB channels plus one slot per incoming layer.
			for _, l := range overlayLayers {
				if l.win != nil {
					l.win.SetCloseIntercept(nil)
					l.win.Close()
				}
			}
			overlayLayers = nil
			imgs = imgs[:3+len(layerStates)]
			origPixels = origPixels[:3+len(layerStates)]
			for i := 3; i < len(imgs); i++ {
				imgs[i] = nil
				origPixels[i] = nil
			}
			for i, st := range layerStates {
				s := st
				s.Open = true
				s = normalizeComposeOverlayState(s, i)
				nextLayerNumber++
				overlayLayers = append(overlayLayers, &overlayLayer{
					idx:      3 + i,
					name:     fmt.Sprintf("Layer %d", nextLayerNumber),
					settings: s,
				})
			}

			go func() {
				type loadResult struct {
					idx      int
					img      *models.LoadedImage
					preview  *image.RGBA
					artifact composeArtifactDescriptor
					state    models.ChannelState
					err      error
				}

				total := len(imgs)
				stateForIdx := func(i int) models.ChannelState {
					if i < 3 {
						return project.Channels[i]
					}
					return layerStates[i-3].Channel
				}
				results := make([]loadResult, 0, total)
				errors := make([]string, 0, total)
				if largeMode {
					// Large-mode project loads are deliberately sequential. Each source is
					// staged through the artifact transaction before its bounded preview is
					// generated; no legacy full-pixel loader is used.
					for i := 0; i < total; i++ {
						state := stateForIdx(i)
						res := loadResult{idx: i, state: state}
						if state.Path != "" {
							slot := fmt.Sprintf("project-%d", i)
							if largeStore == nil {
								res.err = fmt.Errorf("disk-backed Compose store is unavailable")
							} else {
								img, preview, artifact, loadErr := loadLargeComposeImage(state.Path, largeStore, slot)
								res.img, res.preview, res.artifact, res.err = img, preview, artifact, loadErr
								if res.err == nil {
									turns := state.Rotation90 % 4
									for turns > 0 {
										artifact, res.err = largeStore.RotateArtifact90CW(artifact)
										if res.err != nil {
											break
										}
										res.artifact = artifact
										img.HDU.Data.Width, img.HDU.Data.Height = artifact.Width, artifact.Height
										turns--
									}
									if res.err == nil && state.Rotation90%4 != 0 {
										res.preview, _, _, res.err = composeLargeStretchedPreview(artifact.Path, img)
									}
								}
							}
						}
						results = append(results, res)
					}
				} else {
					resultsCh := make(chan loadResult, total)
					var wg sync.WaitGroup
					for i := 0; i < total; i++ {
						state := stateForIdx(i)
						if state.Path == "" {
							results = append(results, loadResult{idx: i, state: state})
							continue
						}
						wg.Add(1)
						go func(idx int, state models.ChannelState) {
							defer wg.Done()
							img, loadErr := loadImageFromPath(state.Path)
							resultsCh <- loadResult{idx: idx, img: img, state: state, err: loadErr}
						}(i, state)
					}
					go func() { wg.Wait(); close(resultsCh) }()
					for res := range resultsCh {
						results = append(results, res)
					}
				}
				for _, res := range results {
					if res.state.Path == "" {
						imgs[res.idx] = nil
						continue
					}
					if res.err != nil {
						label := fmt.Sprintf("Channel %d", res.idx+1)
						if res.idx >= 3 {
							label = fmt.Sprintf("Layer %d", res.idx-2)
						}
						errors = append(errors, fmt.Sprintf("%s: %v", label, res.err))
						continue
					}
					imgs[res.idx] = res.img
					if largeMode {
						largeMu.Lock()
						largeArtifacts[res.idx] = res.artifact
						largePreviews[res.idx] = res.preview
						largeMu.Unlock()
					}
					clearComposeOrigPixels(&origPixels, res.idx)
				}
				if largeMode && len(errors) > 0 {
					// Project replacement is all-or-none in disk mode: discard every
					// staged artifact when any source fails, including artifacts already
					// published into the temporary result maps above.
					for _, res := range results {
						if res.artifact.Slot != "" {
							_, _ = largeStore.RemoveSlotIfCurrent(res.artifact)
						}
						imgs[res.idx] = nil
						largeMu.Lock()
						delete(largeArtifacts, res.idx)
						delete(largePreviews, res.idx)
						largeMu.Unlock()
					}
				}

				fyne.Do(func() {
					if largeMode && len(errors) > 0 {
						// Restore the detached runtime snapshot before reporting failure.
						restoreComposeLargeProjectSnapshot(&imgs, &origPixels, &overlayLayers, &largeArtifacts, &largePreviews, &colorCalibration, &saveColorCalibration, previousImgs, previousOrig, previousOverlays, previousArtifacts, previousPreviews, previousCalibration, previousSaveCalibration)
						largeMu.Lock()
						largeArtifacts = previousArtifacts
						largePreviews = previousPreviews
						largeRuntime.artifacts = largeArtifacts
						largeRuntime.previews = largePreviews
						largeMu.Unlock()
						colorCalibration = previousCalibration
						saveColorCalibration = previousSaveCalibration
						for _, l := range overlayLayers {
							if l.win != nil {
								l.win.SetCloseIntercept(nil)
								l.win.Close()
							}
						}
						for _, l := range previousOverlays {
							l.win, l.viewport, l.control = nil, nil, nil
							openOverlayLayerWindow(l)
						}
						blinkChannels = nil
						blinkCheck.SetChecked(false)
						if stopBlink != nil {
							stopBlink()
						}
						dialog.ShowError(fmt.Errorf("%s", strings.Join(errors, "\n")), win)
						progressDialog.Hide()
						return
					} else if largeMode {
						// Commit succeeded; old session slots can now be reclaimed.
						largeMu.RLock()
						currentArtifacts := make(map[int]composeArtifactDescriptor, len(largeArtifacts))
						for idx, d := range largeArtifacts {
							currentArtifacts[idx] = d
						}
						largeMu.RUnlock()
						for idx, old := range previousArtifacts {
							if cur, ok := currentArtifacts[idx]; !ok || cur.Path != old.Path {
								_, _ = largeStore.RemoveSlotIfCurrent(old)
							}
						}
					}
					debuglog.Log("load compose project: applying loaded project state")
					withSuspendedRefresh(func() {
						for i, img := range imgs[:3] {
							if img != nil {
								if largeMode {
									img.Rotation90 = project.Channels[i].Rotation90
								} else {
									restoreComposeChannelRotation(img, project.Channels[i].Rotation90)
								}
								applyChannelState(i, project.Channels[i], imgs, viewports, controlSets)
							}
						}
						sharedHistCheck.SetChecked(project.SharedHistogramScale)
						buildCompositeCheck.SetChecked(!project.DisableComposite)
						if stopBlink != nil {
							stopBlink()
						}
						blinkCheck.SetChecked(false)
						blinkExcludedIdx = clampComposeBlinkFilter(project.BlinkExcludedFilter)
						if project.BlinkChannelKeys != nil {
							blinkChannels = resolveComposeBlinkKeys(*project.BlinkChannelKeys, composeBlinkSources())
						} else if project.BlinkChannels != nil {
							selection := make([]int, len(*project.BlinkChannels))
							copy(selection, *project.BlinkChannels)
							blinkChannels = resolveComposeBlinkSelection(composeBlinkSources(), selection, project.BlinkFilters, project.BlinkExcludedFilter)
						} else {
							blinkChannels = resolveComposeBlinkSelection(composeBlinkSources(), nil, project.BlinkFilters, project.BlinkExcludedFilter)
						}
						if largeMode {
							blinkCheck.SetChecked(false)
							blinkChannels = nil
						} else {
							blinkCheck.SetChecked(project.BlinkFilters)
						}
						measureEnabled = project.MeasureComposite
						if measureCheck != nil {
							measureCheck.SetChecked(measureEnabled)
						}
						if !measureEnabled {
							measureStart = nil
							measureEnd = nil
						}
						if updateHistScaleLabel != nil {
							updateHistScaleLabel()
						}
						if updateBlinkStatus != nil {
							updateBlinkStatus()
						}
						if updateMeasurement != nil {
							updateMeasurement()
						}
					})
					debuglog.Log("load compose project: loaded images applied; refreshing previews asynchronously")
					for idx := range headerWins {
						closeHeaderWindow(idx)
					}
					if updateMenus != nil {
						updateMenus()
					}
					for _, l := range overlayLayers {
						openOverlayLayerWindow(l)
						if l.idx < len(imgs) && imgs[l.idx] != nil && l.control != nil {
							applyChannelState(l.idx, layerStates[l.idx-3].Channel, imgs, layerViews(l), layerControls(l))
							refreshLayerPreview(l)
						}
					}
					if len(errors) > 0 {
						dialog.ShowError(fmt.Errorf("%s", strings.Join(errors, "\n")), win)
					}
					refreshAsync(func() {
						if !largeMode && blinkCheck.Checked && startBlink != nil {
							startBlink()
						}
						progressDialog.Hide()
						debuglog.Log("load compose project: previews refreshed")
					})
				})
			}()
		}, win,
			gofiledialog.WithFilters(gofiledialog.Filter{Name: "Compose projects", Extensions: []string{".gfprj"}}),
		); err != nil {
			dialog.ShowError(err, win)
		}
	}

	type vpState struct {
		zoom    float64
		zoomSel string
		offset  fyne.Position
	}

	captureViewportStates := func() []vpState {
		states := make([]vpState, len(viewports))
		for i, vp := range viewports {
			if vp != nil {
				states[i] = vpState{
					zoom:    vp.zoom,
					zoomSel: vp.zoomLabel.Selected,
					offset:  vp.scroll.Offset,
				}
			}
		}
		return states
	}

	restoreViewportStates := func(states []vpState) {
		for i, vp := range viewports {
			if vp != nil && i < len(states) {
				vp.zoom = states[i].zoom
				vp.setZoomLabelValue(states[i].zoomSel)
				vp.applyZoom()
				vp.scroll.Offset = states[i].offset
				vp.scroll.Refresh()
			}
		}
	}

	resetData := func() {
		largeMu.RLock()
		store := largeStore
		largeMu.RUnlock()
		if largeMode && store != nil {
			savedStates := captureViewportStates()
			expected := make([]composeArtifactDescriptor, 0, 3)
			selected := make([]*models.LoadedImage, 0, 3)
			identities := make([]*models.LoadedImage, 0, 3)
			indices := make([]int, 0, 3)
			requests := make(map[int]uint64)
			var session uint64
			largeMu.RLock()
			session = largeSessionGeneration
			store = largeStore
			if store == nil {
				largeMu.RUnlock()
				dialog.ShowInformation("Reset", "The large-file Compose session is no longer available.", win)
				return
			}
			for i := 0; i < 3; i++ {
				if imgs[i] == nil {
					continue
				}
				d, ok := largeArtifacts[i]
				if !ok {
					largeMu.RUnlock()
					dialog.ShowError(fmt.Errorf("missing disk artifact for Channel %d", i+1), win)
					return
				}
				expected = append(expected, d)
				identities = append(identities, imgs[i])
				// Copy the complete small channel state while holding the same lock
				// used by large-mode writers. Reset staging must never read a live
				// LoadedImage while a channel job is committing metadata.
				snapshot := *imgs[i]
				snapshot.HDU = imgs[i].HDU
				snapshot.HDU.Header.Cards = make(map[string]string, len(imgs[i].HDU.Header.Cards))
				for key, value := range imgs[i].HDU.Header.Cards {
					snapshot.HDU.Header.Cards[key] = value
				}
				snapshot.HDU.Data.Pixels = nil
				snapshot.HDU.Data.Int32Pixels = nil
				selected = append(selected, &snapshot)
				indices = append(indices, i)
				slot := fmt.Sprintf("channel-%d", i)
				requests[i] = largeLoadGenerations[slot]
			}
			largeMu.RUnlock()
			if len(selected) == 0 {
				dialog.ShowInformation("Reset", "No loaded channels to reset.", win)
				return
			}
			progressDialog := dialog.NewCustom("Reset", "Restoring channels from disk…", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()
			go func() {
				// Never hold largeMu across FITS I/O. Clear Channels and other UI
				// invalidation paths must be able to advance the generation while
				// this reset is staging its transactional replacements.
				resetStillCurrent := func() bool {
					largeMu.RLock()
					defer largeMu.RUnlock()
					if largeStore != store || largeSessionGeneration != session {
						return false
					}
					for j, idx := range indices {
						slot := fmt.Sprintf("channel-%d", idx)
						if imgs[idx] != identities[j] || largeLoadGenerations[slot] != requests[idx] {
							return false
						}
						current, ok := largeArtifacts[idx]
						if !ok || current.Generation != expected[j].Generation || current.Path != expected[j].Path {
							return false
						}
						stored, ok := store.Descriptor(slot)
						if !ok || stored.Generation != expected[j].Generation || stored.Path != expected[j].Path {
							return false
						}
					}
					return true
				}
				if !resetStillCurrent() {
					fyne.Do(func() {
						progressDialog.Hide()
						dialog.ShowInformation("Reset", "Channels changed before reset started.", win)
					})
					return
				}
				descs, previews, hdus, primaries, err := stageComposeLargeReset(context.Background(), store, selected, expected)
				if err != nil {
					fyne.Do(func() {
						progressDialog.Hide()
						dialog.ShowError(err, win)
					})
					return
				}
				fyne.Do(func() {
					progressDialog.Hide()
					// Revalidate image identity and artifact generations under the
					// short publish lock immediately before mutating live state.
					largeMu.Lock()
					publishCurrent := largeStore == store && largeSessionGeneration == session
					for j, idx := range indices {
						slot := fmt.Sprintf("channel-%d", idx)
						current, ok := largeArtifacts[idx]
						stored, storedOK := store.Descriptor(slot)
						if !publishCurrent || imgs[idx] != identities[j] || largeLoadGenerations[slot] != requests[idx] || !ok || current.Generation != expected[j].Generation || current.Path != expected[j].Path || !storedOK || stored.Generation != descs[j].Generation || stored.Path != descs[j].Path {
							publishCurrent = false
							break
						}
					}
					if !publishCurrent {
						largeMu.Unlock()
						for _, d := range descs {
							_, _ = store.RemoveSlotIfCurrent(d)
						}
						dialog.ShowInformation("Reset", "Channels changed while reset was running.", win)
						return
					}
					for j, idx := range indices {
						imgs[idx].HDU, imgs[idx].Primary = hdus[j], primaries[j]
						imgs[idx].HDU.Data.Width, imgs[idx].HDU.Data.Height = descs[j].Width, descs[j].Height
						imgs[idx].HDU.Data.Pixels = nil
						largeArtifacts[idx], largePreviews[idx] = descs[j], previews[j]
					}
					largeMu.Unlock()
					invalidateCalibration()
					refresh()
					restoreViewportStates(savedStates)
					dialog.ShowInformation("Reset Complete", "Channels restored from disk.", win)
				})
			}()
			return
		}
		loaded := false
		errors := make([]string, 0, 3)

		savedStates := captureViewportStates()

		withSuspendedRefresh(func() {
			for i := 0; i < 3; i++ {
				if imgs[i] == nil {
					continue
				}
				loaded = true
				state := channelStateFromImage(imgs[i])
				reloaded, err := loadImageFromPath(imgs[i].Path)
				if err != nil {
					errors = append(errors, fmt.Sprintf("Channel %d: %v", i+1, err))
					continue
				}
				imgs[i] = reloaded
				clearComposeOrigPixels(&origPixels, i)
				restoreComposeChannelRotation(reloaded, state.Rotation90)
				applyChannelState(i, state, imgs, viewports, controlSets)
			}
		})
		invalidateCalibration()

		if !loaded {
			dialog.ShowInformation("Reset", "No loaded channels to reset.", win)
			return
		}

		refresh()
		restoreViewportStates(savedStates)
		if len(errors) > 0 {
			dialog.ShowError(fmt.Errorf("%s", strings.Join(errors, "\n")), win)
			return
		}
		dialog.ShowInformation("Reset Complete", "Channels restored from disk.", win)
	}

	alignChannels := func() {
		if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
			dialog.ShowInformation("Missing Channels", "Load all three FITS channels before aligning.", win)
			return
		}
		if largeMode && largeStore != nil {
			progressDialog := dialog.NewCustom("Aligning", "Extracting star catalogs...", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()
			go func() {
				largeMu.RLock()
				descs := make(map[int]composeArtifactDescriptor, 3)
				channels := make([]composeAlignmentChannel, 0, 3)
				for _, idx := range []int{0, 1, 2} {
					d, exists := largeArtifacts[idx]
					if exists {
						descs[idx] = d
					}
				}
				largeMu.RUnlock()
				var err error
				for _, idx := range []int{0, 1, 2} {
					d, exists := descs[idx]
					if !exists {
						err = fmt.Errorf("missing disk artifact for Channel %d", idx+1)
						break
					}
					stars, e := largeArtifactStars(d.Path)
					if e != nil {
						err = e
						break
					}
					channels = append(channels, composeAlignmentChannel{Index: idx + 1, Width: d.Width, Height: d.Height, UsableStars: stars, Footprint: composeAlignmentFootprint{MaxX: float64(d.Width), MaxY: float64(d.Height)}})
				}
				match := func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
					if len(target.UsableStars) < 3 || len(reference.UsableStars) < 3 {
						return composeAlignmentMatch{}, fmt.Errorf("insufficient stars found for alignment")
					}
					sx, sy := float64(reference.Width)/float64(target.Width), float64(reference.Height)/float64(target.Height)
					targetStars := make([]processing.Star, len(target.UsableStars))
					for i, star := range target.UsableStars {
						targetStars[i] = star
						targetStars[i].X *= sx
						targetStars[i].Y *= sy
					}
					// Both catalogs are now expressed in the reference pixel frame;
					// use the same robust histogram/mutual-neighbour fit and global
					// refinement as the normal alignment path.
					forward, stats, e := processing.FitCatalogResidual(targetStars, reference.UsableStars, reference.Width, reference.Height, 30, "general")
					if e != nil {
						return composeAlignmentMatch{}, e
					}
					// FitCatalogResidual maps the resized target catalog into the
					// reference frame. Convert that forward transform back to the
					// target's native frame exactly as the normal pixel path does.
					return composeAlignmentMatchFromFittedAffine(target, reference, forward, stats), nil
				}
				alignment := composeAlignmentResult{}
				if err == nil {
					alignment = coordinateComposeAlignmentWithEligibility(channels, 2, match, composeAlignmentFallbackPairEligible)
				}
				fyne.Do(func() {
					progressDialog.Hide()
					if err != nil {
						dialog.ShowError(err, win)
						return
					}
					// Channel 2 is the alignment root.  Do not commit either
					// fitted result if the reference artifact was replaced while
					// catalogs were being extracted.
					refExpected := descs[1]
					refCurrent, refOK := largeStore.Descriptor(refExpected.Slot)
					if !refOK || !composeLargeAlignmentReferenceCurrent(refCurrent, refExpected) {
						return
					}
					for _, idx := range []int{0, 2} {
						res := alignment.Channels[idx+1]
						expected := descs[idx]
						cur, current := largeStore.Descriptor(expected.Slot)
						if !current || cur.Generation != expected.Generation || cur.Path != expected.Path || !res.Applicable {
							continue
						}
						setChannelAlignTransform(imgs[idx], res.Backward)
						// The fitted affine supersedes the user nudge, matching the
						// normal alignment path. Keep controls and metadata in sync.
						if idx < len(controlSets) {
							resetComposeAlignmentOffsets(controlSets[idx])
						}
						invalidateCalibration()
					}
					refresh()
				})
			}()
			return
		}

		savedStates := captureViewportStates()

		// Reference is Channel 2 (green); align Channel 1 (blue) and Channel 3 (red)
		// to it. imgs always holds the ORIGINAL pixels (offsets are applied only at
		// render time), so the computed offset is absolute. Store it in the Manual
		// Offset fields (the source of truth); the refresh below renders it.
		channels := []composeAlignmentChannel{
			{Index: 1, OriginalPixels: imgs[0].HDU.Data.Pixels, Width: imgs[0].HDU.Data.Width, Height: imgs[0].HDU.Data.Height},
			{Index: 2, OriginalPixels: imgs[1].HDU.Data.Pixels, Width: imgs[1].HDU.Data.Width, Height: imgs[1].HDU.Data.Height},
			{Index: 3, OriginalPixels: imgs[2].HDU.Data.Pixels, Width: imgs[2].HDU.Data.Width, Height: imgs[2].HDU.Data.Height},
		}

		progressDialog := dialog.NewCustom("Aligning", "Please wait...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			// alignOne returns the backward (output→source) transform that registers
			// base to the reference, using the same robust pixel-space star matcher
			// the mosaic builder uses (WCS-independent: channel WCS headers can
			// disagree with the real pixel registration by ~100 px).
			match := func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
				_, fitted, stats, err := processing.AlignChannelByStars(
					target.OriginalPixels, target.Width, target.Height,
					reference.OriginalPixels, reference.Width, reference.Height, 30.0, "general",
				)
				if err != nil {
					return composeAlignmentMatch{}, err
				}
				return composeAlignmentMatchFromFittedAffine(target, reference, fitted, stats), nil
			}

			for i := range channels {
				channels[i].Footprint = composeAlignmentFootprint{MaxX: float64(channels[i].Width), MaxY: float64(channels[i].Height)}
				channels[i].UsableStars = processing.ExtractStars(channels[i].OriginalPixels, channels[i].Width, channels[i].Height, 4.0, 3)
			}
			alignment := coordinateComposeAlignmentWithEligibility(channels, 2, match, composeAlignmentFallbackPairEligible)

			fyne.Do(func() {
				progressDialog.Hide()

				// Write the alignment into the Manual Offset fields; the offset is
				// applied at render time (not baked) by the refresh below.
				setAndApply := func(idx int, back processing.AffineTransform) (dx, dy, rot float64) {
					w := imgs[idx].HDU.Data.Width
					h := imgs[idx].HDU.Data.Height
					// Store the full fitted affine (scale/skew included); the Manual
					// Offset becomes a zeroed user nudge applied on top of it. The
					// returned dx/dy/rot are the equivalent translation/rotation for
					// the summary line only.
					dx, dy, rot = extractManualOffset(back, w, h)
					setChannelAlignTransform(imgs[idx], back)
					invalidateCalibration()
					if idx < len(controlSets) && controlSets[idx] != nil {
						if controlSets[idx].XOffsetEntry != nil {
							controlSets[idx].XOffsetEntry.SetValue(0)
						}
						if controlSets[idx].YOffsetEntry != nil {
							controlSets[idx].YOffsetEntry.SetValue(0)
						}
						if controlSets[idx].RotOffsetEntry != nil {
							controlSets[idx].RotOffsetEntry.SetValue(0)
						}
					}
					return dx, dy, rot
				}

				resultDetail := func(result composeAlignmentChannelResult) string {
					detail := fmt.Sprintf("matched=%d inliers=%d rms=%.2f", result.Stats.MatchedStars, result.Stats.GlobalInliers, result.Stats.RMS)
					if !result.Direct {
						detail += fmt.Sprintf(" via Channel %d", result.ReferenceIndex)
					}
					return detail
				}
				blueResult := alignment.Channels[1]
				redResult := alignment.Channels[3]
				blueLine := resultDetail(blueResult)
				var bdx, bdy, brot float64
				var errBlue, errRed error
				if blueResult.Applicable {
					bdx, bdy, brot = setAndApply(0, blueResult.Backward)
				} else {
					errBlue = composeAlignmentResultError(alignment, 1, blueResult)
					blueLine = "FAILED: " + errBlue.Error()
				}
				redLine := resultDetail(redResult)
				var rdx, rdy, rrot float64
				if redResult.Applicable {
					rdx, rdy, rrot = setAndApply(2, redResult.Backward)
				} else {
					errRed = composeAlignmentResultError(alignment, 3, redResult)
					redLine = "FAILED: " + errRed.Error()
				}

				refresh()
				restoreViewportStates(savedStates)

				if errBlue != nil && errRed != nil {
					dialog.ShowError(fmt.Errorf("Blue: %v\nRed: %v", errBlue, errRed), win)
					return
				}
				msg := fmt.Sprintf("Alignment Complete.\n\nBlue (Channel 1):\n  X: %+.2f  Y: %+.2f  Rot: %+.2f°\n  %s\n\nRed (Channel 3):\n  X: %+.2f  Y: %+.2f  Rot: %+.2f°\n  %s",
					bdx, bdy, brot, blueLine,
					rdx, rdy, rrot, redLine)
				dialog.ShowInformation("Alignment Data", msg, win)
			})
		}()
	}

	crossChannelClean := func() {
		if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
			dialog.ShowInformation("Missing Channels", "Load all three channels before cleaning.", win)
			return
		}

		savedStates := captureViewportStates()
		if largeMode && largeStore != nil {
			expected := make([]composeArtifactDescriptor, 3)
			for i := 0; i < 3; i++ {
				d, ok := largeArtifacts[i]
				if !ok || d.Path == "" {
					dialog.ShowInformation("Missing Channels", "Load all three channels before cleaning.", win)
					return
				}
				expected[i] = d
			}
			cleanCtx, cancelClean := context.WithCancel(context.Background())
			cancelButton := widget.NewButton("Cancel", cancelClean)
			progressLabel := widget.NewLabel("Preparing cleaner…")
			progressBar := widget.NewProgressBar()
			progressDialog := dialog.NewCustom("Cleaning", "", container.NewBorder(nil, cancelButton, nil, nil, container.NewVBox(progressLabel, progressBar)), win)
			progressDialog.Show()
			go func() {
				defer cancelClean()
				lastProgressStage, lastProgressPercent := "", -1
				updateProgress := func(stage string, completed, total int) {
					if total <= 0 {
						return
					}
					percent := min(100, completed*100/total)
					if stage == lastProgressStage && percent == lastProgressPercent {
						return
					}
					lastProgressStage, lastProgressPercent = stage, percent
					fyne.Do(func() {
						progressLabel.SetText(fmt.Sprintf("%s — %d%%", stage, percent))
						progressBar.SetValue(float64(completed) / float64(total))
					})
				}
				widths := []int{expected[0].Width, expected[1].Width, expected[2].Width}
				heights := []int{expected[0].Height, expected[1].Height, expected[2].Height}
				sharedW, sharedH := widths[0], heights[0]
				for i := 1; i < 3; i++ {
					if widths[i] < sharedW {
						sharedW = widths[i]
					}
					if heights[i] < sharedH {
						sharedH = heights[i]
					}
				}
				var maskFiles [3]*os.File
				var maskScratchFiles [3]*os.File
				var labelFiles [3]*os.File
				var equivFiles [3]*os.File
				var memberFiles [3]*os.File
				var crLabelFiles [3]*os.File
				var crEquivFiles [3]*os.File
				var crMemberFiles [3]*os.File
				var crStatsFiles [3]*os.File
				var crDecisionFiles [3]*os.File
				var crDecisionValidFiles [3]*os.File
				var crReplacementFiles [3]*os.File
				var crReplacementValidFiles [3]*os.File
				for i := range maskFiles {
					f, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-mask-%d-*.bin", i))
					if e != nil {
						for _, m := range maskFiles {
							if m != nil {
								_ = m.Close()
							}
						}
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					maskFiles[i] = f
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(f)
					sf, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-mask-scratch-%d-*.bin", i))
					if e != nil {
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					maskScratchFiles[i] = sf
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(sf)
					lf, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-label-%d-*.bin", i))
					if e != nil {
						for _, m := range maskFiles {
							if m != nil {
								_ = m.Close()
							}
						}
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					labelFiles[i] = lf
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(lf)
					ef, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-equiv-%d-*.bin", i))
					if e != nil {
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					equivFiles[i] = ef
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(ef)
					mf, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-member-%d-*.bin", i))
					if e != nil {
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					memberFiles[i] = mf
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(mf)
					clf, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-cr-label-%d-*.bin", i))
					if e != nil {
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					crLabelFiles[i] = clf
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(clf)
					cef, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-cr-equiv-%d-*.bin", i))
					if e != nil {
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					crEquivFiles[i] = cef
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(cef)
					cmf, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-cr-member-%d-*.bin", i))
					if e != nil {
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					crMemberFiles[i] = cmf
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(cmf)
					stf, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-cr-stats-%d-*.bin", i))
					if e != nil {
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					crStatsFiles[i] = stf
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(stf)
					df, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-cr-decision-%d-*.bin", i))
					if e != nil {
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					crDecisionFiles[i] = df
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(df)
					dvf, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-cr-decision-valid-%d-*.bin", i))
					if e != nil {
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					crDecisionValidFiles[i] = dvf
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(dvf)
					rf, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-cr-replacement-%d-*.bin", i))
					if e != nil {
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					crReplacementFiles[i] = rf
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(rf)
					rvf, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-cr-replacement-valid-%d-*.bin", i))
					if e != nil {
						fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, win) })
						return
					}
					crReplacementValidFiles[i] = rvf
					defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(rvf)
				}
				var stagedPreviews [3]*image.RGBA
				next, err := largeStore.ReplaceManyIfCurrentArtifactsPrepared(expected, func(outs [3]*fitsio.Float32Artifact) error {
					var readers [3]*fitsio.Float32Artifact
					for i := range readers {
						var e error
						readers[i], e = fitsio.OpenFloat32ArtifactReadOnly(expected[i].Path)
						if e != nil {
							for _, r := range readers {
								if r != nil {
									_ = r.Close()
								}
							}
							return e
						}
						defer readers[i].Close()
					}
					ro := processing.CrossChannelCleanDiskOptions{Widths: widths, Heights: heights, Passes: 2, TileWidth: 256, TileHeight: 256, Halo: 6, Context: cleanCtx, Progress: func(p processing.CrossChannelCleanProgress) {
						updateProgress(p.Stage, p.Completed, p.Total)
					}}
					var scratch [3][2]*fitsio.Float32Artifact
					var scratchPaths [3][2]string
					var scratchActive [3]int
					for i := 0; i < 3; i++ {
						for pass := 0; pass < 2; pass++ {
							sp, e := os.CreateTemp(largeStore.root, fmt.Sprintf("clean-scratch-%d-%d-*.bin", i, pass))
							if e != nil {
								return e
							}
							scratchPaths[i][pass] = sp.Name()
							_ = sp.Close()
							_ = os.Remove(scratchPaths[i][pass])
							scratch[i][pass], e = fitsio.CreateFloat32Artifact(scratchPaths[i][pass], widths[i], heights[i])
							if e != nil {
								return e
							}
							defer func(a *fitsio.Float32Artifact, p string) { _ = a.Close(); _ = os.Remove(p) }(scratch[i][pass], scratchPaths[i][pass])
						}
						idx := i
						ro.CosmicScratch[i] = processing.CosmicRayScratch{
							ReadRow:  func(y int, dst []float32) error { return scratch[idx][scratchActive[idx]].ReadRow(y, dst) },
							WriteRow: func(y int, src []float32) error { return scratch[idx][1-scratchActive[idx]].WriteRow(y, src) },
							SeedRow:  func(y int, src []float32) error { return scratch[idx][scratchActive[idx]].WriteRow(y, src) },
							Swap:     func() error { scratchActive[idx] = 1 - scratchActive[idx]; return nil },
						}
					}
					for i := 0; i < 3; i++ {
						idx := i
						ro.MaskReadRow[i] = func(y int, dst []byte) error {
							buf := dst[:sharedW]
							n, e := maskFiles[idx].ReadAt(buf, int64(y*sharedW))
							if n < len(buf) {
								clear(buf[n:])
							}
							if e == io.EOF {
								return nil
							}
							return e
						}
						ro.MaskWriteRow[i] = func(y int, src []byte) error {
							_, e := maskFiles[idx].WriteAt(src[:sharedW], int64(y*sharedW))
							return e
						}
						ro.MaskScratchReadRow[i] = func(y int, dst []byte) error {
							buf := dst[:sharedW]
							n, e := maskScratchFiles[idx].ReadAt(buf, int64(y*sharedW))
							if n < len(buf) {
								clear(buf[n:])
							}
							if e == io.EOF {
								return nil
							}
							return e
						}
						ro.MaskScratchWriteRow[i] = func(y int, src []byte) error {
							_, e := maskScratchFiles[idx].WriteAt(src[:sharedW], int64(y*sharedW))
							return e
						}
						ro.ComponentScratch[i] = processing.CrossChannelComponentScratch{
							ReadEquivalence: func(label uint32) (uint32, bool, error) {
								var b [4]byte
								n, e := equivFiles[idx].ReadAt(b[:], int64(label)*4)
								if e != nil && e != io.EOF {
									return 0, false, e
								}
								if n != 4 {
									return 0, false, nil
								}
								v := binary.LittleEndian.Uint32(b[:])
								return v, v != 0 && v != label, nil
							},
							WriteEquivalence: func(label, canonical uint32) error {
								var b [4]byte
								binary.LittleEndian.PutUint32(b[:], canonical)
								_, e := equivFiles[idx].WriteAt(b[:], int64(label)*4)
								return e
							},
							LabelReadRow: func(y int, dst []uint32) error {
								buf := make([]byte, sharedW*4)
								n, e := labelFiles[idx].ReadAt(buf, int64(y*sharedW*4))
								if e != nil && e != io.EOF {
									return e
								}
								if n < len(buf) {
									clear(buf[n:])
								}
								for j := 0; j < sharedW && j < len(dst); j++ {
									dst[j] = binary.LittleEndian.Uint32(buf[j*4:])
								}
								return nil
							},
							LabelWriteRow: func(y int, src []uint32) error {
								buf := make([]byte, sharedW*4)
								for j := 0; j < sharedW && j < len(src); j++ {
									binary.LittleEndian.PutUint32(buf[j*4:], src[j])
								}
								_, e := labelFiles[idx].WriteAt(buf, int64(y*sharedW*4))
								return e
							},
							AppendMember: func(label uint32, pixel int) error {
								var rec [8]byte
								binary.LittleEndian.PutUint32(rec[0:4], label)
								binary.LittleEndian.PutUint32(rec[4:8], uint32(pixel))
								_, e := memberFiles[idx].Write(rec[:])
								return e
							},
						}
						ro.CRCandidateScratch[i] = processing.GlobalCRCandidateScratch{
							ReadEquivalence: func(label uint32) (uint32, bool, error) {
								var b [4]byte
								n, e := crEquivFiles[idx].ReadAt(b[:], int64(label)*4)
								if e != nil && e != io.EOF {
									return 0, false, e
								}
								if n != 4 {
									return 0, false, nil
								}
								v := binary.LittleEndian.Uint32(b[:])
								return v, v != 0 && v != label, nil
							},
							WriteEquivalence: func(label, canonical uint32) error {
								var b [4]byte
								binary.LittleEndian.PutUint32(b[:], canonical)
								_, e := crEquivFiles[idx].WriteAt(b[:], int64(label)*4)
								return e
							},
							LabelReadRow: func(y int, dst []uint32) error {
								buf := make([]byte, sharedW*4)
								n, e := crLabelFiles[idx].ReadAt(buf, int64(y*sharedW*4))
								if e != nil && e != io.EOF {
									return e
								}
								if n < len(buf) {
									clear(buf[n:])
								}
								for j := 0; j < sharedW && j < len(dst); j++ {
									dst[j] = binary.LittleEndian.Uint32(buf[j*4:])
								}
								return nil
							},
							LabelWriteRow: func(y int, src []uint32) error {
								buf := make([]byte, sharedW*4)
								for j := 0; j < sharedW && j < len(src); j++ {
									binary.LittleEndian.PutUint32(buf[j*4:], src[j])
								}
								_, e := crLabelFiles[idx].WriteAt(buf, int64(y*sharedW*4))
								return e
							},
							AppendMember: func(label uint32, pixel int) error {
								var rec [8]byte
								binary.LittleEndian.PutUint32(rec[0:4], label)
								binary.LittleEndian.PutUint32(rec[4:8], uint32(pixel))
								_, e := crMemberFiles[idx].Write(rec[:])
								return e
							},
							// Persist compact geometry keyed by canonical label. This keeps
							// replacement classification O(1) per component instead of
							// rescanning the entire member stream for every label.
							AccumulateComponent: func(label uint32, pixel int) error {
								const recSize = int64(40)
								var rec [5]int64
								buf := make([]byte, recSize)
								n, e := crStatsFiles[idx].ReadAt(buf, int64(label)*recSize)
								if e != nil && e != io.EOF {
									return e
								}
								if n == int(recSize) {
									for j := range rec {
										rec[j] = int64(binary.LittleEndian.Uint64(buf[j*8:]))
									}
								} else {
									rec[1], rec[3] = int64(sharedW), int64(sharedH)
									rec[2], rec[4] = -1, -1
								}
								x, y := pixel%sharedW, pixel/sharedW
								rec[0]++
								if int64(x) < rec[1] {
									rec[1] = int64(x)
								}
								if int64(x) > rec[2] {
									rec[2] = int64(x)
								}
								if int64(y) < rec[3] {
									rec[3] = int64(y)
								}
								if int64(y) > rec[4] {
									rec[4] = int64(y)
								}
								for j := range rec {
									binary.LittleEndian.PutUint64(buf[j*8:], uint64(rec[j]))
								}
								_, e = crStatsFiles[idx].WriteAt(buf, int64(label)*recSize)
								return e
							},
							ReadComponentStats: func(label uint32) (int, int, int, int, int, bool, error) {
								const recSize = int64(40)
								var buf [40]byte
								n, e := crStatsFiles[idx].ReadAt(buf[:], int64(label)*recSize)
								if e != nil && e != io.EOF {
									return 0, 0, 0, 0, 0, false, e
								}
								if n != len(buf) {
									return 0, 0, 0, 0, 0, false, nil
								}
								return int(int64(binary.LittleEndian.Uint64(buf[0:8]))), int(int64(binary.LittleEndian.Uint64(buf[8:16]))), int(int64(binary.LittleEndian.Uint64(buf[16:24]))), int(int64(binary.LittleEndian.Uint64(buf[24:32]))), int(int64(binary.LittleEndian.Uint64(buf[32:40]))), true, nil
							},
						}
						// Persist one replacement value per canonical candidate component.
						// A missing record is represented by a short ReadAt, so zero is a
						// valid replacement value as well.
						decisionFile := crDecisionFiles[idx]
						decisionValidFile := crDecisionValidFiles[idx]
						ro.CRCandidateScratch[i].ReadComponentValue = func(label uint32) (float32, bool, error) {
							var buf [4]byte
							var valid [1]byte
							vn, ve := decisionValidFile.ReadAt(valid[:], int64(label))
							if ve != nil && ve != io.EOF {
								return 0, false, ve
							}
							if vn != 1 || valid[0] == 0 {
								return 0, false, nil
							}
							n, e := decisionFile.ReadAt(buf[:], int64(label)*4)
							if e != nil && e != io.EOF {
								return 0, false, e
							}
							if n != len(buf) {
								return 0, false, nil
							}
							return math.Float32frombits(binary.LittleEndian.Uint32(buf[:])), true, nil
						}
						ro.CRCandidateScratch[i].DecideComponent = func(label uint32, firstMember int) (float32, error) {
							x, y := firstMember%sharedW, firstMember/sharedW
							var vals [121]float64
							n := 0
							window := make([]float32, 11)
							for k := 0; k < 11; k++ {
								yy := y + k - 5
								if yy < 0 || yy >= sharedH {
									continue
								}
								sx0, sx1 := x-5, x+6
								if sx0 < 0 {
									sx0 = 0
								}
								if sx1 > sharedW {
									sx1 = sharedW
								}
								if e := readers[idx].ReadRange(yy, sx0, sx1, window[:sx1-sx0]); e != nil {
									return 0, e
								}
								for xx := sx0; xx < sx1; xx++ {
									v := float64(window[xx-sx0])
									if !math.IsNaN(v) {
										vals[n] = v
										n++
									}
								}
							}
							if n == 0 {
								return 0, nil
							}
							sort.Float64s(vals[:n])
							replacement := float32(vals[n/2])
							var buf [4]byte
							binary.LittleEndian.PutUint32(buf[:], math.Float32bits(replacement))
							if _, e := decisionFile.WriteAt(buf[:], int64(label)*4); e != nil {
								return 0, e
							}
							_, e := decisionValidFile.WriteAt([]byte{1}, int64(label))
							return replacement, e
						}
						replacementFile := crReplacementFiles[idx]
						replacementValidFile := crReplacementValidFiles[idx]
						ro.CRCandidateScratch[i].WriteMemberValue = func(pixel int, replacement float32) error {
							if pixel < 0 {
								return fmt.Errorf("negative replacement member index %d", pixel)
							}
							var buf [4]byte
							binary.LittleEndian.PutUint32(buf[:], math.Float32bits(replacement))
							if _, err := replacementFile.WriteAt(buf[:], int64(pixel)*4); err != nil {
								return err
							}
							_, err := replacementValidFile.WriteAt([]byte{1}, int64(pixel))
							return err
						}
					}
					for i := 0; i < 3; i++ {
						idx := i
						ro.ReadRow[i] = func(y int, row []float32) error { return readers[idx].ReadRow(y, row) }
						ro.WriteRow[i] = func(y int, row []float32) error { return outs[idx].WriteRow(y, row) }
					}
					if e := processing.CrossChannelCleanDisk(ro); e != nil {
						return e
					}
					// Cosmic-ray scratch output is now final. Replay deferred component
					// replacements afterward so they cannot be overwritten by the final
					// scratch-to-output copy above.
					for c := 0; c < 3; c++ {
						rowsPerBatch := crossChannelCleanReplayRowsPerBatch(sharedW, sharedH)
						rows := make([]float32, rowsPerBatch*widths[c])
						replacements := make([]byte, rowsPerBatch*sharedW*4)
						valid := make([]byte, rowsPerBatch*sharedW)
						for y := 0; y < sharedH; y += rowsPerBatch {
							if e := cleanCtx.Err(); e != nil {
								return e
							}
							rowCount := min(rowsPerBatch, sharedH-y)
							batchRows := rows[:rowCount*widths[c]]
							batchReplacements := replacements[:rowCount*sharedW*4]
							batchValid := valid[:rowCount*sharedW]
							if e := outs[c].ReadRows(y, rowCount, batchRows); e != nil {
								return e
							}
							validN, e := crReplacementValidFiles[c].ReadAt(batchValid, int64(y*sharedW))
							if e != nil && e != io.EOF {
								return e
							}
							if validN < len(batchValid) {
								clear(batchValid[validN:])
							}
							replacementN, e := crReplacementFiles[c].ReadAt(batchReplacements, int64(y*sharedW)*4)
							if e != nil && e != io.EOF {
								return e
							}
							if replacementN < len(batchReplacements) {
								clear(batchReplacements[replacementN:])
								// Match the former per-pixel behavior: a valid marker
								// without all four replacement bytes must not turn into
								// a zero-valued replacement.
								if complete := replacementN / 4; complete < len(batchValid) {
									clear(batchValid[complete:])
								}
							}
							dirty := false
							for row := 0; row < rowCount; row++ {
								rowStart := row * widths[c]
								sharedStart := row * sharedW
								if applyCrossChannelReplacementRow(batchRows[rowStart:rowStart+sharedW], batchReplacements[sharedStart*4:], batchValid[sharedStart:]) {
									dirty = true
								}
							}
							if dirty {
								if e := outs[c].WriteRows(y, rowCount, batchRows); e != nil {
									return e
								}
							}
							updateProgress(fmt.Sprintf("Applying replacements (channel %d)", c+1), y+rowCount, sharedH)
						}
					}
					return nil
				}, func(staged []composeArtifactDescriptor) error {
					for i := range staged {
						preview, _, _, e := composeLargeStretchedPreview(staged[i].Path, imgs[i])
						if e != nil {
							return e
						}
						stagedPreviews[i] = preview
					}
					return nil
				})
				if err == nil {
					fyne.Do(func() {
						for i := range next {
							largeArtifacts[i] = next[i]
							largePreviews[i] = stagedPreviews[i]
							imgs[i].HDU.Data.Pixels = nil
							clearComposeOrigPixels(&origPixels, i)
						}
						invalidateCalibration()
						progressDialog.Hide()
						refresh()
						restoreViewportStates(savedStates)
						dialog.ShowInformation("Complete", "Star masks generated and cosmic rays eradicated in the shared region.", win)
					})
				} else {
					fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(err, win) })
				}
				return
				/* unreachable legacy duplicate implementation
				writes := make([]func(*fitsio.Float32Artifact) error, 3)
				for i := 0; i < 3; i++ {
					idx := i
					writes[i] = func(out *fitsio.Float32Artifact) error {
						if err := cleanCtx.Err(); err != nil {
							return err
						}
						src := make([]*fitsio.Float32Artifact, 3)
						for c := range src {
							var err error
							src[c], err = fitsio.OpenFloat32ArtifactReadOnly(expected[c].Path)
							if err != nil {
								for _, a := range src {
									if a != nil {
										_ = a.Close()
									}
								}
								return err
							}
							defer src[c].Close()
						}
						sharedW, sharedH := widths[0], heights[0]
						for c := 1; c < 3; c++ {
							if widths[c] < sharedW {
								sharedW = widths[c]
							}
							if heights[c] < sharedH {
								sharedH = heights[c]
							}
						}
						row := make([]float32, widths[idx])
						for y := 0; y < heights[idx]; y++ {
							if err := cleanCtx.Err(); err != nil {
								return err
							}
							if err := src[idx].ReadRow(y, row); err != nil {
								return err
							}
							if err := out.WriteRow(y, row); err != nil {
								return err
							}
						}
						sigmas := make([]float64, 3)
						sample := make([]float32, 0, 65536)
						maxWidth := widths[0]
						for c := 1; c < 3; c++ {
							if widths[c] > maxWidth {
								maxWidth = widths[c]
							}
						}
						sampleRow := make([]float32, maxWidth)
						for c := 0; c < 3; c++ {
							step := heights[c] / 64
							if step < 1 {
								step = 1
							}
							for y := 0; y < heights[c] && len(sample) < cap(sample); y += step {
								if err := src[c].ReadRow(y, sampleRow[:widths[c]]); err != nil {
									return err
								}
								remain := cap(sample) - len(sample)
								if remain > widths[c] {
									remain = widths[c]
								}
								sample = append(sample, sampleRow[:remain]...)
							}
							_, sigmas[c] = processing.EstimateBackground(sample)
							sample = sample[:0]
						}
						for y0 := 0; y0 < sharedH; y0 += 256 {
							for x0 := 0; x0 < sharedW; x0 += 256 {
								if err := cleanCtx.Err(); err != nil {
									return err
								}
								x1, y1 := x0+256, y0+256
								if x1 > sharedW {
									x1 = sharedW
								}
								if y1 > sharedH {
									y1 = sharedH
								}
								sx0, sy0 := x0-6, y0-6
								if sx0 < 0 {
									sx0 = 0
								}
								if sy0 < 0 {
									sy0 = 0
								}
								sx1, sy1 := x1+6, y1+6
								if sx1 > sharedW {
									sx1 = sharedW
								}
								if sy1 > sharedH {
									sy1 = sharedH
								}
								tw, th := sx1-sx0, sy1-sy0
								tile := make([][]float32, 3)
								for c := 0; c < 3; c++ {
									tile[c] = make([]float32, tw*th)
									rr := make([]float32, widths[c])
									for yy := sy0; yy < sy1; yy++ {
										if err := src[c].ReadRow(yy, rr); err != nil {
											return err
										}
										copy(tile[c][(yy-sy0)*tw:], rr[sx0:sx1])
									}
								}
								cleaned, err := processing.CrossChannelCleanTile(tile, tw, th, sigmas, 2)
								if err != nil {
									return err
								}
								for y := y0; y < y1; y++ {
									if err := out.ReadRow(y, row); err != nil {
										return err
									}
									copy(row[x0:x1], cleaned[idx][(y-sy0)*tw+x0-sx0:(y-sy0)*tw+x1-sx0])
									if err := out.WriteRow(y, row); err != nil {
										return err
									}
								}
							}
						}
						return nil
					}
				}
				previews := make([]*image.RGBA, 3)
				next, err := largeStore.ReplaceManyIfCurrentPrepared(expected, writes, func(staged []composeArtifactDescriptor) error {
					for i := range staged {
						if cancelErr := cleanCtx.Err(); cancelErr != nil {
							return cancelErr
						}
						var previewErr error
						previews[i], _, _, previewErr = composeLargeStretchedPreview(staged[i].Path, imgs[i])
						if previewErr != nil {
							return previewErr
						}
					}
					return nil
				})
				*/
			}()
			return
		}

		sharedWidth := 0
		sharedHeight := 0
		for i := 0; i < 3; i++ {
			data := imgs[i].HDU.Data
			if data.Width <= 0 || data.Height <= 0 {
				dialog.ShowInformation(
					"Invalid Channel Data",
					fmt.Sprintf("Channel %d has invalid dimensions %dx%d.", i+1, data.Width, data.Height),
					win,
				)
				return
			}
			usableHeight := len(data.Pixels) / data.Width
			if usableHeight <= 0 {
				dialog.ShowInformation(
					"Invalid Channel Data",
					fmt.Sprintf("Channel %d does not have enough pixels for its declared width %d.", i+1, data.Width),
					win,
				)
				return
			}
			if usableHeight > data.Height {
				usableHeight = data.Height
			}
			if i == 0 || data.Width < sharedWidth {
				sharedWidth = data.Width
			}
			if i == 0 || usableHeight < sharedHeight {
				sharedHeight = usableHeight
			}
		}
		if sharedWidth <= 0 || sharedHeight <= 0 {
			dialog.ShowInformation("Invalid Channel Data", "Could not determine a shared image region to clean.", win)
			return
		}

		progressDialog := dialog.NewCustom("Cleaning", "Building star mask and removing artifacts...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			cropTopLeft := func(data fitsio.ImageData, width, height int) []float32 {
				cropped := make([]float32, width*height)
				for y := 0; y < height; y++ {
					srcStart := y * data.Width
					dstStart := y * width
					copy(cropped[dstStart:dstStart+width], data.Pixels[srcStart:srcStart+width])
				}
				return cropped
			}
			pasteTopLeft := func(dst []float32, dstWidth int, src []float32, width, height int) {
				for y := 0; y < height; y++ {
					dstStart := y * dstWidth
					srcStart := y * width
					copy(dst[dstStart:dstStart+width], src[srcStart:srcStart+width])
				}
			}

			channels := make([][]float32, 0, 3)
			sigmas := make([]float64, 0, 3)
			for i := 0; i < 3; i++ {
				cropped := cropTopLeft(imgs[i].HDU.Data, sharedWidth, sharedHeight)
				channels = append(channels, cropped)
				_, sig := processing.EstimateBackground(cropped)
				sigmas = append(sigmas, sig)
			}

			starMasks := processing.BuildLayerStarMasks(channels, sharedWidth, sharedHeight, sigmas)
			passes := 2

			cleaned := make([][]float32, 3)
			var wg sync.WaitGroup
			for i := 0; i < 3; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					cleaned[idx] = processing.RemoveCosmicRays(channels[idx], sharedWidth, sharedHeight, sigmas[idx], passes, starMasks[idx])
				}(i)
			}
			wg.Wait()

			for i := 0; i < 3; i++ {
				out := make([]float32, len(imgs[i].HDU.Data.Pixels))
				copy(out, imgs[i].HDU.Data.Pixels)
				pasteTopLeft(out, imgs[i].HDU.Data.Width, cleaned[i], sharedWidth, sharedHeight)
				imgs[i].HDU.Data.Pixels = out
				clearComposeOrigPixels(&origPixels, i)
			}
			invalidateCalibration()

			fyne.Do(func() {
				win.Canvas().Refresh(win.Content())
				progressDialog.Hide()
				refresh()
				restoreViewportStates(savedStates)
				dialog.ShowInformation("Complete", fmt.Sprintf("Star masks generated and cosmic rays eradicated in the shared %dx%d region.", sharedWidth, sharedHeight), win)
			})
		}()
	}

	exportRGB := func() {
		buf, w, h, _, rendered, err := composeRGB(context.Background(), cloneCalibration())
		if buf == nil {
			dialog.ShowInformation("Missing", "Load three FITS first", win)
			return
		}
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		var largePaths [3]string
		if largeMode && largeStore != nil {
			comp, ok := largeStore.Composite()
			if !ok {
				dialog.ShowError(fmt.Errorf("missing disk composite"), win)
				return
			}
			for i := range comp.Planes {
				largePaths[i] = comp.Planes[i].Path
			}
			w, h = comp.Width, comp.Height
		}
		finalBuf := processing.ApplyRGBLevels(buf, levels)
		// Use the same immutable render result for high-bit-depth output as for
		// the preview (including the existing overlay blend).
		// Reuse the same immutable manual-offset-applied snapshot used by preview.
		if rendered == nil {
			dialog.ShowError(fmt.Errorf("missing render result"), win)
			return
		}
		finalBuf = append([]byte(nil), rendered.Preview...)
		processing.ApplyRGBLevels(finalBuf, levels)
		w, h = rendered.Width, rendered.Height
		rF, gF, bF := rendered.R, rendered.G, rendered.B
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()
			format := detectExportFormat(path)
			showExportOptionsDialog(format, win, func(opts export.Options) {
				if largeMode {
					if format == export.PNG && opts.BitDepth != 16 {
						opts.BitDepth = 8
					}
					if err := export.FromFloat32ArtifactsWithLevels(context.Background(), path, largePaths, w, h, format, opts, levels); err != nil {
						dialog.ShowError(err, win)
						return
					}
					debuglog.Log(fmt.Sprintf("exportRGB: wrote disk composite %s", path))
					return
				}
				if format == export.PNG && opts.BitDepth == 16 && rF != nil {
					rA, gA, bA := processing.ApplyRGBLevelsFloat32(rF, gF, bF, levels)
					if err := export.FromFloat32Channels(path, rA, gA, bA, w, h, format, opts); err != nil {
						dialog.ShowError(err, win)
						return
					}
					debuglog.Log(fmt.Sprintf("exportRGB: wrote 16-bit composite %s", path))
					return
				}
				if err := export.FromRGBABytes(path, finalBuf, w, h, format, opts); err != nil {
					dialog.ShowError(err, win)
					return
				}
				debuglog.Log(fmt.Sprintf("exportRGB: wrote composite %s", path))
			})
		}, win)
		save.SetFileName("composite.png")
		save.Show()
	}

	measureLabel := widget.NewLabel("Measure: --")
	measureLabel.TextStyle = fyne.TextStyle{Monospace: true}

	updateMeasurement = func() {
		viewports[3].setMeasurementOverlay(measureStart, measureEnd, false)
		switch {
		case measureStart != nil && measureEnd != nil:
			m := measurePoints(*measureStart, *measureEnd)
			measureLabel.SetText(fmt.Sprintf("A(%d,%d) B(%d,%d)\ndx=%+d dy=%+d d=%.2f px", m.Start.X, m.Start.Y, m.End.X, m.End.Y, m.DX, m.DY, m.Distance))
		case measureStart != nil:
			measureLabel.SetText(fmt.Sprintf("Measure: A=(%d,%d) — click B", measureStart.X, measureStart.Y))
		default:
			measureLabel.SetText("Measure: --")
		}
	}

	measureCheck = NewToggle(func(v bool) {
		measureEnabled = v
		if !v {
			measureStart = nil
			measureEnd = nil
			updateMeasurement()
		}
	})

	viewports[3].overlay.onTapped = func(pos fyne.Position) {
		if !measureEnabled {
			return
		}
		point, ok := viewports[3].imagePointAtPosition(pos, false)
		if !ok {
			return
		}
		if measureStart == nil || measureEnd != nil {
			measureStart = &imagePoint{X: point.X, Y: point.Y}
			measureEnd = nil
		} else {
			measureEnd = &imagePoint{X: point.X, Y: point.Y}
		}
		updateMeasurement()
	}

	viewports[3].onViewChanged = func() {
		updateMeasurement()
	}

	blinkStatus := widget.NewLabel("")
	blinkStatus.Wrapping = fyne.TextWrapWord
	blinkFrame := 0

	applyBlinkFrame := func() {
		if !blinkCheck.Checked {
			return
		}
		blinkMu.Lock()
		frames := append([]composeBlinkFrame(nil), blinkPrepared...)
		blinkMu.Unlock()
		if len(frames) < 2 {
			return
		}
		dst := viewports[3]
		if dst == nil {
			if updateBlinkStatus != nil {
				updateBlinkStatus()
			}
			return
		}
		frame := frames[blinkFrame%len(frames)].Preview
		if frame.Image == nil || frame.OrigW == 0 || frame.OrigH == 0 {
			return
		}
		dst.image.Image = frame.Image
		dst.origW, dst.origH = frame.OrigW, frame.OrigH
		dst.bins = frame.Bins
		dst.histMax = frame.HistMax
		if dst.StatsLabel != nil {
			dst.StatsLabel.SetText(fmt.Sprintf("Blink: %s", frames[blinkFrame%len(frames)].Name))
		}
		dst.histogram.Refresh()
		if dst.zoomLabel.Selected == "fit" {
			dst.zoom = dst.fitZoom()
		}
		dst.applyZoom()
		dst.image.Refresh()
	}

	updateBlinkStatus = func() {
		sources := composeBlinkSources()
		selection := blinkChannels
		if selection == nil {
			selection = resolveComposeBlinkSelection(sources, nil, false, 0)
		}
		selection = filterComposeBlinkSelection(selection, sources)
		if !blinkCheck.Checked {
			blinkStatus.SetText("Blink: off")
			return
		}
		if len(selection) < 2 {
			blinkStatus.SetText("Blink: choose at least two channels")
			return
		}
		names := make([]string, 0, len(selection))
		for _, index := range selection {
			for _, source := range sources {
				if source.ProjectIndex == index {
					names = append(names, source.Name)
					break
				}
			}
		}
		blinkStatus.SetText(fmt.Sprintf("Blink: %s", strings.Join(names, " <-> ")))
	}

	stopBlink = func() {
		blinkMu.Lock()
		blinkSeq++
		blinkMu.Unlock()
	}
	refreshBlinkFrame = func() {
		if !blinkCheck.Checked {
			return
		}
		blinkFrame = 0
		updateBlinkStatus()
		applyBlinkFrame()
	}
	startBlink = func() {
		stopBlink()
		if !blinkCheck.Checked {
			updateBlinkStatus()
			return
		}
		if blinkChannels == nil {
			blinkChannels = resolveComposeBlinkSelection(composeBlinkSources(), nil, false, 0)
		}
		if len(filterComposeBlinkSelection(blinkChannels, composeBlinkSources())) < 2 {
			stopBlink()
			updateBlinkStatus()
			return
		}
		// Rebuild the per-channel previews so the blink reflects the current Manual
		// Offsets (applied at render time by renderImages).
		refresh()
		blinkMu.Lock()
		blinkSeq++
		seq := blinkSeq
		blinkMu.Unlock()
		blinkFrame = 0
		updateBlinkStatus()
		applyBlinkFrame()
		go func() {
			ticker := time.NewTicker(700 * time.Millisecond)
			defer ticker.Stop()
			for range ticker.C {
				blinkMu.Lock()
				currentSeq := blinkSeq
				blinkMu.Unlock()
				if currentSeq != seq {
					return
				}
				fyne.Do(func() {
					blinkMu.Lock()
					currentSeq := blinkSeq
					blinkMu.Unlock()
					if currentSeq != seq || !blinkCheck.Checked {
						return
					}
					blinkFrame++
					applyBlinkFrame()
				})
			}
		}()
	}

	blinkCheck.OnChanged = func(v bool) {
		if v {
			startBlink()
			return
		}
		stopBlink()
		updateBlinkStatus()
		refresh()
	}
	chooseBlinkChannels = func() {
		sources := composeBlinkSources()
		if len(sources) < 2 {
			dialog.ShowInformation("Blink", "Load at least two channels first.", win)
			return
		}
		current := blinkChannels
		if current == nil {
			current = resolveComposeBlinkSelection(sources, nil, false, 0)
		}
		selected := make(map[int]bool, len(current))
		for _, index := range current {
			selected[index] = true
		}
		checks := make([]*widget.Check, len(sources))
		content := container.NewVBox()
		for i, source := range sources {
			check := widget.NewCheck(source.Name, nil)
			check.SetChecked(selected[source.ProjectIndex])
			checks[i] = check
			content.Add(check)
		}
		d := dialog.NewCustomConfirm("Choose Blink Channels", "Apply", "Cancel", container.NewVScroll(content), func(ok bool) {
			if !ok {
				return
			}
			selection := make([]int, 0, len(sources))
			for i, check := range checks {
				if check.Checked {
					selection = append(selection, sources[i].ProjectIndex)
				}
			}
			if len(selection) < 2 {
				dialog.ShowInformation("Blink", "Select at least two channels.", win)
				return
			}
			blinkChannels = selection
			updateBlinkStatus()
			if blinkCheck.Checked {
				startBlink()
			} else {
				refresh()
			}
		}, win)
		d.Show()
	}
	updateBlinkStatus()

	//alignBtn := widget.NewButton("1. Align to Channel 2 (Green)", alignChannels)
	//crossCleanBtn := widget.NewButton("2. Cross-Channel Clean", crossChannelClean)

	saveProjectItem := fyne.NewMenuItem("Save Compose Project", saveProject)
	loadProjectItem := fyne.NewMenuItem("Load Compose Project", loadProject)
	loadFilterSetItem := fyne.NewMenuItem("Load Filter Set...", func() {
		showComposeMagicFolderPicker(app, win, func(preset string, rows []composeMagicRow) error {
			if err := validateComposeMagicCapacity(rows, len(overlayLayers)); err != nil {
				return err
			}

			ctx, cancel := context.WithCancel(context.Background())
			var progressDialog *dialog.CustomDialog
			finished := false
			cancelButton := widget.NewButton("Cancel", func() {
				cancel()
				if progressDialog != nil {
					progressDialog.Hide()
				}
			})
			progressDialog = dialog.NewCustomWithoutButtons(
				"Loading Filter Set",
				container.NewVBox(
					widget.NewLabel("Loading files and applying Magic + Auto MTF..."),
					widget.NewProgressBarInfinite(),
					cancelButton,
				),
				win,
			)
			progressDialog.SetOnClosed(func() {
				if !finished {
					cancel()
				}
			})
			progressDialog.Show()
			spec := composeMagicSpec{Preset: preset, Rows: append([]composeMagicRow(nil), rows...)}
			go func() {
				if largeMode {
					// Stage and process one source at a time. All descriptors remain
					// private until every row succeeds, so cancellation/failure leaves
					// the live project untouched.
					if err := validateComposeMagicPlan(spec.Rows); err != nil {
						fyne.Do(func() { finished = true; progressDialog.Hide(); dialog.ShowError(err, win) })
						return
					}
					magicPresetValue := processing.ParseMagicPreset(spec.Preset)
					type staged struct {
						row      composeMagicRow
						img      *models.LoadedImage
						preview  *image.RGBA
						artifact composeArtifactDescriptor
					}
					ordered := append([]composeMagicRow(nil), spec.Rows...)
					sort.SliceStable(ordered, func(i, j int) bool {
						if ordered[i].File.FilterNumber != ordered[j].File.FilterNumber {
							return ordered[i].File.FilterNumber < ordered[j].File.FilterNumber
						}
						return strings.ToLower(ordered[i].File.Name) < strings.ToLower(ordered[j].File.Name)
					})
					stagedRows := make([]staged, 0, len(ordered))
					cleanup := func() {
						for _, x := range stagedRows {
							cleanupLargeArtifactIfCurrent(x.artifact)
						}
					}
					for i, row := range ordered {
						if ctx.Err() != nil {
							cleanup()
							return
						}
						slot := fmt.Sprintf("filter-stage-%d-%d", time.Now().UnixNano(), i)
						img, preview, artifact, loadErr := loadLargeComposeImage(row.File.Path, largeStore, slot)
						if loadErr != nil {
							cleanup()
							fyne.Do(func() {
								finished = true
								progressDialog.Hide()
								dialog.ShowError(fmt.Errorf("load %s: %w", composeMagicFileLabel(row.File), loadErr), win)
							})
							return
						}
						lease, leaseErr := fitsio.MaterializeFloat32ArtifactLease(artifact.Path)
						if leaseErr != nil {
							cleanupLargeArtifactIfCurrent(artifact)
							cleanup()
							fyne.Do(func() { finished = true; progressDialog.Hide(); dialog.ShowError(leaseErr, win) })
							return
						}
						clone := *img
						clone.HDU.Data.Pixels = lease.Pixels
						magicResult := processing.ApplyMagicLevels(&clone, magicPresetValue)
						if ctx.Err() != nil {
							lease.Release()
							cleanupLargeArtifactIfCurrent(artifact)
							cleanup()
							return
						}
						processing.AutoMTFMidtone(&clone)
						lease.Release()
						if ctx.Err() != nil {
							cleanupLargeArtifactIfCurrent(artifact)
							cleanup()
							return
						}
						if magicResult.ValidPixels == 0 {
							cleanupLargeArtifactIfCurrent(artifact)
							cleanup()
							fyne.Do(func() {
								finished = true
								progressDialog.Hide()
								dialog.ShowError(fmt.Errorf("process %s: Magic found no valid image samples", composeMagicFileLabel(row.File)), win)
							})
							return
						}
						preview, _, _, loadErr = composeLargeStretchedPreview(artifact.Path, &clone)
						if loadErr != nil {
							cleanupLargeArtifactIfCurrent(artifact)
							cleanup()
							fyne.Do(func() { finished = true; progressDialog.Hide(); dialog.ShowError(loadErr, win) })
							return
						}
						clone.HDU.Data.Pixels = nil
						stagedRows = append(stagedRows, staged{row: row, img: &clone, preview: preview, artifact: artifact})
					}
					if !composeLargeInstallAllowed(ctx) {
						cleanup()
						return
					}
					fyne.Do(func() {
						if !composeLargeInstallAllowed(ctx) {
							cleanup()
							return
						}
						defer func() { finished = true; progressDialog.Hide() }()
						existingSlots := make([]int, len(overlayLayers))
						for i, layer := range overlayLayers {
							existingSlots[i] = layer.idx
						}
						planRows := make([]composeMagicRow, len(stagedRows))
						for i := range stagedRows {
							planRows[i] = stagedRows[i].row
						}
						if installErr := validateComposeMagicCapacity(planRows, len(existingSlots)); installErr != nil {
							cleanup()
							dialog.ShowError(installErr, win)
							return
						}
						base := map[composeMagicAssignment]staged{}
						for _, x := range stagedRows {
							if x.row.Assignment != composeMagicCustom {
								base[x.row.Assignment] = x
							}
						}
						for assignment, index := range map[composeMagicAssignment]int{composeMagicBlue: 0, composeMagicGreen: 1, composeMagicRed: 2} {
							x := base[assignment]
							if old := largeArtifacts[index]; old.Slot != "" {
								_, _ = largeStore.RemoveSlotIfCurrent(old)
							}
							imgs[index] = x.img
							largeArtifacts[index] = x.artifact
							largePreviews[index] = x.preview
							clearComposeOrigPixels(&origPixels, index)
							applyChannelState(index, channelStateFromImage(x.img), imgs, viewports, controlSets)
						}
						for _, x := range stagedRows {
							if x.row.Assignment != composeMagicCustom {
								continue
							}
							settings := defaultOverlayLayerSettings(len(overlayLayers))
							settings.ColorR, settings.ColorG, settings.ColorB = x.row.Color.R, x.row.Color.G, x.row.Color.B
							settings.Open = true
							slot, ok := freeOverlaySlot()
							if !ok {
								cleanup()
								dialog.ShowError(errors.New("no free overlay slot"), win)
								return
							}
							layer := createOverlayLayerAt(slot, settings)
							imgs[layer.idx] = x.img
							largeArtifacts[layer.idx] = x.artifact
							largePreviews[layer.idx] = x.preview
							clearComposeOrigPixels(&origPixels, layer.idx)
							openOverlayLayerWindowWithPreview(layer, nil)
						}
						composeMagicPreset.SetSelected(spec.Preset)
						buildCompositeCheck.SetChecked(true)
						if updateMenus != nil {
							updateMenus()
						}
						refresh()
					})
					return
				}
				batch, prepareErr := prepareComposeMagicBatch(ctx, spec, loadImageFromPath)
				previews := make(map[*models.LoadedImage]*composeOverlayPreviewData)
				if prepareErr == nil {
					for _, channel := range batch.Channels {
						if channel.Row.Assignment != composeMagicCustom {
							continue
						}
						preview, previewErr := buildComposeOverlayPreviewData(ctx, channel.Image)
						if previewErr != nil {
							prepareErr = previewErr
							break
						}
						previews[channel.Image] = preview
					}
				}
				fyne.Do(func() {
					if prepareErr != nil {
						finished = true
						progressDialog.Hide()
						if !errors.Is(prepareErr, context.Canceled) {
							dialog.ShowError(prepareErr, win)
						}
						return
					}
					if ctx.Err() != nil {
						finished = true
						progressDialog.Hide()
						return
					}

					existingSlots := make([]int, len(overlayLayers))
					for i, layer := range overlayLayers {
						existingSlots[i] = layer.idx
					}
					installPlan, installErr := planComposeMagicInstall(batch, imgs, existingSlots)
					if installErr != nil {
						finished = true
						progressDialog.Hide()
						dialog.ShowError(installErr, win)
						return
					}
					if ctx.Err() != nil { // Last boundary before the atomic install and composite enable.
						finished = true
						progressDialog.Hide()
						return
					}
					withSuspendedRefresh(func() {
						for idx, image := range installPlan.Base {
							replaceComposeChannelImage(imgs, idx, image)
							clearComposeOrigPixels(&origPixels, idx)
							applyChannelState(idx, channelStateFromImage(image), imgs, viewports, controlSets)
							closeHeaderWindow(idx)
						}
						for _, custom := range installPlan.Customs {
							channel := custom.Channel
							settings := defaultOverlayLayerSettings(len(overlayLayers))
							settings.ColorR = channel.Row.Color.R
							settings.ColorG = channel.Row.Color.G
							settings.ColorB = channel.Row.Color.B
							settings.Open = true
							layer := createOverlayLayerAt(custom.Slot, settings)
							imgs[layer.idx] = channel.Image
							clearComposeOrigPixels(&origPixels, layer.idx)
							openOverlayLayerWindowWithPreview(layer, previews[channel.Image])
							if layer.control != nil && layer.control.MagicPresetSelect != nil {
								layer.control.MagicPresetSelect.SetSelected(batch.Preset)
							}
						}
						renderMu.Lock()
						for i := 0; i < len(renderCache) && i < 3; i++ {
							renderCache[i] = composeRenderCache{}
						}
						renderMu.Unlock()
						composeMagicPreset.SetSelected(batch.Preset)
						buildCompositeCheck.SetChecked(true)
					})
					finished = true
					progressDialog.Hide()
					if updateMenus != nil {
						updateMenus()
					}
					refresh()
				})
			}()
			return nil
		})
	})
	exportRGBItem := fyne.NewMenuItem("Export Compose RGB", exportRGB)

	viewHeaderItems := []*fyne.MenuItem{
		fyne.NewMenuItem("View FITS Header 1", func() { showHeader(0) }),
		fyne.NewMenuItem("View FITS Header 2", func() { showHeader(1) }),
		fyne.NewMenuItem("View FITS Header 3", func() { showHeader(2) }),
	}
	saveHeaderItems := []*fyne.MenuItem{
		fyne.NewMenuItem("Save FITS Header 1...", func() { saveHeader(0) }),
		fyne.NewMenuItem("Save FITS Header 2...", func() { saveHeader(1) }),
		fyne.NewMenuItem("Save FITS Header 3...", func() { saveHeader(2) }),
	}
	sendToEdit := func() {
		if globalExportToEdit == nil {
			return
		}
		if largeMode {
			if largeStore != nil {
				if d, ok := largeStore.Composite(); ok {
					startLargeEditSnapshot(d, *levels)
					return
				}
			}
			dialog.ShowInformation("Build Composite first", "Build the composite before sending it to Edit.", win)
			return
		}
		img := compositeImageForEdit()
		if img == nil {
			dialog.ShowInformation("Nothing to send", "Compose all three channels first.", win)
			return
		}
		if err := globalExportToEdit(editImageHandoff{memory: img}); err != nil {
			dialog.ShowError(err, win)
		}
	}

	clearChannels := func() {
		dialog.ShowConfirm("Clear Channels", "Free all three channel images from memory?", func(ok bool) {
			if !ok {
				return
			}
			oldSources := composeBlinkSources()
			for i := 0; i < 3; i++ {
				if largeMode && largeStore != nil {
					invalidateLargeSlot(i)
					largeMu.Lock()
					store := largeStore
					d := largeArtifacts[i]
					delete(largeArtifacts, i)
					delete(largePreviews, i)
					imgs[i] = nil
					largeMu.Unlock()
					if d.Slot != "" && store != nil {
						_, _ = store.RemoveSlotIfCurrent(d)
					}
				} else {
					imgs[i] = nil
				}
				clearComposeOrigPixels(&origPixels, i)
				viewports[i].image.Image = blankImg()
			}
			if blinkChannels != nil {
				blinkChannels = remapComposeBlinkSelection(blinkChannels, oldSources, composeBlinkSources())
			}
			viewports[3].image.Image = blankImg()
			refresh()
			if updateMenus != nil {
				updateMenus()
			}
			go func() {
				runtime.GC()
				debug.FreeOSMemory()
			}()
		}, win)
	}

	resetCompose := func() {
		dialog.ShowConfirm("Reset Compose", "Clear all images and reset Build color composite?", func(ok bool) {
			if !ok {
				return
			}
			for _, l := range overlayLayers {
				if l.win != nil {
					l.win.SetCloseIntercept(nil)
					l.win.Close()
				}
			}
			overlayLayers = nil
			blinkChannels = nil
			if stopBlink != nil {
				stopBlink()
			}
			blinkCheck.SetChecked(false)
			for i := range imgs {
				if largeMode && largeStore != nil {
					invalidateLargeSlot(i)
					largeMu.Lock()
					store := largeStore
					d := largeArtifacts[i]
					delete(largeArtifacts, i)
					delete(largePreviews, i)
					imgs[i] = nil
					largeMu.Unlock()
					if d.Slot != "" && store != nil {
						_, _ = store.RemoveSlotIfCurrent(d)
					}
				} else {
					imgs[i] = nil
				}
				clearComposeOrigPixels(&origPixels, i)
			}
			imgs = imgs[:3]
			origPixels = origPixels[:3]
			for i := range viewports {
				viewports[i].image.Image = blankImg()
			}
			buildCompositeCheck.SetChecked(false)
			refresh()
			if updateMenus != nil {
				updateMenus()
			}
			go func() {
				runtime.GC()
				debug.FreeOSMemory()
			}()
		}, win)
	}

	var colorCalibrationWindow fyne.Window
	var openColorCalibration func()
	openColorCalibration = func() {
		if colorCalibrationWindow != nil {
			colorCalibrationWindow.RequestFocus()
			return
		}
		if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
			dialog.ShowInformation("Color Calibration", "Load all three base channels first.", win)
			return
		}
		mode := NewSafeSelect([]string{"Off", "Instrument", "Gaia"}, nil)
		mode.SetSelected(map[models.PhotometricMode]string{models.PhotometricInstrument: "Instrument", models.PhotometricGaia: "Gaia"}[colorCalibration.PhotometricMode])
		if colorCalibration.PhotometricMode == models.PhotometricOff || colorCalibration.PhotometricMode == "" {
			mode.SetSelected("Off")
		}
		gaiaAccess := NewSafeSelect([]string{"Online", "Cache Only"}, nil)
		if colorCalibration.Gaia.AccessMode == "cacheOnly" {
			gaiaAccess.SetSelected("Cache Only")
		} else {
			gaiaAccess.SetSelected("Online")
		}
		gaiaRelease := widget.NewEntry()
		gaiaRelease.SetText(colorCalibration.Gaia.Release)
		if gaiaRelease.Text == "" {
			gaiaRelease.SetText("DR3")
		}
		gaiaEndpoint := widget.NewEntry()
		gaiaEndpoint.SetText(colorCalibration.Gaia.Endpoint)
		if gaiaEndpoint.Text == "" {
			gaiaEndpoint.SetText("https://gea.esac.esa.int/tap-server/tap")
		}
		gaiaMatchRadius := widget.NewEntry()
		gaiaMatchRadius.SetText(strconv.FormatFloat(normalizeGaiaMatchRadiusArcsec(colorCalibration.Gaia.MatchRadiusArcsec), 'f', -1, 64))
		gaiaCachePathEntry := widget.NewEntry()
		effectiveGaiaSettings := applyGaiaCachePathPreference(colorCalibration.Gaia, app.Preferences().String(gaiaCachePathPreferenceKey))
		gaiaCachePathEntry.SetText(effectiveGaiaSettings.CachePath)
		gaiaCacheStatusLabel := widget.NewLabel("")
		refreshGaiaCacheStatus := func() {
			path, err := gaiaCachePath(applyGaiaCachePathPreference(colorCalibration.Gaia, app.Preferences().String(gaiaCachePathPreferenceKey)), "")
			if err != nil {
				gaiaCacheStatusLabel.SetText("Cache unavailable: " + err.Error())
				return
			}
			exists, bytes, err := gaiaCacheStatus(path)
			if err != nil {
				gaiaCacheStatusLabel.SetText("Cache unavailable: " + err.Error())
				return
			}
			if !exists {
				gaiaCacheStatusLabel.SetText("Cache: not created")
			} else {
				gaiaCacheStatusLabel.SetText(fmt.Sprintf("Cache: %s (%d bytes)", path, bytes))
			}
		}
		clearGaiaCacheButton := widget.NewButton("Clear cache", func() {
			path, err := gaiaCachePath(applyGaiaCachePathPreference(colorCalibration.Gaia, app.Preferences().String(gaiaCachePathPreferenceKey)), "")
			if err == nil {
				err = clearGaiaCache(path)
			}
			if err != nil {
				gaiaCacheStatusLabel.SetText("Cache clear failed: " + err.Error())
			} else {
				refreshGaiaCacheStatus()
			}
		})
		gaiaInfo := widget.NewLabel("")
		updateGaiaInfo := func() {
			gaiaInfo.SetText(fmt.Sprintf("Gaia %s · %s · cache %s", gaiaRelease.Text, map[bool]string{true: "cache-only", false: "online"}[colorCalibration.Gaia.AccessMode == "cacheOnly"], func() string {
				if effectiveGaiaSettings.CachePath == "" {
					return "default"
				}
				return effectiveGaiaSettings.CachePath
			}()))
		}
		updateGaiaInfo()
		refreshGaiaCacheStatus()
		neutral := NewToggle(nil)
		neutral.SetChecked(colorCalibration.NeutralizeBackground)
		white := NewSafeSelect([]string{"Flat Fnu", "Flat Flambda", "Average spiral galaxy"}, nil)
		white.SetSelected(map[models.WhiteReference]string{models.WhiteReferenceFlatFlambda: "Flat Flambda", models.WhiteReferenceAverageSpiralGalaxy: "Average spiral galaxy"}[colorCalibration.WhiteReference])
		if colorCalibration.WhiteReference == "" || colorCalibration.WhiteReference == models.WhiteReferenceFlatFnu {
			white.SetSelected("Flat Fnu")
		}
		selection := NewSafeSelect([]string{"Automatic", "Aligned reference ROI"}, nil)
		if colorCalibration.BackgroundSelection == models.BackgroundROI {
			selection.SetSelected("Aligned reference ROI")
		} else {
			selection.SetSelected("Automatic")
		}
		roiX, roiY, roiW, roiH := NewNumberEntry(0, 0), NewNumberEntry(0, 0), NewNumberEntry(0, 0), NewNumberEntry(0, 0)
		roiX.SetValue(float64(colorCalibration.BackgroundROI.X))
		roiY.SetValue(float64(colorCalibration.BackgroundROI.Y))
		roiW.SetValue(float64(colorCalibration.BackgroundROI.Width))
		roiH.SetValue(float64(colorCalibration.BackgroundROI.Height))
		saveCalibration := NewToggle(nil)
		saveCalibration.SetChecked(saveColorCalibration)
		beforePreview := widget.NewButton("Before", func() { v := false; calibrationPreviewOverride = &v; refresh() })
		afterPreview := widget.NewButton("After", func() { v := true; calibrationPreviewOverride = &v; refresh() })
		status := widget.NewLabel(composeCalibrationStatusText(colorCalibration))
		status.Wrapping = fyne.TextWrapWord
		calculate := widget.NewButton("Calculate", nil)
		cancelButton := widget.NewButton("Cancel", nil)
		setStale := func() {
			clearGaiaRefinement()
			markComposeCalibrationStale(&colorCalibration)
			status.SetText(composeCalibrationStatusText(colorCalibration))
			refresh()
		}
		mode.OnChanged = func(v string) {
			if v == "Instrument" {
				colorCalibration.PhotometricMode = models.PhotometricInstrument
			} else if v == "Gaia" {
				colorCalibration.PhotometricMode = models.PhotometricGaia
			} else {
				colorCalibration.PhotometricMode = models.PhotometricOff
			}
			setStale()
		}
		gaiaAccess.OnChanged = func(v string) {
			if v == "Cache Only" {
				colorCalibration.Gaia.AccessMode = "cacheOnly"
			} else {
				colorCalibration.Gaia.AccessMode = "online"
			}
			updateGaiaInfo()
			setStale()
		}
		gaiaRelease.OnChanged = func(v string) { colorCalibration.Gaia.Release = v; updateGaiaInfo(); setStale() }
		gaiaEndpoint.OnChanged = func(v string) { colorCalibration.Gaia.Endpoint = v; setStale() }
		gaiaMatchRadius.OnChanged = func(v string) {
			if radius, err := parseGaiaMatchRadiusArcsec(v); err == nil {
				colorCalibration.Gaia.MatchRadiusArcsec = radius
				setStale()
			}
		}
		gaiaCachePathEntry.OnChanged = func(v string) {
			updateComposeGaiaCachePath(&colorCalibration, v)
			app.Preferences().SetString(gaiaCachePathPreferenceKey, v)
			effectiveGaiaSettings.CachePath = v
			refreshGaiaCacheStatus()
		}
		neutral.OnChanged = func(v bool) { colorCalibration.NeutralizeBackground = v; setStale() }
		white.OnChanged = func(v string) {
			switch v {
			case "Flat Flambda":
				colorCalibration.WhiteReference = models.WhiteReferenceFlatFlambda
			case "Average spiral galaxy":
				colorCalibration.WhiteReference = models.WhiteReferenceAverageSpiralGalaxy
			default:
				colorCalibration.WhiteReference = models.WhiteReferenceFlatFnu
			}
			setStale()
		}
		selection.OnChanged = func(v string) {
			if v == "Aligned reference ROI" {
				colorCalibration.BackgroundSelection = models.BackgroundROI
			} else {
				colorCalibration.BackgroundSelection = models.BackgroundAutomatic
			}
			setStale()
		}
		setROI := func() {
			colorCalibration.BackgroundROI = models.CalibrationROI{X: int(roiX.Value()), Y: int(roiY.Value()), Width: int(roiW.Value()), Height: int(roiH.Value())}
			setStale()
		}
		roiX.OnChanged, roiY.OnChanged, roiW.OnChanged, roiH.OnChanged = func(float64) { setROI() }, func(float64) { setROI() }, func(float64) { setROI() }, func(float64) { setROI() }
		saveCalibration.OnChanged = func(v bool) { saveColorCalibration = v; refresh() }
		prior := colorCalibration
		cancelButton.Disable()
		calculate.OnTapped = func() {
			if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
				return
			}
			if colorCalibration.PhotometricMode == models.PhotometricGaia {
				radius, err := parseGaiaMatchRadiusArcsec(gaiaMatchRadius.Text)
				if err != nil {
					status.SetText(err.Error())
					return
				}
				colorCalibration.Gaia.MatchRadiusArcsec = radius
			}
			prior = colorCalibration
			calibrationGeneration++
			generation := calibrationGeneration
			ctx, ok := calibrationJob.begin(generation)
			if !ok {
				return
			}
			calibrationSnapshot := colorCalibration
			refinedResidualSnapshot := refinedGaiaResidual
			refinedSourcesSnapshot := append([]gaia.Source(nil), refinedGaiaSources...)
			calibrationSnapshot.Overlays = append([]models.OverlayCalibrationState(nil), colorCalibration.Overlays...)
			for i := range calibrationSnapshot.Overlays {
				calibrationSnapshot.Overlays[i].Diagnostics.Warnings = append([]string(nil), colorCalibration.Overlays[i].Diagnostics.Warnings...)
			}
			priorForJob := prior
			imageSnapshots := make([]*models.LoadedImage, 3)
			var offsetSnapshots [3][3]float64
			for i := range imageSnapshots {
				imageSnapshots[i] = cloneLoadedImageForStretchMatch(imgs[i])
				if composeChannelOffsetFields != nil {
					dx, dy, rot, _ := composeChannelOffsetFields(i)
					offsetSnapshots[i] = [3]float64{dx, dy, rot}
				}
			}
			colorCalibration.Status = models.CalibrationCalculating
			status.SetText(composeCalibrationStatusText(colorCalibration))
			calculate.Disable()
			cancelButton.Enable()
			mode.Disable()
			neutral.Disable()
			white.Disable()
			selection.Disable()
			gaiaMatchRadius.Disable()
			inputs := make([]processing.CalibrationInput, 3)
			streamInputs := make([]processing.CalibrationStreamInput, 3)
			largeCalibration := composeLargeModeActive != nil && composeLargeModeActive()
			var inputErr error
			var backgroundReaders [3]*alignedArtifactRowReader
			if largeCalibration && calibrationSnapshot.NeutralizeBackground {
				ref, ok := largeArtifacts[1]
				if !ok {
					inputErr = fmt.Errorf("channel 2 disk artifact is unavailable")
				} else {
					for i := range backgroundReaders {
						d, exists := largeArtifacts[i]
						if !exists {
							inputErr = fmt.Errorf("channel %d disk artifact is unavailable", i+1)
							break
						}
						off := offsetSnapshots[i]
						backgroundReaders[i] = &alignedArtifactRowReader{
							path: d.Path, source: *imageSnapshots[i], reference: *imageSnapshots[1],
							width: ref.Width, height: ref.Height, offsetX: off[0], offsetY: off[1], offsetRot: off[2],
						}
					}
				}
			}
			for i := range inputs {
				img := imageSnapshots[i]
				if !largeCalibration {
					pixels := append([]float32(nil), img.HDU.Data.Pixels...)
					inputs[i] = processing.CalibrationInput{SourceIdentity: img.Path, Width: img.HDU.Data.Width, Height: img.HDU.Data.Height, Pixels: pixels, Valid: make([]bool, len(pixels)), Alignment: fmt.Sprintf("channel-%d", i)}
					for j := range inputs[i].Valid {
						inputs[i].Valid[j] = true
					}
				} else {
					d, ok := largeArtifacts[i]
					if !ok {
						inputErr = fmt.Errorf("channel %d disk artifact is unavailable", i+1)
						break
					}
					streamInputs[i] = processing.CalibrationStreamInput{SourceIdentity: img.Path, Width: d.Width, Height: d.Height, Alignment: fmt.Sprintf("channel-%d", i)}
					path := d.Path
					streamInputs[i].ReadRow = func(y int, dst []float32) error {
						a, err := fitsio.OpenFloat32ArtifactReadOnly(path)
						if err != nil {
							return err
						}
						defer a.Close()
						return a.ReadRow(y, dst)
					}
					streamInputs[i].ReadValidRow = func(y int, dst []bool) error {
						for j := range dst {
							dst[j] = true
						}
						return nil
					}
				}
				if settingsMode := calibrationSnapshot.PhotometricMode; settingsMode == models.PhotometricInstrument {
					ref := processing.ReferenceFnu
					if calibrationSnapshot.WhiteReference == models.WhiteReferenceFlatFlambda {
						ref = processing.ReferenceFlambda
					}
					headerValue := func(h fitsio.Header, keys ...string) string {
						for _, key := range keys {
							if value := fitsio.HeaderString(h, key); value != "" {
								return value
							}
						}
						return ""
					}
					detector := headerValue(img.HDU.Header, "DETECTOR")
					if strings.HasPrefix(strings.ToUpper(detector), "NRC") {
						detector = "NRC"
					}
					metadata := processing.InstrumentMetadata{Telescope: headerValue(img.Primary, "TELESCOP", "TELESCOPE"), Instrument: headerValue(img.Primary, "INSTRUME", "INSTRUMENT"), Detector: detector, Filter: headerValue(img.HDU.Header, "FILTER", "FILTER1", "FILTER2"), Primary: img.Primary, SCI: img.HDU.Header, Reference: ref}
					photometry, parseErr := processing.ParseInstrumentPhotometry(metadata)
					if parseErr != nil {
						inputErr = parseErr
						break
					}
					if largeCalibration {
						streamInputs[i].Metadata = metadata
						streamInputs[i].Photometry = &photometry
					} else {
						inputs[i].Metadata = metadata
						inputs[i].Photometry = &photometry
					}
				}
			}
			settings := processing.CalibrationSettings{PhotometricMode: calibrationSnapshot.PhotometricMode, NeutralizeBackground: calibrationSnapshot.NeutralizeBackground, BackgroundSelection: calibrationSnapshot.BackgroundSelection, BackgroundROI: calibrationSnapshot.BackgroundROI, WhiteReference: calibrationSnapshot.WhiteReference, LinkedStretch: calibrationSnapshot.LinkedStretch, Overlays: append([]models.OverlayCalibrationState(nil), calibrationSnapshot.Overlays...), AlgorithmVersion: "ui-v1", ReferenceVersion: "local-v1", Gaia: calibrationSnapshot.Gaia}
			go func() {
				var result processing.CalibrationResult
				gaiaDone := false
				err := inputErr
				if err == nil && calibrationSnapshot.PhotometricMode == models.PhotometricGaia {
					debuglog.Log(fmt.Sprintf("Gaia UI calibration start: access=%s release=%s radius=%.3f magnitude=%.2f cache=%s", calibrationSnapshot.Gaia.AccessMode, calibrationSnapshot.Gaia.Release, calibrationSnapshot.Gaia.MatchRadiusArcsec, calibrationSnapshot.Gaia.MagnitudeLimit, calibrationSnapshot.Gaia.CachePath))
					// Gaia is a staged provider job; construct it from the immutable
					// image/settings snapshot so cache-only mode never reaches HTTP.
					calibrationSnapshot.Gaia = applyGaiaCachePathPreference(calibrationSnapshot.Gaia, app.Preferences().String(gaiaCachePathPreferenceKey))
					cachePath, pathErr := gaiaCachePath(calibrationSnapshot.Gaia, "")
					if pathErr != nil {
						err = pathErr
					} else {
						calibrationSnapshot.Gaia = resolveGaiaSettings(calibrationSnapshot.Gaia)
						cache, openErr := composeGaiaCacheOpener(ctx, cachePath)
						if openErr != nil {
							err = openErr
						} else {
							defer cache.Close()
							mode := gaia.AccessOnline
							if calibrationSnapshot.Gaia.AccessMode == "cacheOnly" {
								mode = gaia.AccessCacheOnly
							}
							provider, providerErr := newGaiaProvider(calibrationSnapshot.Gaia.Endpoint, mode, calibrationSnapshot.Gaia.Release, calibrationSnapshot.Gaia.XPRepresentation, cache)
							if providerErr != nil {
								err = providerErr
							} else {
								query, pixelToSky, queryErr := deriveGaiaFieldQuery(imageSnapshots[1], calibrationSnapshot.Gaia)
								if queryErr != nil {
									err = queryErr
								} else {
									calibrationSnapshot.Gaia.ObservationEpoch = query.ObservationEpoch
									query.Release = calibrationSnapshot.Gaia.Release
									if calibrationSnapshot.Gaia.MagnitudeLimit > 0 {
										query.MagnitudeLimit = calibrationSnapshot.Gaia.MagnitudeLimit
									}
									query.ObservationEpoch = calibrationSnapshot.Gaia.ObservationEpoch
									// Discover and normalize Gaia sources once. The same immutable
									// slice is reused by refinement/calibration so refinement never
									// triggers a second catalog or XP request.
									sources := refinedSourcesSnapshot
									discoverErr := error(nil)
									if len(sources) == 0 {
										sources, discoverErr = provider.DiscoverSources(ctx, query)
										if discoverErr == nil {
											sources, discoverErr = gaia.NormalizeSources(sources, calibrationSnapshot.Gaia.Release)
										}
									}
									if discoverErr != nil {
										err = discoverErr
									}
									var stars []processing.Star
									var aw, ah int
									var alignedPlanes [3]processing.AlignedPlane
									var detectionLease *fitsio.MaterializedPlane
									if largeCalibration {
										d, ok := largeArtifacts[1]
										if !ok {
											err = fmt.Errorf("channel 2 disk artifact is unavailable")
										} else {
											aw, ah = d.Width, d.Height
											reader := artifactRowReader{path: d.Path}
											stars, err = processing.ExtractStarsTiledReader(ctx, reader, aw, ah, 256, 5, 3)
											if len(stars) > processing.TweakRegCatalogMaxStars {
												stars = stars[:processing.TweakRegCatalogMaxStars]
											}
										}
									} else {
										aligned, aw0, ah0, alignErr := processing.AlignedPlanesForCalibration(ctx, imageSnapshots)
										if alignErr != nil {
											err = alignErr
										} else {
											aw, ah = aw0, ah0
											alignedPlanes = aligned
											stars = processing.ExtractAndLimitStars(alignedPlanes[1].Pixels, aw, ah, 5, 3, 500)
										}
									}
									if detectionLease != nil {
										defer detectionLease.Release()
									}
									if err == nil {
										debuglog.Log(fmt.Sprintf("Gaia UI alignment/detection: aligned=%dx%d detected_stars=%d", aw, ah, len(stars)))
										planes := [3]processing.GaiaPlane{}
										readers := [3]processing.GaiaPlaneReader{}
										for c := range planes {
											if largeCalibration {
												d, ok := largeArtifacts[2-c]
												if !ok {
													err = fmt.Errorf("channel %d disk artifact is unavailable", 3-c)
													break
												}
												off := offsetSnapshots[2-c]
												readers[c] = &alignedArtifactRowReader{path: d.Path, source: *imageSnapshots[2-c], reference: *imageSnapshots[1], width: aw, height: ah, offsetX: off[0], offsetY: off[1], offsetRot: off[2]}
												planes[c].Width, planes[c].Height = aw, ah
												continue
											}
											// UI channels are B,G,R while the canonical planes are R,G,B.
											p := alignedPlanes[2-c]
											pixels := append([]float32(nil), p.Pixels...)
											for i, valid := range p.Valid {
												if i < len(pixels) && !valid {
													pixels[i] = float32(math.NaN())
												}
											}
											planes[c] = processing.GaiaPlane{Pixels: pixels, Valid: append([]bool(nil), p.Valid...), Width: aw, Height: ah}
										}
										matchRadius := calibrationSnapshot.Gaia.MatchRadiusArcsec
										if matchRadius <= 0 {
											matchRadius = 2
										}
										epoch := calibrationSnapshot.Gaia.ObservationEpoch
										if epoch <= 0 {
											epoch = 2000
										}
										magnitude := calibrationSnapshot.Gaia.MagnitudeLimit
										if magnitude <= 0 {
											magnitude = 18
										}
										if refinedResidualSnapshot != nil {
											pixelToSky, err = processing.RefinedPixelToSky(pixelToSky, *refinedResidualSnapshot)
										}
										greq := processing.GaiaCalibrationRequest{Query: query, Settings: gaiaRequestSettings(calibrationSnapshot.Gaia, matchRadius, epoch, magnitude), Sources: sources, DetectedStars: stars, Planes: planes, Readers: readers, PixelToSky: pixelToSky}
										greq.Settings.ObservationEpoch = query.ObservationEpoch
										gaiaResult, runErr := composeGaiaJobService.Run(ctx, GaiaJobRequest{Provider: provider, Query: query, Settings: greq.Settings, Calibration: greq}, func(p GaiaJobProgress) {
											debuglog.Log(fmt.Sprintf("Gaia UI stage: %s", p.Stage))
											fyne.Do(func() { status.SetText(fmt.Sprintf("Calibration: calculating — Gaia %s", p.Stage)) })
										})
										for c := range readers {
											if r, ok := readers[c].(*alignedArtifactRowReader); ok {
												_ = r.Close()
											}
										}
										if runErr != nil {
											err = runErr
											debuglog.Log(fmt.Sprintf("Gaia UI terminal: failed error=%v", runErr))
										} else {
											gaiaDone = true
											result.Base = [3]models.LinearTransform{{Gain: gaiaResult.Diagnostics.Gains[0]}, {Gain: gaiaResult.Diagnostics.Gains[1]}, {Gain: gaiaResult.Diagnostics.Gains[2]}}
											result.Status = gaiaResult.Status
											result.Diagnostics.Message = fmt.Sprintf("Gaia: %d matched, %d accepted", gaiaResult.Diagnostics.MatchedStars, gaiaResult.Diagnostics.AcceptedStars)
											result.Provenance = gaiaResult.Provenance
											result.SourceFingerprint = gaiaResult.SourceFingerprint
											result.SettingsFingerprint = gaiaResult.SettingsFingerprint
											debuglog.Log(fmt.Sprintf("Gaia UI terminal: status=%s matched=%d accepted=%d rejected=%d", gaiaResult.Status, gaiaResult.Diagnostics.MatchedStars, gaiaResult.Diagnostics.AcceptedStars, gaiaResult.Diagnostics.RejectedStars))
										}
									}
								}
							}
						}
					}
				}
				if err == nil && largeCalibration && calibrationSnapshot.PhotometricMode != models.PhotometricGaia {
					backgroundInputs := append([]processing.CalibrationStreamInput(nil), streamInputs...)
					calibrationInputs := streamInputs
					if calibrationSnapshot.NeutralizeBackground {
						for i, reader := range backgroundReaders {
							if reader == nil {
								err = fmt.Errorf("channel %d aligned background reader is unavailable", i+1)
								break
							}
							backgroundInputs[i].Width, backgroundInputs[i].Height = reader.width, reader.height
							backgroundInputs[i].ReadRow = reader.ReadRow
							backgroundInputs[i].ReadValidRow = reader.ReadValidRow
						}
						// The aligned reference-grid samples are the canonical input
						// for neutralized calibration. Use them for the final streamed
						// fingerprint/calculation as well as background estimation;
						// otherwise the persisted fingerprint would describe a raw,
						// differently sized raster than normal Compose.
						calibrationInputs = append([]processing.CalibrationStreamInput(nil), backgroundInputs...)
						defer func() {
							for _, reader := range backgroundReaders {
								if reader != nil {
									_ = reader.Close()
								}
							}
						}()
					}
					if err == nil {
						for i := range streamInputs {
							if calibrationSnapshot.NeutralizeBackground {
								var roi *models.CalibrationROI
								if calibrationSnapshot.BackgroundSelection == models.BackgroundROI {
									r := calibrationSnapshot.BackgroundROI
									roi = &r
								}
								e, eerr := processing.EstimateBackgroundStream(ctx, backgroundInputs[i], roi, processing.DefaultBackgroundConfig())
								if eerr != nil {
									err = eerr
									break
								}
								if e.Status != models.CalibrationValid {
									err = &processing.UnsupportedCalibrationError{Reason: fmt.Sprintf("channel %d background: %s", i+1, e.RejectionReason)}
									break
								}
								calibrationInputs[i].Background = e.Transform
							}
						}
						if err == nil {
							result, err = processing.CalculateCalibrationStreaming(ctx, calibrationInputs, settings)
						}
					}
				}
				if err != nil {
					// Metadata parsing above produced the actionable unsupported reason.
				} else if ctx.Err() != nil {
					err = ctx.Err()
				} else if !largeCalibration {
					if calibrationSnapshot.NeutralizeBackground {
						roi := calibrationSnapshot.BackgroundROI
						var roiPtr *models.CalibrationROI
						if calibrationSnapshot.BackgroundSelection == models.BackgroundROI {
							roiPtr = &roi
						}
						alignedPlanes, _, _, alignErr := processing.AlignedPlanesForCalibration(ctx, imageSnapshots)
						if alignErr != nil {
							err = alignErr
						}
						if err == nil {
							for i := range inputs {
								plane := alignedPlanes[2-i]
								inputs[i].Pixels = append([]float32(nil), plane.Pixels...)
								inputs[i].Valid = append([]bool(nil), plane.Valid...)
								inputs[i].Width, inputs[i].Height = plane.Width, plane.Height
							}
						}
						for i := range inputs {
							if err != nil {
								break
							}
							if ctx.Err() != nil {
								err = ctx.Err()
								break
							}
							// Compose's canonical plane order is R,G,B while the UI
							// channel slots are B,G,R.
							plane := alignedPlanes[2-i]
							estimate, estimateErr := processing.EstimateBackgroundPlane(ctx, processing.BackgroundPlane{Pixels: plane.Pixels, Width: plane.Width, Height: plane.Height, Valid: plane.Valid, ROI: roiPtr}, processing.DefaultBackgroundConfig())
							if estimateErr != nil {
								err = estimateErr
								break
							}
							if estimate.Status != models.CalibrationValid {
								err = &processing.UnsupportedCalibrationError{Reason: fmt.Sprintf("channel %d background: %s", i+1, estimate.RejectionReason)}
								break
							}
							estimate.Transform.Gain = 1
							inputs[i].Background = estimate.Transform
						}
					}
				}
				if err == nil && ctx.Err() != nil {
					err = ctx.Err()
				}
				if err == nil && !gaiaDone && !largeCalibration {
					result, err = processing.CalculateCalibration(inputs, settings)
				}
				fyne.Do(func() {
					if ctx.Err() != nil && err == nil {
						err = ctx.Err()
					}
					liveGaiaCachePath := colorCalibration.Gaia.CachePath
					accepted := composeCalibrationResultForUI(&calibrationJob, generation, calibrationGeneration, &colorCalibration, &priorForJob, result, err)
					if accepted {
						if gaiaDone {
							// Persist only the resolved settings of an accepted Gaia job;
							// failed or superseded jobs must not alter current settings.
							liveSettings := colorCalibration.Gaia
							liveSettings.CachePath = liveGaiaCachePath
							colorCalibration.Gaia = retainComposeProjectGaiaCachePath(liveSettings, calibrationSnapshot.Gaia)
						}
						status.SetText(composeCalibrationStatusText(colorCalibration))
						refresh()
					}
					if accepted && generation == calibrationGeneration {
						calculate.Enable()
						cancelButton.Disable()
						mode.Enable()
						neutral.Enable()
						white.Enable()
						selection.Enable()
						gaiaMatchRadius.Enable()
					}
				})
			}()
		}
		cancelButton.OnTapped = func() {
			if calibrationJob.cancelJob() {
				colorCalibration = prior
				if prior.Status != models.CalibrationValid {
					colorCalibration.Status = models.CalibrationCancelled
				}
				status.SetText(composeCalibrationStatusText(colorCalibration))
				calculate.Enable()
				cancelButton.Disable()
				mode.Enable()
				neutral.Enable()
				white.Enable()
				selection.Enable()
				gaiaMatchRadius.Enable()
				refresh()
			}
		}
		var calibrationWindow fyne.Window
		closeCalibration := widget.NewButton("Close", func() {
			if calibrationWindow != nil {
				calibrationWindow.Close()
			}
		})
		content := container.NewVBox(
			container.NewBorder(nil, nil, widget.NewLabel("Photometric mode"), nil, mode),
			container.NewHBox(neutral, widget.NewLabel("Neutralize background")),
			container.NewBorder(nil, nil, widget.NewLabel("White reference"), nil, white),
			container.NewBorder(nil, nil, widget.NewLabel("Background selection"), nil, selection),
			container.NewBorder(nil, nil, widget.NewLabel("Gaia access"), nil, gaiaAccess),
			container.NewGridWithColumns(2, widget.NewLabel("Gaia release"), gaiaRelease, widget.NewLabel("Gaia endpoint"), gaiaEndpoint),
			container.NewBorder(nil, nil, widget.NewLabel("Gaia match radius (arcsec)"), nil, gaiaMatchRadius),
			container.NewBorder(nil, nil, widget.NewLabel("Cache path"), clearGaiaCacheButton, gaiaCachePathEntry),
			gaiaCacheStatusLabel,
			gaiaInfo,
			container.NewGridWithColumns(4, roiX, roiY, roiW, roiH), status,
			container.NewHBox(calculate, cancelButton, saveCalibration, widget.NewLabel("Save Color Calibration"), beforePreview, afterPreview, layout.NewSpacer(), closeCalibration),
		)
		calibrationWindow = app.NewWindow("Color Calibration")
		colorCalibrationWindow = calibrationWindow
		// Keep the controls clear of every edge of the scroll viewport. The
		// explicit spacers make this a stable 20 px margin regardless of theme.
		calibrationWindow.SetContent(container.NewVScroll(container.NewBorder(vpad(20), vpad(20), hpad(20), hpad(20), content)))
		calibrationWindow.SetOnClosed(func() {
			if colorCalibrationWindow == calibrationWindow {
				colorCalibrationWindow = nil
			}
			calibrationPreviewOverride = nil
			refresh()
		})
		// A scroll container reports only its first child's minimum height until its
		// viewport is explicitly sized. Without this, the Color Calibration window
		// can appear as just the photometric-mode row, hiding the settings and
		// Calculate controls below it.
		calibrationWindow.Resize(fyne.NewSize(760, 640))
		calibrationWindow.Show()
	}

	copySettingsItem := fyne.NewMenuItem("Copy Channel 1 Settings to 2 & 3", copySettings)
	matchStretchItem := fyne.NewMenuItem("Match Channel Stretch...", showMatchStretchDialog)
	showGaiaPicker := func() {
		if imgs[1] == nil {
			dialog.ShowInformation("Missing Channel 2", "Load Channel 2 before selecting Gaia reference stars.", win)
			return
		}
		preview, _ := viewports[1].image.Image.(image.Image)
		snapshot := cloneLoadedImageForStretchMatch(imgs[1])
		pickerGeneration := gaiaRefinementGeneration
		sourcePath, sourceWidth, sourceHeight := snapshot.Path, snapshot.HDU.Data.Width, snapshot.HDU.Data.Height
		showComposeGaiaPicker(app, win, snapshot, preview, composeGaiaResidualRunnerForImage(snapshot, colorCalibration.Gaia), func(result composeGaiaResidualResult) bool {
			if pickerGeneration != gaiaRefinementGeneration || imgs[1] == nil || imgs[1].Path != sourcePath || imgs[1].HDU.Data.Width != sourceWidth || imgs[1].HDU.Data.Height != sourceHeight || imgs[1].Rotation90 != 0 || imgs[1].HasAlignTransform {
				return false
			}
			residual := result.Transform
			refinedGaiaResidual = &residual
			refinedGaiaSources = append([]gaia.Source(nil), result.Sources...)
			markComposeCalibrationStale(&colorCalibration)
			refresh()
			return true
		})
	}
	gaiaPickerItem := fyne.NewMenuItem("Pick Channel 2 Gaia Stars...", showGaiaPicker)
	addLayerItem := fyne.NewMenuItem("Add Colored Layer...", addColoredLayer)
	colorCalibrationItem := fyne.NewMenuItem("Color Calibration...", openColorCalibration)
	normalizeScaleItem := fyne.NewMenuItem("Normalize Scale to Channel 2", normalizeScale)
	sendToEditItem := fyne.NewMenuItem("Send Composite to Edit", sendToEdit)
	alignChannelsItem := fyne.NewMenuItem("Align to Channel 2", alignChannels)
	cleanChannelsItem := fyne.NewMenuItem("Cross-Channel Clean", crossChannelClean)
	resetDataItem := fyne.NewMenuItem("Reset Data (Undo Align & Clean)", resetData)

	openLevels := func() {
		if levelsWin == nil {
			levelsWin = newRGBLevelsWindow(app, levels, refresh)
		}
		levelsWin.setHistogram(latestRGBStats)
		levelsWin.updateEntries()
		levelsWin.win.Show()
		levelsWin.win.RequestFocus()
	}

	// File: project I/O and handing the result off to other tabs / disk.
	fileMenu := fyne.NewMenu("File",
		loadProjectItem,
		loadFilterSetItem,
		saveProjectItem,
		fyne.NewMenuItemSeparator(),
		exportRGBItem,
		sendToEditItem,
	)
	// Compose: everything that acts on the channels themselves (merge of the
	// former Channels and Process menus).
	composeMenu := fyne.NewMenu("Compose",
		alignChannelsItem,
		gaiaPickerItem,
		cleanChannelsItem,
		resetDataItem,
		fyne.NewMenuItemSeparator(),
		copySettingsItem,
		matchStretchItem,
		normalizeScaleItem,
		fyne.NewMenuItemSeparator(),
		addLayerItem,
		colorCalibrationItem,
	)
	// View: display tuning plus the per-channel FITS header viewers/savers.
	viewMenu := fyne.NewMenu("View",
		fyne.NewMenuItem("RGB Levels...", openLevels),
		fyne.NewMenuItem("Color Legend...", showColorLegend),
		fyne.NewMenuItemSeparator(),
		viewHeaderItems[0],
		viewHeaderItems[1],
		viewHeaderItems[2],
		fyne.NewMenuItemSeparator(),
		saveHeaderItems[0],
		saveHeaderItems[1],
		saveHeaderItems[2],
	)

	updateMenus = func() {
		for i := range viewHeaderItems {
			disabled := imgs[i] == nil
			viewHeaderItems[i].Disabled = disabled
			saveHeaderItems[i].Disabled = disabled
		}
		copySettingsItem.Disabled = imgs[0] == nil
		matchStretchItem.Disabled = imgs[0] == nil && imgs[1] == nil && imgs[2] == nil
		gaiaPickerItem.Disabled = imgs[1] == nil

		allLoaded := imgs[0] != nil && imgs[1] != nil && imgs[2] != nil

		normalizeScaleItem.Disabled = !allLoaded
		alignChannelsItem.Disabled = !allLoaded
		cleanChannelsItem.Disabled = !allLoaded
		resetDataItem.Disabled = !allLoaded
		exportRGBItem.Disabled = !allLoaded
		sendToEditItem.Disabled = !allLoaded || (largeMode && largeStore != nil && func() bool { _, ok := largeStore.Composite(); return !ok }())
		colorCalibrationItem.Disabled = !allLoaded

		// if allLoaded {
		// 	alignBtn.Enable()
		// 	crossCleanBtn.Enable()
		// } else {
		// 	alignBtn.Disable()
		// 	crossCleanBtn.Disable()
		// }

		anyOverlayLoaded := false
		for _, l := range overlayLayers {
			if l.win != nil && l.idx < len(imgs) && imgs[l.idx] != nil {
				anyOverlayLoaded = true
				break
			}
		}
		anyChannelLoaded := imgs[0] != nil || imgs[1] != nil || imgs[2] != nil || anyOverlayLoaded
		if anyChannelLoaded {
			largeFilesCheck.Disable()
		} else {
			largeFilesCheck.Enable()
		}
		if largeFilesCheck.Checked {
			blinkCheck.SetChecked(false)
			blinkCheck.Disable()
		} else {
			blinkCheck.Enable()
		}
		saveProjectItem.Disabled = imgs[0] == nil && imgs[1] == nil && imgs[2] == nil && !anyOverlayLoaded
		if m := win.MainMenu(); m != nil {
			m.Refresh()
		}
	}
	updateMenus()
	largeFilesCheck.OnChanged = func(enabled bool) {
		hasOverlayImage := false
		for _, layer := range overlayLayers {
			if layer != nil && layer.idx >= 0 && layer.idx < len(imgs) && imgs[layer.idx] != nil {
				hasOverlayImage = true
				break
			}
		}
		if enabled && (imgs[0] != nil || imgs[1] != nil || imgs[2] != nil || hasOverlayImage) {
			largeFilesCheck.SetChecked(false)
			return
		}
		if enabled {
			store, err := newComposeLargeStore("")
			if err != nil {
				largeFilesCheck.SetChecked(false)
				dialog.ShowError(err, win)
				return
			}
			largeStore = store
			largeRuntime.store = store
			largeMode = true
			blinkCheck.SetChecked(false)
			blinkCheck.Disable()
			app.Preferences().SetBool(composeLargeFilesPreferenceKey, true)
		} else {
			largeMode = false
			app.Preferences().SetBool(composeLargeFilesPreferenceKey, false)
			largeMu.Lock()
			largeSessionGeneration++
			store := largeStore
			largeStore = nil
			largeMu.Unlock()
			if store != nil {
				_ = store.Close()
			}
			largeMu.Lock()
			largePreviews = make(map[int]*image.RGBA)
			largeArtifacts = make(map[int]composeArtifactDescriptor)
			largeRuntime.previews = largePreviews
			largeRuntime.artifacts = largeArtifacts
			largeMu.Unlock()
		}
		updateMenus()
	}
	globalComposeLargeCleanup = func() {
		largeMu.Lock()
		largeSessionGeneration++
		store := largeStore
		largeStore = nil
		largeMu.Unlock()
		if store != nil {
			editSnapshotMu.Lock()
			if editSnapshotCancel != nil {
				editSnapshotCancel()
			}
			editSnapshotMu.Unlock()
			_ = store.Close()
		}
	}

	// Register package-level callback so the preview window can inject an image
	// into any channel with its current stretch settings.
	globalSendToChannel = func(channelIdx int, img *models.LoadedImage) {
		if channelIdx < 0 || channelIdx >= 3 {
			return
		}
		if composeLargeModeActive != nil && composeLargeModeActive() {
			if img == nil || img.HDU.Data.Width <= 0 || img.HDU.Data.Height <= 0 || len(img.HDU.Data.Pixels) < img.HDU.Data.Width*img.HDU.Data.Height || largeStore == nil {
				debuglog.Log("Compose large mode: Send to Channel requires a materialized incoming image")
				return
			}
			incoming := *img
			pixels := img.HDU.Data.Pixels
			w, h := img.HDU.Data.Width, img.HDU.Data.Height
			slot := fmt.Sprintf("channel-%d", channelIdx)
			request, session, store := nextLargeLoadGeneration(slot)
			stageSlot := fmt.Sprintf("send-channel-%d-%d", channelIdx, time.Now().UnixNano())
			largeMu.RLock()
			oldDescriptor := largeArtifacts[channelIdx]
			largeMu.RUnlock()
			go func() {
				if store == nil {
					fyne.Do(func() { dialog.ShowError(fmt.Errorf("disk-backed Compose store is unavailable"), win) })
					return
				}
				d, err := store.Replace(stageSlot, w, h, func(out *fitsio.Float32Artifact) error {
					row := make([]float32, w)
					for y := 0; y < h; y++ {
						copy(row, pixels[y*w:(y+1)*w])
						if err := out.WriteRow(y, row); err != nil {
							return err
						}
					}
					return nil
				})
				if err == nil {
					incoming.HDU.Data.Pixels = nil
					preview, _, _, pErr := composeLargeStretchedPreview(d.Path, &incoming)
					err = pErr
					if err != nil {
						_, _ = store.RemoveSlotIfCurrent(d)
					} else {
						largeMu.Lock()
						currentSession := largeSessionGeneration == session && largeLoadGenerations[slot] == request && largeStore == store
						if !currentSession {
							largeMu.Unlock()
							_, _ = store.RemoveSlotIfCurrent(d)
							err = errors.New("stale Compose channel generation")
							return
						}
						largeArtifacts[channelIdx] = d
						largePreviews[channelIdx] = preview
						largeMu.Unlock()
						if oldDescriptor.Slot != "" {
							_, _ = store.RemoveSlotIfCurrent(oldDescriptor)
						}
					}
				}
				fyne.Do(func() {
					if err != nil {
						dialog.ShowError(err, win)
						return
					}
					largeMu.Lock()
					current, currentOK := largeArtifacts[channelIdx]
					currentSession := largeStore == store && composeLargeSendCommitAllowed(session, largeSessionGeneration, request, largeLoadGenerations[slot], currentOK, current, d)
					largeMu.Unlock()
					if !currentSession {
						_, _ = store.RemoveSlotIfCurrent(d)
						return
					}
					largeMu.Lock()
					replaceComposeChannelImage(imgs, channelIdx, &incoming)
					largeMu.Unlock()
					clearComposeOrigPixels(&origPixels, channelIdx)
					applyChannelState(channelIdx, channelStateFromImage(&incoming), imgs, viewports, controlSets)
					invalidateCalibration()
					refresh()
					if updateMenus != nil {
						updateMenus()
					}
				})
			}()
			return
		}
		replaceComposeChannelImage(imgs, channelIdx, img)
		clearComposeOrigPixels(&origPixels, channelIdx)
		applyChannelState(channelIdx, channelStateFromImage(img), imgs, viewports, controlSets)
		refresh()
		if updateMenus != nil {
			updateMenus()
		}
	}

	channelTabs := NewChannelTabs(
		NewChannelTabItem("Blue", color.RGBA{R: 100, G: 149, B: 237, A: 255}, controlSets[0].Content),
		NewChannelTabItem("Green", color.RGBA{R: 80, G: 200, B: 80, A: 255}, controlSets[1].Content),
		NewChannelTabItem("Red", color.RGBA{R: 237, G: 80, B: 80, A: 255}, controlSets[2].Content),
	)

	clearBtn := widget.NewButton("Clear Channels", clearChannels)
	clearBtn.Importance = widget.DangerImportance

	resetBtn := widget.NewButton("Reset", resetCompose)
	resetBtn.Importance = widget.DangerImportance

	histScaleStatus := widget.NewLabel("")
	histScaleStatus.Wrapping = fyne.TextWrapWord
	updateHistScaleLabel = func() {
		if sharedHistCheck.Checked {
			histScaleStatus.SetText("Histograms: shared filter scale")
			return
		}
		histScaleStatus.SetText("Histograms: per-filter auto scale")
	}
	updateHistScaleLabel()

	var magicAll *widget.Button
	magicAll = widget.NewButton("Magic", func() {
		if composeLargeModeActive != nil && composeLargeModeActive() {
			preset := processing.ParseMagicPreset(composeMagicPreset.Selected)
			type target struct {
				idx      int
				identity *models.LoadedImage
				image    models.LoadedImage
				d        composeArtifactDescriptor
			}
			largeRuntime.mu.RLock()
			targets := make([]target, 0, len(imgs))
			for i, img := range imgs {
				if img == nil {
					continue
				}
				d, ok := largeRuntime.artifacts[i]
				if !ok {
					continue
				}
				targets = append(targets, target{idx: i, identity: img, image: snapshotLargeLoadedImage(img), d: d})
			}
			largeRuntime.mu.RUnlock()
			go func() {
				largeRuntime.jobMu.Lock()
				defer largeRuntime.jobMu.Unlock()
				type result struct {
					idx      int
					identity *models.LoadedImage
					img      models.LoadedImage
					preview  *image.RGBA
					d        composeArtifactDescriptor
				}
				results := make([]result, 0, len(targets))
				for _, target := range targets {
					d := target.d
					lease, err := fitsio.MaterializeFloat32ArtifactLease(d.Path)
					if err != nil {
						return
					}
					clone := target.image
					clone.HDU.Data.Pixels = lease.Pixels
					processing.ApplyMagicLevels(&clone, preset)
					processing.AutoMTFMidtone(&clone)
					lease.Release()
					preview, _, _, err := composeLargeStretchedPreview(d.Path, &clone)
					if err != nil {
						return
					}
					results = append(results, result{target.idx, target.identity, clone, preview, d})
				}
				fyne.Do(func() {
					largeRuntime.mu.Lock()
					for _, r := range results {
						cur, ok := largeRuntime.artifacts[r.idx]
						var storeCur composeArtifactDescriptor
						storeOK := largeRuntime.store != nil
						if storeOK {
							storeCur, storeOK = largeRuntime.store.Descriptor(r.d.Slot)
						}
						identityOK := r.idx < len(imgs) && imgs[r.idx] == r.identity
						if !ok || !storeOK || !identityOK || cur.Generation != r.d.Generation || cur.Path != r.d.Path || storeCur.Generation != r.d.Generation || storeCur.Path != r.d.Path {
							largeRuntime.mu.Unlock()
							return
						}
					}
					type syncItem struct {
						fn  func(*models.LoadedImage)
						img *models.LoadedImage
					}
					syncFns := make([]syncItem, 0, len(results))
					for _, r := range results {
						r.img.HDU.Data.Pixels = nil
						imgs[r.idx] = &r.img
						largeRuntime.previews[r.idx] = r.preview
						if syncFn := largeRuntime.syncWidgets[r.idx]; syncFn != nil {
							syncFns = append(syncFns, syncItem{fn: syncFn, img: imgs[r.idx]})
						}
					}
					largeRuntime.mu.Unlock()
					for _, item := range syncFns {
						item.fn(item.img)
					}
					refresh()
				})
			}()
			return
		}
		preset := processing.ParseMagicPreset(composeMagicPreset.Selected)
		// Snapshot loaded channels; processing runs off the UI thread.
		type magicTarget struct {
			idx      int
			img      *models.LoadedImage
			before   magicStretchSnapshot
			prepared models.LoadedImage
		}
		targets := make([]magicTarget, 0, len(imgs))
		for i, img := range imgs {
			if img != nil {
				targets = append(targets, magicTarget{idx: i, img: img, before: snapshotMagicStretch(img), prepared: *img})
			}
		}
		if len(targets) == 0 {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		finished := false
		var progressDialog *dialog.CustomDialog
		progressLabel := widget.NewLabel("Applying Magic + Auto MTF to loaded channels…")
		cancelButton := widget.NewButton("Cancel", func() {
			cancel()
			if progressDialog != nil {
				progressDialog.Hide()
			}
		})
		progressDialog = dialog.NewCustomWithoutButtons("Magic", container.NewVBox(
			progressLabel,
			widget.NewProgressBarInfinite(), cancelButton,
		), win)
		progressDialog.SetOnClosed(func() {
			if !finished {
				cancel()
			}
		})
		progressDialog.Show()
		magicAll.Disable()
		go func() {
			results := make([]models.LoadedImage, len(targets))
			for j, target := range targets {
				if composeMagicCanceled(ctx) != nil {
					fyne.Do(func() {
						finished = true
						if progressDialog != nil {
							progressDialog.Hide()
						}
						magicAll.Enable()
					})
					return
				}
				clone := target.prepared
				processing.ApplyMagicLevels(&clone, preset)
				processing.AutoMTFMidtone(&clone)
				results[j] = clone
			}
			fyne.Do(func() {
				defer cancel()
				if ctx.Err() != nil {
					finished = true
					if progressDialog != nil {
						progressDialog.Hide()
					}
					magicAll.Enable()
					return
				}
				for _, target := range targets {
					if target.idx >= len(imgs) || imgs[target.idx] != target.img || !magicStretchUnchanged(target.img, target.before) {
						finished = true
						if progressDialog != nil {
							progressDialog.Hide()
						}
						magicAll.Enable()
						return
					}
				}
				withSuspendedRefresh(func() {
					for j, target := range targets {
						installMagicStretch(target.img, results[j])
						if target.idx < len(controlSets) {
							applyChannelState(target.idx, channelStateFromImage(target.img), imgs, viewports, controlSets)
							continue
						}
						for _, layer := range overlayLayers {
							if layer != nil && layer.idx == target.idx && layer.control != nil && layer.viewport != nil {
								applyChannelState(target.idx, channelStateFromImage(target.img), imgs, layerViews(layer), layerControls(layer))
								break
							}
						}
					}
				})
				finished = true
				progressLabel.SetText("Refreshing previews…")
				cancelButton.Disable()
				refreshAsync(func() {
					if progressDialog != nil {
						progressDialog.Hide()
					}
					magicAll.Enable()
				})
			})
		}()
	})
	magicAll.Importance = widget.HighImportance

	options := container.NewVBox(
		container.NewHBox(widget.NewLabel("Magic preset"), composeMagicPreset),
		container.NewHBox(sharedHistCheck, widget.NewLabel("Shared histogram scale")),
		histScaleStatus,
		container.NewHBox(largeFilesCheck, widget.NewLabel("Disk-backed large files (slow)")),
		container.NewHBox(blinkCheck, widget.NewLabel("Blink filters")),
		widget.NewButton("Choose channels...", chooseBlinkChannels),
		blinkStatus,
		container.NewHBox(measureCheck, widget.NewLabel("Measure composite")),
		clearBtn,
		resetBtn,
	)
	channels := container.NewVBox(
		magicAll,
		container.NewHBox(buildCompositeCheck, widget.NewLabel("Build color composite")),
		widget.NewSeparator(),
		measureLabel,
		widget.NewSeparator(),
		channelTabs,
	)
	controls := widget.NewAccordion(
		widget.NewAccordionItem("Options", options),
		widget.NewAccordionItem("Channels", channels),
	)
	controls.MultiOpen = true
	controls.Open(1)

	controlsScroll := container.NewVScroll(controls)
	controlsScroll.SetMinSize(fyne.NewSize(260, 200))

	// Build border containers explicitly so we can swap viewport content for maximize/restore.
	bColors := [4]color.RGBA{
		{100, 149, 237, 255},
		{80, 200, 80, 255},
		{237, 80, 80, 255},
		{220, 220, 220, 255},
	}
	borders := make([]*fyne.Container, 4)
	borderRects := make([]*canvas.Rectangle, 4)
	borderedObjects := func(idx int) []fyne.CanvasObject {
		return []fyne.CanvasObject{
			borderRects[idx],
			container.NewPadded(viewports[idx].container),
		}
	}
	for i := range viewports {
		rect := canvas.NewRectangle(color.Transparent)
		rect.StrokeColor = bColors[i]
		rect.StrokeWidth = 1
		rect.CornerRadius = 6
		borderRects[i] = rect
		borders[i] = container.NewMax(borderedObjects(i)...)
	}

	grid := container.NewGridWithColumns(2, borders[0], borders[1], borders[2], borders[3])

	var split *container.Split
	maximizedIdx := -1

	var restore func()
	var maximize func(idx int)

	restore = func() {
		if maximizedIdx < 0 {
			return
		}
		i := maximizedIdx
		borders[i].Objects = borderedObjects(i)
		borders[i].Refresh()
		maximizedIdx = -1
		split.Trailing = grid
		split.Refresh()
	}

	maximize = func(idx int) {
		if maximizedIdx >= 0 {
			i := maximizedIdx
			borders[i].Objects = borderedObjects(i)
			borders[i].Refresh()
		}
		maximizedIdx = idx
		if idx >= 0 && idx < 3 {
			channelTabs.SetActive(idx)
		}
		borders[idx].Objects = []fyne.CanvasObject{borderRects[idx]}
		borders[idx].Refresh()

		restoreBar := container.NewHBox(
			newCompactBtn("Restore", func() { restore() }),
		)
		split.Trailing = container.NewBorder(restoreBar, nil, nil, nil, viewports[idx].container)
		split.Refresh()
	}

	for i := range viewports {
		i := i
		viewports[i].actionRow.Objects = append(viewports[i].actionRow.Objects,
			newCompactBtn("Max", func() { maximize(i) }),
			hpad(20),
		)
		viewports[i].actionRow.Refresh()
	}

	split = container.NewHSplit(controlsScroll, grid)
	split.SetOffset(0.32)
	return split, []*fyne.Menu{fileMenu, composeMenu, viewMenu}
}

// loadImagesFromPath loads all SCI extensions from a FITS file as separate LoadedImage values.
// For multi-chip files (e.g. HST FLC), this returns one entry per SCI extension.
// Falls back to the first HDU if no SCI extensions are found.
func loadImagesFromPath(path string) (results []*models.LoadedImage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("fatal crash intercepted: %v", r)
		}
	}()

	file, loadErr := fitsio.LoadFile(path)
	if loadErr != nil {
		return nil, loadErr
	}

	sciHDUs := file.SelectSCI()
	if len(sciHDUs) == 0 {
		sciHDUs = []fitsio.HDU{file.HDUs[0]}
	}

	primary := file.HDUs[0].Header
	for _, hdu := range sciHDUs {
		if cleaned, cleanErr := cleanHDUWithDQ(hdu, file); cleanErr == nil {
			hdu = cleaned
		}
		minV, maxV := processing.AutoLevels(hdu.Data.Pixels)
		median, sigma := processing.EstimateBackground(hdu.Data.Pixels)
		peak := median + 10*sigma
		if peak > maxV {
			peak = maxV
		}
		results = append(results, &models.LoadedImage{
			Path:       path,
			HDU:        hdu,
			Primary:    primary,
			Mode:       stretch.Linear,
			Black:      minV,
			White:      maxV,
			Background: median,
			Peak:       peak,
			ScaledPeak: 10,
			ShowClip:   true,
		})
	}
	return results, nil
}

func loadImageFromPath(path string) (*models.LoadedImage, error) {
	imgs, err := loadImagesFromPath(path)
	if err != nil {
		return nil, err
	}
	return imgs[0], nil
}

func loadLargeComposeImage(path string, store *composeLargeStore, slot string) (*models.LoadedImage, *image.RGBA, composeArtifactDescriptor, error) {
	name, extver := "SCI", ""
	primary, hdu, err := fitsio.InspectSelectedHDU(path, name, extver)
	if err != nil {
		name = ""
		primary, hdu, err = fitsio.InspectSelectedHDU(path, name, extver)
	}
	if err != nil {
		return nil, nil, composeArtifactDescriptor{}, err
	}
	d, err := store.ReplaceFromFITS(slot, path, name, extver)
	if err != nil {
		return nil, nil, composeArtifactDescriptor{}, err
	}
	preview, minV, maxV, err := composeLargePreview(d.Path)
	if err != nil {
		_, _ = store.RemoveSlotIfCurrent(d)
		return nil, nil, composeArtifactDescriptor{}, err
	}
	img := &models.LoadedImage{Path: path, Primary: primary, HDU: hdu, Mode: stretch.Linear, Black: float64(minV), White: float64(maxV), Peak: float64(maxV), ScaledPeak: 10, ShowClip: true}
	img.HDU.Data.Pixels = nil
	return img, preview, d, nil
}

// stageComposeLargeReset restores source pixels while retaining the current
// stretch/alignment state. Every source and preview is prepared before the
// store publishes any replacement.
func stageComposeLargeReset(ctx context.Context, store *composeLargeStore, imgs []*models.LoadedImage, expected []composeArtifactDescriptor) ([]composeArtifactDescriptor, []*image.RGBA, []fitsio.HDU, []fitsio.Header, error) {
	if len(expected) != len(imgs) {
		return nil, nil, nil, nil, errors.New("reset channel count mismatch")
	}
	sizes := make([][2]int, len(imgs))
	hdus := make([]fitsio.HDU, len(imgs))
	primaries := make([]fitsio.Header, len(imgs))
	writes := make([]func(*fitsio.Float32Artifact) error, len(imgs))
	for i, img := range imgs {
		if img == nil {
			return nil, nil, nil, nil, fmt.Errorf("missing channel %d", i+1)
		}
		name := img.HDU.ExtName
		extver := fitsio.HeaderString(img.HDU.Header, "EXTVER")
		primary, hdu, err := fitsio.InspectSelectedHDU(img.Path, name, extver)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		turns := ((img.Rotation90 % 4) + 4) % 4
		w, h := hdu.Data.Width, hdu.Data.Height
		if turns%2 != 0 {
			w, h = h, w
		}
		sizes[i] = [2]int{w, h}
		hdus[i], primaries[i] = hdu, primary
		slot := expected[i].Slot
		writes[i] = func(out *fitsio.Float32Artifact) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			raw := filepath.Join(store.root, fmt.Sprintf("%s-reset-source.tmp", slot))
			defer os.Remove(raw)
			if err := fitsio.CopySelectedHDUToRawFloat32Artifact(img.Path, name, extver, raw); err != nil {
				return err
			}
			src, err := fitsio.OpenFloat32ArtifactReadOnly(raw)
			if err != nil {
				return err
			}
			defer src.Close()
			current := src
			var intermediates []*fitsio.Float32Artifact
			var intermediatePaths []string
			defer func() {
				for _, a := range intermediates {
					_ = a.Close()
				}
				for _, p := range intermediatePaths {
					_ = os.Remove(p)
				}
			}()
			for turn := 0; turn < turns; turn++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				last := turn == turns-1
				if last {
					row := make([]float32, current.Height)
					for y := 0; y < current.Width; y++ {
						if err := current.ReadRowRotatedCW(y, row); err != nil {
							return err
						}
						if err := out.WriteRow(y, row); err != nil {
							return err
						}
					}
					continue
				}
				p := filepath.Join(store.root, fmt.Sprintf("%s-reset-turn-%d.tmp", slot, turn))
				_ = os.Remove(p)
				intermediatePaths = append(intermediatePaths, p)
				next, err := fitsio.CreateFloat32Artifact(p, current.Height, current.Width)
				if err != nil {
					return err
				}
				intermediates = append(intermediates, next)
				row := make([]float32, current.Height)
				for y := 0; y < current.Width; y++ {
					if err := current.ReadRowRotatedCW(y, row); err != nil {
						return err
					}
					if err := next.WriteRow(y, row); err != nil {
						return err
					}
				}
				if err := next.Sync(); err != nil {
					return err
				}
				current = next
			}
			if turns == 0 {
				row := make([]float32, current.Width)
				for y := 0; y < current.Height; y++ {
					if err := current.ReadRow(y, row); err != nil {
						return err
					}
					if err := out.WriteRow(y, row); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	var previews []*image.RGBA
	descs, err := store.ReplaceManyIfCurrentSized(expected, sizes, writes, func(staged []composeArtifactDescriptor) error {
		previews = make([]*image.RGBA, len(staged))
		for i, d := range staged {
			if err := ctx.Err(); err != nil {
				return err
			}
			clone := *imgs[i]
			clone.HDU.Data.Width, clone.HDU.Data.Height = d.Width, d.Height
			clone.HDU.Data.Pixels = nil
			var e error
			previews[i], _, _, e = composeLargeStretchedPreview(d.Path, &clone)
			if e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return descs, previews, hdus, primaries, nil
}

func channelStateFromImage(img *models.LoadedImage) models.ChannelState {
	return models.ChannelState{
		Path:       img.Path,
		Mode:       modeToLabel(img.Mode),
		Black:      img.Black,
		White:      img.White,
		Background: img.Background,
		Peak:       img.Peak,
		ScaledPeak: img.ScaledPeak,
		ShowClip:   img.ShowClip,
		Rotation90: img.Rotation90,

		HasAlign: img.HasAlignTransform,
		AlignA:   img.AlignA,
		AlignB:   img.AlignB,
		AlignC:   img.AlignC,
		AlignD:   img.AlignD,
		AlignE:   img.AlignE,
		AlignF:   img.AlignF,

		AsinhScale:  img.AsinhScale,
		MTFMidtone:  img.MTFMidtone,
		GHSStretch:  img.GHSStretch,
		GHSLocal:    img.GHSLocal,
		GHSSymmetry: img.GHSSymmetry,
	}
}

func applyChannelState(idx int, state models.ChannelState, imgs []*models.LoadedImage, views []*viewport, controls []*models.ChannelControl) {
	img := imgs[idx]
	if img == nil {
		return
	}
	img.Mode = labelToMode(state.Mode)
	img.Black = state.Black
	img.White = state.White
	img.Background = state.Background
	img.Peak = state.Peak
	img.ScaledPeak = state.ScaledPeak
	img.ShowClip = state.ShowClip

	// Restore the full star-alignment affine (applied underneath the Manual Offset
	// at render time). A freshly loaded/reset channel carries HasAlign=false.
	img.HasAlignTransform = state.HasAlign
	img.AlignA, img.AlignB, img.AlignC = state.AlignA, state.AlignB, state.AlignC
	img.AlignD, img.AlignE, img.AlignF = state.AlignD, state.AlignE, state.AlignF
	img.AsinhScale = state.AsinhScale
	img.MTFMidtone = state.MTFMidtone
	img.GHSStretch = state.GHSStretch
	img.GHSLocal = state.GHSLocal
	img.GHSSymmetry = state.GHSSymmetry

	controls[idx].ModeSelect.SetSelected(modeToLabel(img.Mode))
	controls[idx].BackgroundEntry.SetValue(img.Background)
	controls[idx].PeakEntry.SetValue(img.Peak)
	controls[idx].ScaledPeakEntry.SetValue(img.ScaledPeak)
	setStretchParamEntries(controls[idx], img)
	controls[idx].ShowClip.SetChecked(img.ShowClip)

	// Manual Offset fields are the source of truth for placement. Restore them from
	// the state (saved projects carry offsets); a freshly loaded/reset channel has
	// zero offsets in its state, so no stale shift is carried over.
	if controls[idx].XOffsetEntry != nil {
		controls[idx].XOffsetEntry.SetValue(state.OffsetX)
	}
	if controls[idx].YOffsetEntry != nil {
		controls[idx].YOffsetEntry.SetValue(state.OffsetY)
	}
	if controls[idx].RotOffsetEntry != nil {
		controls[idx].RotOffsetEntry.SetValue(state.OffsetRot)
	}

	views[idx].blackBox.SetValue(img.Black)
	views[idx].whiteBox.SetValue(img.White)
}

// tapShield is a transparent overlay that swallows taps. It is placed on top
// of a freshly opened floating window's content for a couple of frames so a
// widget.Select can't be tapped before Fyne's canvas cache is populated by
// the first paint pass (see openOverlayLayerWindow).
type tapShield struct {
	widget.BaseWidget
}

func newTapShield() *tapShield {
	s := &tapShield{}
	s.ExtendBaseWidget(s)
	return s
}

func (s *tapShield) Tapped(*fyne.PointEvent) {}

func (s *tapShield) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

type magicStretchSnapshot struct {
	Background, Peak, Black, White, MTFMidtone float64
	Mode                                       stretch.Mode
}

// snapshotLargeLoadedImage copies the small, immutable channel state needed by
// a background large-file job. Pixel buffers are deliberately detached because
// the artifact is materialized separately by the worker.
func snapshotLargeLoadedImage(img *models.LoadedImage) models.LoadedImage {
	if img == nil {
		return models.LoadedImage{}
	}
	snapshot := *img
	snapshot.HDU = img.HDU
	snapshot.HDU.Header.Cards = cloneHeaderCards(img.HDU.Header.Cards)
	snapshot.HDU.Data.Pixels = nil
	snapshot.HDU.Data.Int32Pixels = nil
	snapshot.Primary.Cards = cloneHeaderCards(img.Primary.Cards)
	return snapshot
}

func cloneHeaderCards(cards map[string]string) map[string]string {
	if cards == nil {
		return nil
	}
	clone := make(map[string]string, len(cards))
	for key, value := range cards {
		clone[key] = value
	}
	return clone
}

func snapshotMagicStretch(img *models.LoadedImage) magicStretchSnapshot {
	return magicStretchSnapshot{img.Background, img.Peak, img.Black, img.White, img.MTFMidtone, img.Mode}
}
func magicStretchUnchanged(img *models.LoadedImage, s magicStretchSnapshot) bool {
	return snapshotMagicStretch(img) == s
}
func installMagicStretch(dst *models.LoadedImage, src models.LoadedImage) {
	dst.Background, dst.Peak, dst.Black, dst.White, dst.MTFMidtone, dst.Mode = src.Background, src.Peak, src.Black, src.White, src.MTFMidtone, src.Mode
}

type largeChannelRuntime struct {
	store       *composeLargeStore
	artifacts   map[int]composeArtifactDescriptor
	previews    map[int]*image.RGBA
	mu          *sync.RWMutex
	jobMu       sync.Mutex
	syncWidgets map[int]func(*models.LoadedImage)
	refresh     func()
}

func composeLargeStretchedPreview(path string, img *models.LoadedImage) (*image.RGBA, float32, float32, error) {
	a, err := fitsio.OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer a.Close()
	step := 1
	if a.Width > 1600 || a.Height > 1600 {
		if a.Width > a.Height {
			step = (a.Width + 1599) / 1600
		} else {
			step = (a.Height + 1599) / 1600
		}
	}
	pw, ph := (a.Width+step-1)/step, (a.Height+step-1)/step
	out := image.NewRGBA(image.Rect(0, 0, pw, ph))
	var eqVals []float32
	if img.Mode == stretch.HistEq {
		eqVals = make([]float32, pw*ph)
	}
	eqHist := make([]int, 256)
	row := make([]float32, a.Width)
	var min, max float32
	first := true
	for y := 0; y < a.Height; y++ {
		if err := a.ReadRow(y, row); err != nil {
			return nil, 0, 0, err
		}
		for _, v := range row {
			if !isFiniteLarge(v) {
				continue
			}
			if first || v < min {
				min = v
			}
			if first || v > max {
				max = v
			}
			first = false
		}
		if y%step != 0 {
			continue
		}
		py := y / step
		for x := 0; x < a.Width; x += step {
			v := row[x]
			q := float64(processing.DiskStretchPreviewValue(v, *img))
			if eqVals != nil {
				eqVals[py*pw+x/step] = float32(q)
			} else {
				c := uint8(q*255 + 0.5)
				out.SetRGBA(x/step, py, color.RGBA{c, c, c, 255})
			}
		}
	}
	if eqVals != nil {
		for _, v := range eqVals {
			eqHist[int(v*255)]++
		}
		total := 0
		for _, n := range eqHist {
			total += n
		}
		sum := 0
		cdf := make([]float32, 256)
		for i, n := range eqHist {
			sum += n
			if total > 0 {
				cdf[i] = float32(float64(sum) / float64(total))
			}
		}
		for i, v := range eqVals {
			c := uint8(cdf[int(v*255)]*255 + 0.5)
			out.SetRGBA(i%pw, i/pw, color.RGBA{c, c, c, 255})
		}
	}
	if first {
		min, max = 0, 1
	}
	return out, min, max, nil
}

func largeArtifactRange(path string) (float32, float32, error) {
	a, err := fitsio.OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		return 0, 0, err
	}
	defer a.Close()
	row := make([]float32, a.Width)
	var min, max float32
	first := true
	for y := 0; y < a.Height; y++ {
		if err := a.ReadRow(y, row); err != nil {
			return 0, 0, err
		}
		for _, v := range row {
			if !isFiniteLarge(v) {
				continue
			}
			if first || v < min {
				min = v
			}
			if first || v > max {
				max = v
			}
			first = false
		}
	}
	if first {
		return 0, 1, nil
	}
	return min, max, nil
}

type largeStretchSummary struct {
	median, high                   float64
	black, white, background, peak float64
	cores                          []float64
	// coreAnchors are retained as a compact catalog so target channels can
	// locate the corresponding star before sampling its core value.  Keeping
	// coordinates (rather than a second pixel plane) preserves the one-plane
	// large-file invariant.
	coreAnchors []processing.Star
}

// largeArtifactStretchSummary deliberately materializes only the channel being
// summarized. It mirrors the normal helper's finite-value percentiles and
// SmartLevels exactly, then releases the plane before the next channel pass.
func largeArtifactStretchSummary(path string, img *models.LoadedImage, starCores bool) (largeStretchSummary, error) {
	return largeArtifactStretchSummaryWithAnchors(path, img, starCores, nil, 0, 0)
}

func largeArtifactStretchSummaryWithAnchors(path string, img *models.LoadedImage, starCores bool, refAnchors []processing.Star, refWidth, refHeight int) (largeStretchSummary, error) {
	lease, err := fitsio.MaterializeFloat32ArtifactLease(path)
	if err != nil {
		return largeStretchSummary{}, err
	}
	defer lease.Release()
	s := largeStretchSummary{}
	var ok bool
	if s.median, ok = composePercentile(lease.Pixels, 50); !ok {
		return s, fmt.Errorf("channel has no finite pixels")
	}
	if s.high, ok = composePercentile(lease.Pixels, 99.8); !ok {
		return s, fmt.Errorf("channel high anchor unavailable")
	}
	s.black, s.white, s.background, s.peak = processing.SmartLevels(lease.Pixels)
	if starCores && img != nil {
		white := img.White
		if white <= img.Black {
			white = img.Peak
		}
		stars := processing.ExtractAndLimitStars(lease.Pixels, lease.Width, lease.Height, 4, 5, 64)
		if refAnchors == nil {
			// Reference pass: retain only compact coordinates and the sampled
			// finite core values. The source plane is released on return.
			for _, star := range stars {
				x, y := int(math.Round(star.X)), int(math.Round(star.Y))
				if x < 0 || y < 0 || x >= lease.Width || y >= lease.Height {
					continue
				}
				v := float64(lease.Pixels[y*lease.Width+x])
				if !isFinite64(v) || (white > img.Black && v >= white*0.98) {
					continue
				}
				s.cores = append(s.cores, v)
				star.Flux = v
				s.coreAnchors = append(s.coreAnchors, star)
			}
		} else {
			// Target pass: map target detections into reference coordinates and
			// use the shared catalog matcher. This avoids assuming the two
			// channels have identical dimensions or perfectly coincident stars.
			sx, sy := 1.0, 1.0
			if refWidth > 0 && refHeight > 0 {
				sx = float64(refWidth) / float64(lease.Width)
				sy = float64(refHeight) / float64(lease.Height)
			}
			targetRef := make([]processing.Star, 0, len(stars))
			for _, star := range stars {
				star.X *= sx
				star.Y *= sy
				targetRef = append(targetRef, star)
			}
			pairs := processing.MatchStars(refAnchors, targetRef, 40, 0.02)
			for _, pair := range pairs {
				if len(s.cores) >= 64 {
					break
				}
				x, y := int(math.Round(pair.TargetX/sx)), int(math.Round(pair.TargetY/sy))
				if x < 0 || y < 0 || x >= lease.Width || y >= lease.Height {
					continue
				}
				v := float64(lease.Pixels[y*lease.Width+x])
				if !isFinite64(v) || (white > img.Black && v >= white*0.98) {
					continue
				}
				s.cores = append(s.cores, v)
			}
		}
	}
	sort.Float64s(s.cores)
	return s, nil
}

func applyLargeStretchMatch(dst *models.ChannelState, ref *models.LoadedImage, rs, ts largeStretchSummary) error {
	if dst == nil || ref == nil {
		return fmt.Errorf("missing channel state")
	}
	rh, th := rs.high, ts.high
	if len(rs.cores) >= 5 && len(ts.cores) >= 5 {
		rh, th = composePercentileSorted(rs.cores, 75), composePercentileSorted(ts.cores, 75)
	}
	sp := ref.ScaledPeak
	if !isFinite64(sp) || sp <= 0 {
		sp = 10
	}
	a := composeScaledInput(rs.median, ref.Background, ref.Peak, sp) / sp
	b := composeScaledInput(rh, ref.Background, ref.Peak, sp) / sp
	if !isFinite64(a) || !isFinite64(b) || math.Abs(b-a) < 1e-9 || th <= ts.median {
		dst.Black, dst.White, dst.Background, dst.Peak = ts.black, ts.white, ts.background, ts.peak
	} else {
		denom := (th - ts.median) / (b - a)
		bg, peak := ts.median-a*denom, ts.median-a*denom+denom
		if !isFinite64(bg) || !isFinite64(peak) || peak <= bg {
			dst.Black, dst.White, dst.Background, dst.Peak = ts.black, ts.white, ts.background, ts.peak
		} else {
			dst.Black, dst.White, dst.Background, dst.Peak = bg, peak, bg, peak
		}
	}
	dst.Mode, dst.ScaledPeak, dst.ShowClip = modeToLabel(ref.Mode), sp, ref.ShowClip
	return nil
}

func applyChannelStateToImage(img *models.LoadedImage, st models.ChannelState) {
	if img == nil {
		return
	}
	img.Mode = labelToMode(st.Mode)
	img.Black, img.White = st.Black, st.White
	img.Background, img.Peak, img.ScaledPeak, img.ShowClip = st.Background, st.Peak, st.ScaledPeak, st.ShowClip
	img.AsinhScale, img.MTFMidtone = st.AsinhScale, st.MTFMidtone
	img.GHSStretch, img.GHSLocal, img.GHSSymmetry = st.GHSStretch, st.GHSLocal, st.GHSSymmetry
}

// largeArtifactStars extracts a bounded catalog while materializing only this
// one channel. The catalog is retained; source pixels are released before the
// next channel is opened.
func largeArtifactStars(path string) ([]processing.Star, error) {
	lease, err := fitsio.MaterializeFloat32ArtifactLease(path)
	if err != nil {
		return nil, err
	}
	stars := processing.ExtractAndLimitStars(lease.Pixels, lease.Width, lease.Height, 4.0, 3, 30)
	lease.Release()
	return stars, nil
}

func largeRotateChannel(rt *largeChannelRuntime, idx int, imgs []*models.LoadedImage, refresh func(), onCommit func()) {
	rt.mu.RLock()
	d, ok := rt.artifacts[idx]
	var previewImg models.LoadedImage
	if idx >= 0 && idx < len(imgs) && imgs[idx] != nil {
		previewImg = *imgs[idx]
		previewImg.HDU = imgs[idx].HDU
		previewImg.HDU.Header.Cards = make(map[string]string, len(imgs[idx].HDU.Header.Cards))
		for key, value := range imgs[idx].HDU.Header.Cards {
			previewImg.HDU.Header.Cards[key] = value
		}
		previewImg.HDU.Data.Pixels = nil
		previewImg.HDU.Data.Int32Pixels = nil
	}
	rt.mu.RUnlock()
	if !ok || rt.store == nil {
		return
	}
	go func() {
		rt.jobMu.Lock()
		defer rt.jobMu.Unlock()
		rt.mu.RLock()
		cur, current := rt.artifacts[idx]
		rt.mu.RUnlock()
		if !current || cur.Generation != d.Generation || cur.Path != d.Path {
			fyne.Do(refresh)
			return
		}
		src, err := fitsio.OpenFloat32ArtifactReadOnly(d.Path)
		if err != nil {
			fyne.Do(refresh)
			return
		}
		w, h := src.Width, src.Height
		nd, err := rt.store.ReplaceIfCurrent(d, h, w, func(out *fitsio.Float32Artifact) error {
			row := make([]float32, w)
			dst := make([]float32, h)
			for y := 0; y < w; y++ {
				for x := 0; x < h; x++ {
					if err := src.ReadRow(h-1-x, row); err != nil {
						return err
					}
					dst[x] = row[y]
				}
				if err := out.WriteRow(y, dst); err != nil {
					return err
				}
			}
			return nil
		})
		src.Close()
		if err == nil {
			previewImg.HDU.Data.Width, previewImg.HDU.Data.Height = h, w
			preview, _, _, pErr := composeLargeStretchedPreview(nd.Path, &previewImg)
			err = pErr
			if err == nil {
				rt.mu.Lock()
				cur, current := rt.artifacts[idx]
				if current && cur.Generation == d.Generation {
					rt.artifacts[idx] = nd
					rt.previews[idx] = preview
					imgs[idx].HDU.Data.Width, imgs[idx].HDU.Data.Height = h, w
					imgs[idx].HDU.Data.Pixels = nil
					imgs[idx].Rotation90 = (imgs[idx].Rotation90 + 1) % 4
					clearComposeChannelAlignment(imgs[idx])
				}
				rt.mu.Unlock()
			}
		}
		fyne.Do(func() {
			if err == nil && onCommit != nil {
				onCommit()
			}
			refresh()
		})
	}()
}

func channelControls(label string, col color.Color, idx int, imgs []*models.LoadedImage, origPixels *[][]float32, views []*viewport, refresh func(), magicPreset *widget.Select, allowRotate bool, large ...*largeChannelRuntime) *models.ChannelControl {
	invalidateChannelRefinement := func() {
		if idx == 1 && globalComposeGaiaRefinementInvalidate != nil {
			globalComposeGaiaRefinementInvalidate()
		}
	}
	var disk *largeChannelRuntime
	if len(large) > 0 {
		disk = large[0]
	}
	largeJob := func(op string, mutate func(*models.LoadedImage) error) {
		if disk == nil || disk.store == nil || !composeLargeModeActive() || idx < 0 || idx >= len(imgs) {
			return
		}
		disk.mu.RLock()
		d, ok := disk.artifacts[idx]
		identity := (*models.LoadedImage)(nil)
		var snapshot models.LoadedImage
		if ok && idx < len(imgs) && imgs[idx] != nil {
			identity = imgs[idx]
			snapshot = snapshotLargeLoadedImage(identity)
		} else {
			ok = false
		}
		disk.mu.RUnlock()
		if !ok {
			return
		}
		if idx == 1 && globalComposeGaiaRefinementInvalidate != nil {
			globalComposeGaiaRefinementInvalidate()
		}
		go func() {
			disk.jobMu.Lock()
			defer disk.jobMu.Unlock()
			lease, err := fitsio.MaterializeFloat32ArtifactLease(d.Path)
			if err == nil {
				img := snapshot
				img.HDU.Data.Pixels = lease.Pixels
				err = mutate(&img)
				lease.Release()
				if err == nil {
					preview, _, _, pErr := composeLargeStretchedPreview(d.Path, &img)
					err = pErr
					if err == nil {
						disk.mu.Lock()
						cur, current := disk.artifacts[idx]
						storeCur, storeCurrent := disk.store.Descriptor(d.Slot)
						if current && storeCurrent && imgs[idx] == identity && cur.Generation == d.Generation && cur.Path == d.Path && storeCur.Generation == d.Generation && storeCur.Path == d.Path {
							*imgs[idx] = img
							imgs[idx].HDU.Data.Pixels = nil
							disk.previews[idx] = preview
						} else {
							err = errors.New("stale Compose artifact generation")
						}
						disk.mu.Unlock()
					}
				}
			}
			fyne.Do(func() {
				if err != nil {
					debuglog.Log(fmt.Sprintf("large channel %s: %v", op, err))
				} else if idx < len(views) && views[idx] != nil {
					views[idx].blackBox.SetValue(imgs[idx].Black)
					views[idx].whiteBox.SetValue(imgs[idx].White)
				}
				if err == nil && disk.syncWidgets != nil {
					if syncFn := disk.syncWidgets[idx]; syncFn != nil {
						syncFn(imgs[idx])
					}
				}
				refresh()
			})
		}()
	}
	// Stretch-specific parameter rows. Only the row(s) relevant to the selected
	// mode are shown; the rest stay hidden to avoid clutter.
	asinhScaleEntry := NewNumberEntry(0.1, 3)
	mtfMidtoneEntry := NewNumberEntry(0.01, 3)
	ghsStretchEntry := NewNumberEntry(0.1, 2)
	ghsLocalEntry := NewNumberEntry(0.1, 2)
	ghsSymmetryEntry := NewNumberEntry(0.05, 3)

	asinhScaleEntry.SetValue(stretch.DefaultAsinhScale)
	mtfMidtoneEntry.SetValue(stretch.DefaultMTFMidtone)
	ghsStretchEntry.SetValue(stretch.DefaultGHSStretch)
	ghsLocalEntry.SetValue(stretch.DefaultGHSLocal)
	ghsSymmetryEntry.SetValue(stretch.DefaultGHSSymmetry)

	paramRow := func(label string, entry models.NumberField) *fyne.Container {
		return container.NewBorder(nil, nil, widget.NewLabel(label), nil, entry)
	}
	asinhRow := paramRow("Asinh softening", asinhScaleEntry)
	mtfRow := paramRow("MTF midtone", mtfMidtoneEntry)
	ghsDRow := paramRow("GHS strength D", ghsStretchEntry)
	ghsBRow := paramRow("GHS local b", ghsLocalEntry)
	ghsSPRow := paramRow("GHS symmetry SP", ghsSymmetryEntry)

	updateStretchParams := func(mode stretch.Mode) {
		asinhRow.Hide()
		mtfRow.Hide()
		ghsDRow.Hide()
		ghsBRow.Hide()
		ghsSPRow.Hide()
		switch mode {
		case stretch.Asinh:
			asinhRow.Show()
		case stretch.MTF:
			mtfRow.Show()
		case stretch.GHS:
			ghsDRow.Show()
			ghsBRow.Show()
			ghsSPRow.Show()
		}
	}

	var syncingWidgets bool
	selectBox := widget.NewSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq", "MTF", "GHS"}, func(value string) {
		if syncingWidgets {
			updateStretchParams(labelToMode(value))
			return
		}
		if imgs[idx] == nil {
			updateStretchParams(labelToMode(value))
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			mode := labelToMode(value)
			updateStretchParams(mode)
			largeJob("mode", func(img *models.LoadedImage) error { img.Mode = mode; return nil })
			return
		}
		imgs[idx].Mode = labelToMode(value)
		updateStretchParams(imgs[idx].Mode)
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeJob("mode", func(_ *models.LoadedImage) error { return nil })
			return
		}
		refresh()
	})
	initialMode := stretch.Linear
	if idx >= 0 && idx < len(imgs) && imgs[idx] != nil {
		initialMode = imgs[idx].Mode
	}
	selectBox.SetSelected(modeToLabel(initialMode))

	backgroundEntry := NewNumberEntry(0.001, 4)
	peakEntry := NewNumberEntry(0.001, 4)
	scaledPeakEntry := NewNumberEntry(1, 1)

	backgroundEntry.SetValue(0)
	peakEntry.SetValue(1)
	scaledPeakEntry.SetValue(1)

	// Lock buttons: when engaged, the "Black" level and "Background level" fields
	// mirror each other (and likewise "White"/"Peak level"), so editing one input
	// updates the other. Locking is per-channel and only affects future edits; the
	// syncing guards prevent the paired SetValue from recursing back.
	var blackBgLocked, whitePeakLocked bool
	var syncingBlackBg, syncingWhitePeak bool

	blackBgLockBtn := widget.NewButton("Lock", nil)
	whitePeakLockBtn := widget.NewButton("Lock", nil)
	blackBgLockBtn.Importance = widget.LowImportance
	whitePeakLockBtn.Importance = widget.LowImportance

	setLockAppearance := func(btn *widget.Button, locked bool) {
		if locked {
			btn.SetText("Locked")
			btn.Importance = widget.HighImportance
		} else {
			btn.SetText("Lock")
			btn.Importance = widget.LowImportance
		}
		btn.Refresh()
	}
	blackBgLockBtn.OnTapped = func() {
		blackBgLocked = !blackBgLocked
		setLockAppearance(blackBgLockBtn, blackBgLocked)
	}
	whitePeakLockBtn.OnTapped = func() {
		whitePeakLocked = !whitePeakLocked
		setLockAppearance(whitePeakLockBtn, whitePeakLocked)
	}

	backgroundEntry.OnChanged = func(v float64) {
		if !blackBgLocked || syncingBlackBg {
			return
		}
		syncingBlackBg = true
		views[idx].blackBox.SetValue(v)
		syncingBlackBg = false
	}
	views[idx].blackBox.OnChanged = func(v float64) {
		if !blackBgLocked || syncingBlackBg {
			return
		}
		syncingBlackBg = true
		backgroundEntry.SetValue(v)
		syncingBlackBg = false
	}
	peakEntry.OnChanged = func(v float64) {
		if !whitePeakLocked || syncingWhitePeak {
			return
		}
		syncingWhitePeak = true
		views[idx].whiteBox.SetValue(v)
		syncingWhitePeak = false
	}
	views[idx].whiteBox.OnChanged = func(v float64) {
		if !whitePeakLocked || syncingWhitePeak {
			return
		}
		syncingWhitePeak = true
		peakEntry.SetValue(v)
		syncingWhitePeak = false
	}

	showClip := NewToggle(func(v bool) {
		if imgs[idx] == nil {
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeJob("show-clip", func(img *models.LoadedImage) error { img.ShowClip = v; return nil })
			return
		}
		imgs[idx].ShowClip = v
		refresh()
	})
	showClip.SetChecked(true)

	var apply *widget.Button
	apply = widget.NewButton("Apply", func() {
		if imgs[idx] == nil {
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			background, peak, scaled, black, white := backgroundEntry.Value(), peakEntry.Value(), scaledPeakEntry.Value(), views[idx].blackBox.Value(), views[idx].whiteBox.Value()
			asinh, mtfm, ghsd, ghsb, ghssp := asinhScaleEntry.Value(), mtfMidtoneEntry.Value(), ghsStretchEntry.Value(), ghsLocalEntry.Value(), ghsSymmetryEntry.Value()
			largeJob("apply", func(img *models.LoadedImage) error {
				img.Background, img.Peak, img.ScaledPeak, img.Black, img.White = background, peak, scaled, black, white
				img.AsinhScale, img.MTFMidtone, img.GHSStretch, img.GHSLocal, img.GHSSymmetry = asinh, mtfm, ghsd, ghsb, ghssp
				return nil
			})
			return
		}
		imgs[idx].Background = backgroundEntry.Value()
		imgs[idx].Peak = peakEntry.Value()
		imgs[idx].ScaledPeak = scaledPeakEntry.Value()
		imgs[idx].AsinhScale = asinhScaleEntry.Value()
		imgs[idx].MTFMidtone = mtfMidtoneEntry.Value()
		imgs[idx].GHSStretch = ghsStretchEntry.Value()
		imgs[idx].GHSLocal = ghsLocalEntry.Value()
		imgs[idx].GHSSymmetry = ghsSymmetryEntry.Value()
		imgs[idx].Black = views[idx].blackBox.Value()
		imgs[idx].White = views[idx].whiteBox.Value()
		apply.SetText("Working…")
		apply.Disable()
		go func() {
			time.Sleep(50 * time.Millisecond) // let Fyne paint "Working…" before blocking main thread
			fyne.Do(func() {
				refresh()
				apply.SetText("Apply")
				apply.Enable()
			})
		}()
	})
	apply.Importance = widget.HighImportance

	auto := widget.NewButton("Auto scaling", func() {
		if imgs[idx] == nil {
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeJob("auto scaling", func(img *models.LoadedImage) error { processing.AutoScaleLikeFitsLiberator(img); return nil })
			return
		}
		processing.AutoScaleLikeFitsLiberator(imgs[idx])
		views[idx].blackBox.SetValue(imgs[idx].Black)
		views[idx].whiteBox.SetValue(imgs[idx].White)
		backgroundEntry.SetValue(imgs[idx].Background)
		peakEntry.SetValue(imgs[idx].Peak)
		scaledPeakEntry.SetValue(imgs[idx].ScaledPeak)
		refresh()
	})

	autoMTF := widget.NewButton("Auto MTF", func() {
		if imgs[idx] == nil {
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeJob("auto MTF", func(img *models.LoadedImage) error { processing.AutoMTFMidtone(img); return nil })
			return
		}
		processing.AutoMTFMidtone(imgs[idx])
		backgroundEntry.SetValue(imgs[idx].Background)
		peakEntry.SetValue(imgs[idx].Peak)
		scaledPeakEntry.SetValue(imgs[idx].ScaledPeak)
		mtfMidtoneEntry.SetValue(imgs[idx].MTFMidtone)
		selectBox.SetSelected("MTF") // also reveals the MTF row and triggers refresh
	})

	magic := widget.NewButton("Magic", func() {
		if imgs[idx] == nil {
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			preset := processing.ParseMagicPreset(magicPreset.Selected)
			largeJob("magic", func(img *models.LoadedImage) error {
				processing.ApplyMagicLevels(img, preset)
				processing.AutoMTFMidtone(img)
				return nil
			})
			return
		}
		res := processing.ApplyMagicLevels(imgs[idx], processing.ParseMagicPreset(magicPreset.Selected))
		processing.AutoMTFMidtone(imgs[idx])
		backgroundEntry.SetValue(imgs[idx].Background)
		peakEntry.SetValue(imgs[idx].Peak)
		views[idx].blackBox.SetValue(imgs[idx].Black)
		views[idx].whiteBox.SetValue(imgs[idx].White)
		mtfMidtoneEntry.SetValue(imgs[idx].MTFMidtone)
		selectBox.SetSelected("MTF") // also reveals the MTF row and triggers refresh
		debuglog.Log(fmt.Sprintf(
			"Magic[%s] ch%d: black=%.4g white=%.4g sky=%.4g sigma=%.4g clipLow=%.3f%% clipHigh=%.3f%% stars=%v(%.2f%%) whiteSrc=%s whiteN=%d(%.2f%%)",
			res.Preset, idx, res.Black, res.White, res.Background, res.Sigma,
			res.ClipLowPercent, res.ClipHighPercent, res.StarsExcluded, res.StarPixelPercent,
			res.WhiteSampleSource, res.WhiteSampleCount, res.WhiteSamplePercent))
		refresh()
	})

	xOffsetEntry := NewNumberEntry(1, 2)
	yOffsetEntry := NewNumberEntry(1, 2)
	rotOffsetEntry := NewNumberEntry(0.1, 1)

	// Manual Offsets are applied at render time (never baked into the pixels), so
	// "Apply Offset" simply re-renders the previews and composite with the current
	// X/Y/Rot field values.
	applyOffset := widget.NewButton("Apply Offset", func() {
		if imgs[idx] == nil {
			return
		}
		if idx == 1 && globalComposeGaiaRefinementInvalidate != nil {
			globalComposeGaiaRefinementInvalidate()
		}
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeJob("offset", func(_ *models.LoadedImage) error { return nil })
			return
		}
		refresh()
	})
	rotate := widget.NewButton("Rotate 90°", func() {
		if imgs[idx] == nil {
			return
		}
		if idx == 1 && globalComposeGaiaRefinementInvalidate != nil {
			globalComposeGaiaRefinementInvalidate()
		}
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeRotateChannel(disk, idx, imgs, refresh, func() {
				xOffsetEntry.SetValue(0)
				yOffsetEntry.SetValue(0)
				rotOffsetEntry.SetValue(0)
				clearComposeOrigPixels(origPixels, idx)
			})
			return
		}
		rotateComposeChannel90CW(imgs[idx])
		clearComposeChannelAlignment(imgs[idx])
		xOffsetEntry.SetValue(0)
		yOffsetEntry.SetValue(0)
		rotOffsetEntry.SetValue(0)
		clearComposeOrigPixels(origPixels, idx)
		refresh()
	})
	if disk != nil && disk.syncWidgets != nil {
		disk.syncWidgets[idx] = func(img *models.LoadedImage) {
			if syncingWidgets || img == nil {
				return
			}
			syncingWidgets = true
			backgroundEntry.SetValue(img.Background)
			peakEntry.SetValue(img.Peak)
			scaledPeakEntry.SetValue(img.ScaledPeak)
			views[idx].blackBox.SetValue(img.Black)
			views[idx].whiteBox.SetValue(img.White)
			mtfMidtoneEntry.SetValue(img.MTFMidtone)
			selectBox.SetSelected(modeToLabel(img.Mode))
			syncingWidgets = false
		}
	}

	return &models.ChannelControl{
		Content: container.NewVBox(
			func() fyne.CanvasObject {
				t := canvas.NewText(label, col)
				t.TextStyle = fyne.TextStyle{Bold: true}
				return t
			}(),
			selectBox,
			widget.NewForm(
				widget.NewFormItem("Background level", container.NewBorder(nil, nil, nil, blackBgLockBtn, backgroundEntry)),
				widget.NewFormItem("Peak level", container.NewBorder(nil, nil, nil, whitePeakLockBtn, peakEntry)),
				widget.NewFormItem("Scaled peak level", scaledPeakEntry),
			),
			asinhRow,
			mtfRow,
			ghsDRow,
			ghsBRow,
			ghsSPRow,
			container.NewHBox(showClip, widget.NewLabel("Show clipped pixels")),
			container.NewHBox(outlinedButton(col, auto), outlinedButton(col, autoMTF), outlinedButton(col, apply)),
			outlinedButton(col, magic),
			func() fyne.CanvasObject {
				if allowRotate {
					return outlinedButton(col, rotate)
				}
				return layout.NewSpacer()
			}(),
			widget.NewLabel("Manual Offset"),
			offsetRow("X", xOffsetEntry),
			offsetRow("Y", yOffsetEntry),
			offsetRow("Rot°", rotOffsetEntry),
			applyOffset,
			widget.NewSeparator(),
		),
		ModeSelect:        selectBox,
		BackgroundEntry:   backgroundEntry,
		PeakEntry:         peakEntry,
		ScaledPeakEntry:   scaledPeakEntry,
		AsinhScaleEntry:   asinhScaleEntry,
		MTFMidtoneEntry:   mtfMidtoneEntry,
		GHSStretchEntry:   ghsStretchEntry,
		GHSLocalEntry:     ghsLocalEntry,
		GHSSymmetryEntry:  ghsSymmetryEntry,
		MagicPresetSelect: magicPreset,
		XOffsetEntry:      xOffsetEntry,
		YOffsetEntry:      yOffsetEntry,
		RotOffsetEntry:    rotOffsetEntry,
		ShowClip:          showClip,
	}
}

type composeViewportPreview struct {
	Image      *image.RGBA
	Bins       [256]int
	HistMax    int
	StatsText  string
	FilterText string
	Black      float64
	White      float64
	OrigW      int
	OrigH      int
}

type composePreviewData struct {
	Views       [4]composeViewportPreview
	RGBStats    [3]histogram.Stats
	BlinkFrames []composeBlinkFrame
	Rendered    *processing.ComposeRenderResult
}

type composeBlinkFrame struct {
	ProjectIndex int
	Name         string
	Preview      composeViewportPreview
}

// buildComposeBlinkFrames prepares all selected Blink sources from the same
// immutable render snapshot used by the compose preview generation. RGB frames
// reuse the already-stretched previews; overlay frames are stretched here in
// the worker goroutine and never from the ticker/UI callback.
func buildComposeBlinkFrames(ctx context.Context, imgs []*models.LoadedImage, sources []composeBlinkSource, selection []int, base [4]composeViewportPreview) []composeBlinkFrame {
	frames := make([]composeBlinkFrame, 0, len(selection))
	for _, projectIndex := range selection {
		if err := composeMagicCanceled(ctx); err != nil {
			return nil
		}
		var source *composeBlinkSource
		for i := range sources {
			if sources[i].ProjectIndex == projectIndex {
				source = &sources[i]
				break
			}
		}
		if source == nil || source.RuntimeIndex < 0 || source.RuntimeIndex >= len(imgs) || imgs[source.RuntimeIndex] == nil {
			continue
		}
		preview := composeViewportPreview{}
		if source.RuntimeIndex < 3 {
			preview = base[source.RuntimeIndex]
		} else {
			data, err := buildComposeOverlayPreviewData(ctx, imgs[source.RuntimeIndex])
			if err != nil {
				return nil
			}
			preview = composeViewportPreview{Image: data.image, Bins: data.bins, StatsText: fmt.Sprintf("Sky %.3f  μ %.3f  σ %.3f", data.sky, data.mean, data.std), FilterText: data.filterText, OrigW: data.width, OrigH: data.height, Black: imgs[source.RuntimeIndex].Black, White: imgs[source.RuntimeIndex].White}
		}
		frames = append(frames, composeBlinkFrame{ProjectIndex: projectIndex, Name: source.Name, Preview: preview})
	}
	return frames
}

func buildComposePreviewData(ctx context.Context, imgs []*models.LoadedImage, sharedHistScale bool, buildComposite bool, levels *models.RgbLevels, composeRGB func(context.Context) ([]byte, int, int, [3]histogram.Stats, error)) composePreviewData {
	start := time.Now()
	defer func() {
		debuglog.Log(fmt.Sprintf("buildComposePreviewData: total took %s", time.Since(start)))
	}()
	var out composePreviewData
	var channelPixels [3][]float32
	var channelStats [3]histogram.Stats
	for i := 0; i < 3; i++ {
		if i >= len(imgs) || imgs[i] == nil {
			out.Views[i] = composeViewportPreview{Image: blankImg(), StatsText: "Sky --  μ --  σ --"}
			continue
		}
		channelStart := time.Now()
		stretched, mask := processing.ApplyStretchParallel(imgs[i])
		stats := histogram.Compute(stretched.Pixels)
		sky, _ := processing.EstimateBackground(stretched.Pixels)
		channelPixels[i] = stretched.Pixels
		channelStats[i] = stats
		out.Views[i] = composeViewportPreview{
			Image:      processing.ToGrayRGBA(stretched, mask),
			Bins:       stats.Hist,
			StatsText:  fmt.Sprintf("Sky %.3f  μ %.3f  σ %.3f", sky, stats.Mean, stats.Std),
			FilterText: fitsio.FilterString(imgs[i].Primary),
			Black:      imgs[i].Black,
			White:      imgs[i].White,
			OrigW:      stretched.Width,
			OrigH:      stretched.Height,
		}
		debuglog.Log(fmt.Sprintf("buildComposePreviewData: channel %d took %s", i+1, time.Since(channelStart)))
	}
	if sharedHistScale {
		sharedBins, sharedMax, ok := buildSharedScaleChannelHistograms(channelPixels, channelStats)
		if ok {
			for i := 0; i < 3; i++ {
				if len(channelPixels[i]) == 0 {
					continue
				}
				out.Views[i].Bins = sharedBins[i]
				out.Views[i].HistMax = sharedMax
			}
		}
	}
	if !buildComposite {
		out.Views[3] = composeViewportPreview{Image: blankImg(), StatsText: "Composite: off"}
		return out
	}

	if ctx.Err() != nil {
		out.Views[3] = composeViewportPreview{Image: blankImg(), StatsText: "Sky --  μ --  σ --"}
		return out
	}
	buf, w, h, rgbStats := processing.ComposeRGB(ctx, imgs)
	if composeRGB != nil {
		if altBuf, altW, altH, altStats, err := composeRGB(ctx); err == nil {
			buf, w, h, rgbStats = altBuf, altW, altH, altStats
		} else {
			debuglog.Log(fmt.Sprintf("buildComposePreviewData: compose failed: %v", err))
		}
	}
	if buf == nil {
		out.Views[3] = composeViewportPreview{Image: blankImg(), StatsText: "Sky --  μ --  σ --"}
		return out
	}
	out.RGBStats = rgbStats
	buf = processing.ApplyRGBLevels(buf, levels)
	lumaStats := histogramRGBLuminance(buf)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	copy(img.Pix, buf)
	out.Views[3] = composeViewportPreview{
		Image:     img,
		Bins:      lumaStats.Hist,
		StatsText: fmt.Sprintf("Luma μ %.1f  σ %.1f", lumaStats.Mean, lumaStats.Std),
		OrigW:     w,
		OrigH:     h,
	}
	return out
}

func histogramRGBLuminance(buf []byte) histogram.Stats {
	var stats histogram.Stats
	if len(buf) == 0 {
		return stats
	}
	var sum float64
	for i := 0; i+3 < len(buf); i += 4 {
		luma := int(math.Round(0.299*float64(buf[i]) + 0.587*float64(buf[i+1]) + 0.114*float64(buf[i+2])))
		if luma < 0 {
			luma = 0
		} else if luma > 255 {
			luma = 255
		}
		stats.Hist[luma]++
		sum += float64(luma)
		stats.Count++
	}
	if stats.Count == 0 {
		return stats
	}
	stats.Min = 0
	stats.Max = 255
	stats.Mean = sum / float64(stats.Count)
	var variance float64
	for i := 0; i+3 < len(buf); i += 4 {
		luma := 0.299*float64(buf[i]) + 0.587*float64(buf[i+1]) + 0.114*float64(buf[i+2])
		diff := luma - stats.Mean
		variance += diff * diff
	}
	stats.Std = math.Sqrt(variance / float64(stats.Count))
	return stats
}

func composeBlinkFilterIndex(name string) int {
	for i, filterName := range composeBlinkFilterNames {
		if name == filterName {
			return i
		}
	}
	return 0
}

func clampComposeBlinkFilter(idx int) int {
	if idx < 0 || idx >= len(composeBlinkFilterNames) {
		return 0
	}
	return idx
}

// overlayLayerPalette seeds the tint of a newly added colored layer. Values are
// starting points the user can freely recolor; the cycle just avoids every new
// layer defaulting to the same hue. First two entries preserve the old
// Orange/Yellow defaults.
var overlayLayerPalette = [][3]uint8{
	{159, 140, 80},  // orange
	{255, 220, 90},  // yellow
	{237, 80, 80},   // red
	{100, 149, 237}, // blue
	{80, 200, 120},  // green
	{200, 110, 200}, // magenta
	{110, 200, 200}, // cyan
	{240, 160, 60},  // amber
}

func defaultOverlayLayerSettings(n int) models.OrangeLayerState {
	c := overlayLayerPalette[n%len(overlayLayerPalette)]
	return models.OrangeLayerState{
		ColorR:           c[0],
		ColorG:           c[1],
		ColorB:           c[2],
		Opacity:          1,
		HighlightProtect: 0.5,
	}
}

func normalizeComposeOverlayState(s models.OrangeLayerState, index int) models.OrangeLayerState {
	if s.ColorR == 0 && s.ColorG == 0 && s.ColorB == 0 && s.Opacity == 0 {
		s = defaultOverlayLayerSettings(index)
	}
	if s.BlinkID == "" {
		s.BlinkID = fmt.Sprintf("overlay-legacy-%d", index)
	}
	return s
}

func composeBlinkPair(excluded int) (int, int) {
	excluded = clampComposeBlinkFilter(excluded)
	pair := [2]int{-1, -1}
	next := 0
	for i := 0; i < 3; i++ {
		if i == excluded {
			continue
		}
		pair[next] = i
		next++
	}
	return pair[0], pair[1]
}

// composeBlinkOverlaySource describes an overlay's sparse runtime slot. The
// project index is assigned later from the stable source enumeration, so a
// removed overlay cannot shift the meaning of a saved selection unexpectedly.
type composeBlinkOverlaySource struct {
	RuntimeIndex int
	Name         string
	Path         string
	BlinkID      string
}

type composeBlinkSource struct {
	Name         string
	Key          string
	RuntimeIndex int
	ProjectIndex int
}

// enumerateComposeBlinkSources returns loaded RGB channels followed by loaded
// overlays in runtime-slot order. ProjectIndex is compact and deterministic;
// RuntimeIndex retains the sparse slot used by the live compose workspace.
func enumerateComposeBlinkSources(imgs []*models.LoadedImage, overlays []composeBlinkOverlaySource) []composeBlinkSource {
	sources := make([]composeBlinkSource, 0, len(imgs))
	for i := 0; i < len(composeBlinkFilterNames) && i < len(imgs); i++ {
		if imgs[i] == nil {
			continue
		}
		sources = append(sources, composeBlinkSource{
			Name:         composeBlinkFilterNames[i],
			Key:          fmt.Sprintf("rgb:%d", i),
			RuntimeIndex: i,
			ProjectIndex: len(sources),
		})
	}
	ordered := append([]composeBlinkOverlaySource(nil), overlays...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].RuntimeIndex < ordered[j].RuntimeIndex })
	for _, overlay := range ordered {
		if overlay.RuntimeIndex < len(composeBlinkFilterNames) || overlay.RuntimeIndex < 0 || overlay.RuntimeIndex >= len(imgs) || imgs[overlay.RuntimeIndex] == nil {
			continue
		}
		name := overlay.Name
		if name == "" {
			name = fmt.Sprintf("Overlay %d", overlay.RuntimeIndex-len(composeBlinkFilterNames)+1)
		}
		key := fmt.Sprintf("overlay-slot:%d", overlay.RuntimeIndex)
		if overlay.BlinkID != "" {
			key = "overlay-id:" + overlay.BlinkID
		}
		sources = append(sources, composeBlinkSource{
			Name:         name,
			Key:          key,
			RuntimeIndex: overlay.RuntimeIndex,
			ProjectIndex: len(sources),
		})
	}
	return sources
}

func resolveComposeBlinkKeys(keys []string, sources []composeBlinkSource) []int {
	byKey := make(map[string]int, len(sources))
	for _, source := range sources {
		if source.Key != "" {
			byKey[source.Key] = source.ProjectIndex
		}
	}
	result := make([]int, 0, len(keys))
	seen := make(map[int]bool)
	for _, key := range keys {
		if index, ok := byKey[key]; ok && !seen[index] {
			result = append(result, index)
			seen[index] = true
		}
	}
	return result
}

// filterComposeBlinkSelection drops stale project indices and duplicate
// entries while preserving the user's ordering.
func filterComposeBlinkSelection(selection []int, sources []composeBlinkSource) []int {
	valid := make(map[int]bool, len(sources))
	for _, source := range sources {
		valid[source.ProjectIndex] = true
	}
	result := make([]int, 0, len(selection))
	seen := make(map[int]bool, len(selection))
	for _, index := range selection {
		if valid[index] && !seen[index] {
			result = append(result, index)
			seen[index] = true
		}
	}
	return result
}

// resolveComposeBlinkSelection applies a saved custom selection, or migrates
// the legacy two-of-three RGB setting when no generalized selection exists.
// New projects default to all currently loaded sources.
func resolveComposeBlinkSelection(sources []composeBlinkSource, custom []int, legacyEnabled bool, legacyExcluded int) []int {
	if custom != nil {
		return filterComposeBlinkSelection(custom, sources)
	}
	if legacyEnabled {
		result := make([]int, 0, len(composeBlinkFilterNames)-1)
		excluded := clampComposeBlinkFilter(legacyExcluded)
		for _, source := range sources {
			if source.RuntimeIndex < len(composeBlinkFilterNames) && source.RuntimeIndex != excluded {
				result = append(result, source.ProjectIndex)
			}
		}
		return result
	}
	result := make([]int, len(sources))
	for i := range sources {
		result[i] = sources[i].ProjectIndex
	}
	return result
}

func composeBlinkRuntimeIndices(selection []int, sources []composeBlinkSource) []int {
	selected := filterComposeBlinkSelection(selection, sources)
	byProject := make(map[int]int, len(sources))
	for _, source := range sources {
		byProject[source.ProjectIndex] = source.RuntimeIndex
	}
	result := make([]int, 0, len(selected))
	for _, index := range selected {
		result = append(result, byProject[index])
	}
	return result
}

// remapComposeBlinkSelection carries a selection across runtime changes (for
// example, removing a sparse overlay slot) by runtime identity. Slots absent
// from the new source list are dropped, so a later slot reuse is not selected.
func remapComposeBlinkSelection(selection []int, oldSources, newSources []composeBlinkSource) []int {
	oldRuntime := composeBlinkRuntimeIndices(selection, oldSources)
	byRuntime := make(map[int]int, len(newSources))
	for _, source := range newSources {
		byRuntime[source.RuntimeIndex] = source.ProjectIndex
	}
	remapped := make([]int, 0, len(oldRuntime))
	for _, runtimeIndex := range oldRuntime {
		if projectIndex, ok := byRuntime[runtimeIndex]; ok {
			remapped = append(remapped, projectIndex)
		}
	}
	return filterComposeBlinkSelection(remapped, newSources)
}

// cycleComposeBlinkSelection returns the next selected project index after
// current. It wraps and starts at the first selection when current is stale.
func cycleComposeBlinkSelection(selection []int, current int) (int, bool) {
	if len(selection) == 0 {
		return 0, false
	}
	for i, index := range selection {
		if index == current {
			return selection[(i+1)%len(selection)], true
		}
	}
	return selection[0], true
}

func composePixelValueAt(img *models.LoadedImage, point imagePoint) (float64, bool) {
	if img == nil {
		return 0, false
	}
	data := img.HDU.Data
	if data.Width <= 0 || data.Height <= 0 || point.X < 0 || point.Y < 0 || point.X >= data.Width || point.Y >= data.Height {
		return 0, false
	}
	idx := point.Y*data.Width + point.X
	if idx < 0 || idx >= len(data.Pixels) {
		return 0, false
	}
	return float64(data.Pixels[idx]), true
}

// composePickRadius is the half-width (in pixels) of the box sampled when
// picking a black/white level. A 5x5 region keeps the picked value stable
// against single noisy pixels without averaging over real structure.
const composePickRadius = 2

// composeRegionMedianAt returns the median of the finite pixels in the square
// region of half-width radius centered on point. Using a median (rather than a
// single pixel) makes level picking robust to noise and hot/cold pixels, so the
// committed level no longer depends on exactly which pixel was clicked.
func composeRegionMedianAt(img *models.LoadedImage, point imagePoint, radius int) (float64, bool) {
	if img == nil {
		return 0, false
	}
	data := img.HDU.Data
	if data.Width <= 0 || data.Height <= 0 || point.X < 0 || point.Y < 0 || point.X >= data.Width || point.Y >= data.Height {
		return 0, false
	}
	if radius < 0 {
		radius = 0
	}
	vals := make([]float64, 0, (2*radius+1)*(2*radius+1))
	for dy := -radius; dy <= radius; dy++ {
		y := point.Y + dy
		if y < 0 || y >= data.Height {
			continue
		}
		row := y * data.Width
		for dx := -radius; dx <= radius; dx++ {
			x := point.X + dx
			if x < 0 || x >= data.Width {
				continue
			}
			idx := row + x
			if idx < 0 || idx >= len(data.Pixels) {
				continue
			}
			v := float64(data.Pixels[idx])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return 0, false
	}
	sort.Float64s(vals)
	n := len(vals)
	if n%2 == 1 {
		return vals[n/2], true
	}
	return (vals[n/2-1] + vals[n/2]) / 2, true
}

func buildSharedScaleChannelHistograms(channelPixels [3][]float32, channelStats [3]histogram.Stats) ([3][256]int, int, bool) {
	var bins [3][256]int
	var sharedMin, sharedMax float64
	haveRange := false

	for i := 0; i < 3; i++ {
		stats := channelStats[i]
		if stats.Count == 0 || len(channelPixels[i]) == 0 {
			continue
		}
		low, high := histogram.PercentileClip(stats, 0.1, 99.9)
		if high <= low {
			low, high = stats.Min, stats.Max
		}
		if !haveRange {
			sharedMin, sharedMax = low, high
			haveRange = true
			continue
		}
		if low < sharedMin {
			sharedMin = low
		}
		if high > sharedMax {
			sharedMax = high
		}
	}
	if !haveRange {
		return bins, 0, false
	}

	sharedRange := sharedMax - sharedMin
	sharedMaxCount := 0
	for i := 0; i < 3; i++ {
		if len(channelPixels[i]) == 0 {
			continue
		}
		for _, v := range channelPixels[i] {
			fv := float64(v)
			if math.IsNaN(fv) || math.IsInf(fv, 0) {
				continue
			}
			idx := 0
			if sharedRange > 0 {
				idx = int((fv - sharedMin) / sharedRange * 255.0)
			}
			if idx < 0 {
				idx = 0
			} else if idx > 255 {
				idx = 255
			}
			bins[i][idx]++
			if bins[i][idx] > sharedMaxCount {
				sharedMaxCount = bins[i][idx]
			}
		}
	}
	if sharedMaxCount == 0 {
		return bins, 0, false
	}
	return bins, sharedMaxCount, true
}

func applyComposePreviewData(data composePreviewData, views []*viewport, pushHist func([3]histogram.Stats)) {
	for i := 0; i < 4 && i < len(views); i++ {
		if views[i] == nil {
			continue
		}
		item := data.Views[i]
		if item.Image == nil {
			item.Image = blankImg()
		}
		views[i].image.Image = item.Image
		views[i].origW, views[i].origH = item.OrigW, item.OrigH
		views[i].bins = item.Bins
		views[i].histMax = item.HistMax
		views[i].blackBox.SetValue(item.Black)
		views[i].whiteBox.SetValue(item.White)
		if views[i].StatsLabel != nil {
			if item.StatsText == "" {
				item.StatsText = "Sky --  μ --  σ --"
			}
			views[i].StatsLabel.SetText(item.StatsText)
		}
		views[i].SetFilterText(item.FilterText)
		views[i].histogram.Refresh()
		if views[i].zoomLabel.Selected == "fit" {
			views[i].zoom = views[i].fitZoom()
		}
		views[i].applyZoom()
		views[i].image.Refresh()
	}
	if pushHist != nil {
		pushHist(data.RGBStats)
	}
}

// Updated signature to expect an array of histogram.Stats structs
func updatePreviews(imgs []*models.LoadedImage, views []*viewport, levels *models.RgbLevels, pushHist func([3]histogram.Stats), composeRGB func(context.Context) ([]byte, int, int, [3]histogram.Stats, error)) {
	start := time.Now()
	defer func() {
		debuglog.Log(fmt.Sprintf("updatePreviews: total took %s", time.Since(start)))
	}()
	for i := 0; i < 3; i++ {
		channelStart := time.Now()
		if imgs[i] == nil {
			views[i].image.Image = blankImg()
			views[i].bins = [256]int{}
			views[i].histMax = 0
			views[i].blackBox.SetValue(0)
			views[i].whiteBox.SetValue(0)

			if views[i].StatsLabel != nil {
				views[i].StatsLabel.SetText("Sky --  μ --  σ --")
			}

			views[i].histogram.Refresh()
			views[i].image.Refresh()
			continue
		}

		stretchStart := time.Now()
		stretched, mask := processing.ApplyStretchParallel(imgs[i])
		debuglog.Log(fmt.Sprintf("updatePreviews: channel %d stretch took %s", i+1, time.Since(stretchStart)))

		rgbaStart := time.Now()
		views[i].image.Image = processing.ToGrayRGBA(stretched, mask)
		debuglog.Log(fmt.Sprintf("updatePreviews: channel %d gray RGBA took %s", i+1, time.Since(rgbaStart)))
		views[i].origW, views[i].origH = stretched.Width, stretched.Height

		// Use the new struct to compute data
		histStart := time.Now()
		stats := histogram.Compute(stretched.Pixels)
		sky, _ := processing.EstimateBackground(stretched.Pixels)
		debuglog.Log(fmt.Sprintf("updatePreviews: channel %d histogram took %s", i+1, time.Since(histStart)))
		views[i].bins = stats.Hist
		views[i].histMax = 0

		if views[i].StatsLabel != nil {
			views[i].StatsLabel.SetText(fmt.Sprintf("Sky %.3f  μ %.3f  σ %.3f", sky, stats.Mean, stats.Std))
		}

		views[i].blackBox.SetValue(imgs[i].Black)
		views[i].whiteBox.SetValue(imgs[i].White)

		views[i].histogram.Refresh()
		if views[i].zoomLabel.Selected == "fit" {
			views[i].zoom = views[i].fitZoom()
		}
		views[i].applyZoom()
		views[i].image.Refresh()
		debuglog.Log(fmt.Sprintf("updatePreviews: channel %d total took %s", i+1, time.Since(channelStart)))
	}

	// NOTE: processing.ComposeRGB must be updated to return [3]histogram.Stats instead of [3][256]int
	composeStart := time.Now()
	buf, w, h, rgbStats := processing.ComposeRGB(context.Background(), imgs)
	debuglog.Log(fmt.Sprintf("updatePreviews: ComposeRGB took %s", time.Since(composeStart)))
	if composeRGB != nil {
		if altBuf, altW, altH, altStats, err := composeRGB(context.Background()); err == nil {
			buf, w, h, rgbStats = altBuf, altW, altH, altStats
		} else {
			debuglog.Log(fmt.Sprintf("updatePreviews: compose failed: %v", err))
		}
	}

	if buf == nil {
		if pushHist != nil {
			pushHist([3]histogram.Stats{})
		}
		views[3].image.Image = blankImg()
		views[3].bins = [256]int{}
		views[3].histMax = 0
		views[3].blackBox.SetValue(0)
		views[3].whiteBox.SetValue(0)
		views[3].histogram.Refresh()
		views[3].image.Refresh()
		return
	}

	if pushHist != nil {
		pushHist(rgbStats)
	}

	rgbLevelsStart := time.Now()
	buf = processing.ApplyRGBLevels(buf, levels)
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	copy(img.Pix, buf)
	debuglog.Log(fmt.Sprintf("updatePreviews: RGB levels/final image took %s", time.Since(rgbLevelsStart)))

	views[3].image.Image = img
	views[3].origW, views[3].origH = w, h
	views[3].bins = [256]int{}
	views[3].histMax = 0
	views[3].blackBox.SetValue(0)
	views[3].whiteBox.SetValue(0)
	views[3].histogram.Refresh()

	if views[3].zoomLabel.Selected == "fit" {
		views[3].zoom = views[3].fitZoom()
	}

	views[3].applyZoom()
	views[3].image.Refresh()
}

func defaultRGBLevels() *models.RgbLevels {
	return &models.RgbLevels{
		Min: [3]float64{0, 0, 0},
		Max: [3]float64{255, 255, 255},
	}
}

func composeHasAllBaseChannels(imgs []*models.LoadedImage) bool {
	return len(imgs) >= 3 && imgs[0] != nil && imgs[1] != nil && imgs[2] != nil
}

func composeCompositeDisabledStatus(buildComposite bool, imgs []*models.LoadedImage) string {
	if !buildComposite {
		return "Composite disabled — enable Build color composite"
	}
	if !composeHasAllBaseChannels(imgs) {
		return "Composite disabled until all channels are loaded"
	}
	return "Composite disabled"
}

// applyCrossChannelReplacementRow replays the sparse cosmic-ray replacements
// from one disk row. replacement is little-endian float32 data and valid marks
// the pixels that have a replacement; both are bounded to the shared region.
func applyCrossChannelReplacementRow(row []float32, replacement, valid []byte) bool {
	limit := len(row)
	if len(valid) < limit {
		limit = len(valid)
	}
	if len(replacement)/4 < limit {
		limit = len(replacement) / 4
	}
	dirty := false
	for x := 0; x < limit; x++ {
		if valid[x] == 0 {
			continue
		}
		row[x] = math.Float32frombits(binary.LittleEndian.Uint32(replacement[x*4:]))
		dirty = true
	}
	return dirty
}

const crossChannelCleanReplayBatchBytes = 100 * 1024 * 1024

// crossChannelCleanReplayRowsPerBatch keeps the deferred replacement replay at
// about 100 MiB of pixel data while ensuring narrow images still make progress.
func crossChannelCleanReplayRowsPerBatch(width, height int) int {
	if width <= 0 || height <= 0 {
		return 1
	}
	rows := int(int64(crossChannelCleanReplayBatchBytes) / (int64(width) * 4))
	if rows < 1 {
		rows = 1
	}
	return min(rows, height)
}

// channelBorder wraps a canvas object with a colored rectangular border.
func channelBorder(content fyne.CanvasObject, col color.Color) fyne.CanvasObject {
	rect := canvas.NewRectangle(color.Transparent)
	rect.StrokeColor = col
	rect.StrokeWidth = 1
	rect.CornerRadius = 6
	return container.NewMax(content, rect)
}

// offsetRow builds a compact labelled row for the manual-offset inputs.
// The label is rendered smaller than body text to save vertical space.
func offsetRow(name string, entry *NumberEntry) fyne.CanvasObject {
	lbl := canvas.NewText(name, theme.ForegroundColor())
	lbl.TextSize = theme.TextSize() - 2
	return container.NewBorder(nil, nil, lbl, nil, entry)
}

// composeRenderCache memoises a channel's offset-applied pixels so an unchanged
// Manual Offset isn't re-warped on every render. src is the original pixel slice
// the warp was based on; if the channel is reloaded (new slice) the cache misses.
type composeRenderCache struct {
	dx, dy, rot float64
	hasAlign    bool
	align       processing.AffineTransform
	src         []float32
	pixels      []float32
}

// sameFloatSlice reports whether a and b share the same backing array (and
// length) — used to detect that a channel's original pixels were replaced.
func sameFloatSlice(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return &a[0] == &b[0]
}

// composeManualOffsetTransform builds the backward (output→source) sampling
// transform for a channel's Manual Offset: a rotation by rot degrees about the
// image centre followed by a (dx, dy) pixel shift of the image content. It is
// the single definition shared by Apply Offset, Auto-Align, and blink so the
// Manual Offset fields are the one source of truth for a channel's placement.
func composeManualOffsetTransform(w, h int, dx, dy, rot float64) processing.AffineTransform {
	cx := float64(w) / 2
	cy := float64(h) / 2
	rad := rot * math.Pi / 180
	cosA := math.Cos(rad)
	sinA := math.Sin(rad)
	return processing.AffineTransform{
		A: cosA, B: sinA,
		C: -cosA*(cx+dx) - sinA*(cy+dy) + cx,
		D: -sinA, E: cosA,
		F: sinA*(cx+dx) - cosA*(cy+dy) + cy,
	}
}

// extractManualOffset is the inverse of composeManualOffsetTransform: given a
// backward sampling transform, it recovers the (dx, dy, rot) Manual Offset
// fields that reproduce it. It is exact for a rotation+translation (scale 1);
// any scale component is ignored (Auto-Align is captured as translation+rotation
// per the fields-as-source-of-truth model).
func extractManualOffset(t processing.AffineTransform, w, h int) (dx, dy, rot float64) {
	cx := float64(w) / 2
	cy := float64(h) / 2
	rad := math.Atan2(t.B, t.A)
	cosA := math.Cos(rad)
	sinA := math.Sin(rad)
	// Solve composeManualOffsetTransform's C/F equations for u=cx+dx, v=cy+dy.
	// The 2×2 system has determinant 1 (cos²+sin²).
	u := -cosA*(t.C-cx) + sinA*(t.F-cy)
	v := -sinA*(t.C-cx) - cosA*(t.F-cy)
	return u - cx, v - cy, rad * 180 / math.Pi
}

// channelAlignTransform returns the channel's stored star-alignment affine
// (backward sampling) and whether one is present.
func channelAlignTransform(img *models.LoadedImage) (processing.AffineTransform, bool) {
	if img == nil || !img.HasAlignTransform {
		return processing.AffineTransform{}, false
	}
	return processing.AffineTransform{
		A: img.AlignA, B: img.AlignB, C: img.AlignC,
		D: img.AlignD, E: img.AlignE, F: img.AlignF,
	}, true
}

// setChannelAlignTransform stores the full fitted alignment affine on the image.
// Unlike the Manual Offset fields (translation+rotation only), this preserves the
// scale and skew of the fit, which are applied at render time.
func setChannelAlignTransform(img *models.LoadedImage, t processing.AffineTransform) {
	if img == nil {
		return
	}
	img.HasAlignTransform = true
	img.AlignA, img.AlignB, img.AlignC = t.A, t.B, t.C
	img.AlignD, img.AlignE, img.AlignF = t.D, t.E, t.F
}

func matchComposeChannelStretch(ref, target *models.LoadedImage, matchStarCores bool) error {
	if ref == nil || target == nil {
		return fmt.Errorf("missing channel")
	}
	refPixels := ref.HDU.Data.Pixels
	targetPixels := target.HDU.Data.Pixels
	if len(refPixels) == 0 || len(targetPixels) == 0 {
		return fmt.Errorf("empty image data")
	}

	refLow, ok := composePercentile(refPixels, 50)
	if !ok {
		return fmt.Errorf("reference has no finite pixels")
	}
	targetLow, ok := composePercentile(targetPixels, 50)
	if !ok {
		return fmt.Errorf("target has no finite pixels")
	}

	refHigh, targetHigh, ok := composeStarCoreAnchors(ref, target, matchStarCores)
	if !ok {
		refHigh, ok = composePercentile(refPixels, 99.8)
		if !ok {
			return fmt.Errorf("reference high anchor unavailable")
		}
		targetHigh, ok = composePercentile(targetPixels, 99.8)
		if !ok {
			return fmt.Errorf("target high anchor unavailable")
		}
	}

	refScaledPeak := ref.ScaledPeak
	if math.IsNaN(refScaledPeak) || math.IsInf(refScaledPeak, 0) || refScaledPeak <= 0 {
		refScaledPeak = 10
	}
	target.Mode = ref.Mode
	target.ScaledPeak = refScaledPeak
	target.ShowClip = ref.ShowClip

	zLow := composeScaledInput(refLow, ref.Background, ref.Peak, refScaledPeak)
	zHigh := composeScaledInput(refHigh, ref.Background, ref.Peak, refScaledPeak)
	a := zLow / refScaledPeak
	b := zHigh / refScaledPeak
	if !isFinite64(a) || !isFinite64(b) || math.Abs(b-a) < 1e-9 || targetHigh <= targetLow {
		black, white, background, peak := processing.SmartLevels(targetPixels)
		target.Black = black
		target.White = white
		target.Background = background
		target.Peak = peak
		return nil
	}

	denom := (targetHigh - targetLow) / (b - a)
	background := targetLow - a*denom
	peak := background + denom
	if !isFinite64(background) || !isFinite64(peak) || peak <= background {
		black, white, smartBackground, smartPeak := processing.SmartLevels(targetPixels)
		target.Black = black
		target.White = white
		target.Background = smartBackground
		target.Peak = smartPeak
		return nil
	}

	target.Background = background
	target.Peak = peak
	target.Black = background
	target.White = peak
	return nil
}

func cloneLoadedImageForStretchMatch(img *models.LoadedImage) *models.LoadedImage {
	if img == nil {
		return nil
	}
	clone := *img
	clone.HDU = img.HDU
	clone.HDU.Data = img.HDU.Data
	if img.HDU.Data.Pixels != nil {
		clone.HDU.Data.Pixels = append([]float32(nil), img.HDU.Data.Pixels...)
	}
	return &clone
}

func composeScaledInput(raw, background, peak, scaledPeak float64) float64 {
	if !isFinite64(background) {
		background = 0
	}
	if !isFinite64(peak) || peak <= background {
		peak = background + 1
	}
	if !isFinite64(scaledPeak) || scaledPeak <= 0 {
		scaledPeak = 10
	}
	v := (raw - background) * scaledPeak / (peak - background)
	if v < 0 {
		return 0
	}
	return v
}

func composeStarCoreAnchors(ref, target *models.LoadedImage, enabled bool) (float64, float64, bool) {
	if !enabled || ref.HDU.Data.Width <= 0 || ref.HDU.Data.Height <= 0 {
		return 0, 0, false
	}
	refPixels := ref.HDU.Data.Pixels
	targetPixels := target.HDU.Data.Pixels
	refWhite := ref.White
	if refWhite <= ref.Black {
		refWhite = ref.Peak
	}
	stars := processing.ExtractStars(refPixels, ref.HDU.Data.Width, ref.HDU.Data.Height, 4, 5)
	refCores := make([]float64, 0, 64)
	targetCores := make([]float64, 0, 64)
	for _, star := range stars {
		if len(refCores) >= 64 {
			break
		}
		x := int(math.Round(star.X))
		y := int(math.Round(star.Y))
		if x < 0 || y < 0 || x >= ref.HDU.Data.Width || y >= ref.HDU.Data.Height {
			continue
		}
		refIdx := y*ref.HDU.Data.Width + x
		if refIdx < 0 || refIdx >= len(refPixels) {
			continue
		}
		refCore := float64(refPixels[refIdx])
		if !isFinite64(refCore) || refCore >= refWhite*0.98 {
			continue
		}
		tx := x
		ty := y
		if ref.HDU.Data.Width != target.HDU.Data.Width || ref.HDU.Data.Height != target.HDU.Data.Height {
			tx = int(math.Round(float64(x) * float64(target.HDU.Data.Width) / float64(ref.HDU.Data.Width)))
			ty = int(math.Round(float64(y) * float64(target.HDU.Data.Height) / float64(ref.HDU.Data.Height)))
		}
		if tx < 0 || ty < 0 || tx >= target.HDU.Data.Width || ty >= target.HDU.Data.Height {
			continue
		}
		targetIdx := ty*target.HDU.Data.Width + tx
		if targetIdx < 0 || targetIdx >= len(targetPixels) {
			continue
		}
		targetCore := float64(targetPixels[targetIdx])
		if !isFinite64(targetCore) {
			continue
		}
		refCores = append(refCores, refCore)
		targetCores = append(targetCores, targetCore)
	}
	if len(refCores) < 5 || len(targetCores) < 5 {
		return 0, 0, false
	}
	sort.Float64s(refCores)
	sort.Float64s(targetCores)
	return composePercentileSorted(refCores, 75), composePercentileSorted(targetCores, 75), true
}

func composePercentile(pixels []float32, p float64) (float64, bool) {
	values := make([]float64, 0, len(pixels))
	for _, v := range pixels {
		fv := float64(v)
		if isFinite64(fv) {
			values = append(values, fv)
		}
	}
	if len(values) == 0 {
		return 0, false
	}
	sort.Float64s(values)
	return composePercentileSorted(values, p), true
}

func composePercentileSorted(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if p <= 0 {
		return values[0]
	}
	if p >= 100 {
		return values[len(values)-1]
	}
	pos := (p / 100) * float64(len(values)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return values[lo]
	}
	frac := pos - float64(lo)
	return values[lo]*(1-frac) + values[hi]*frac
}

func isFinite64(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// setStretchParamEntries populates a channel's stretch-parameter entry widgets
// from a LoadedImage, substituting defaults for unset (zero) values so the
// fields always show a meaningful number.
func setStretchParamEntries(control *models.ChannelControl, img *models.LoadedImage) {
	if control == nil || img == nil {
		return
	}
	asinh := img.AsinhScale
	if asinh <= 0 {
		asinh = stretch.DefaultAsinhScale
	}
	mtf := img.MTFMidtone
	if mtf <= 0 || mtf >= 1 {
		mtf = stretch.DefaultMTFMidtone
	}
	d := img.GHSStretch
	if d <= 0 {
		d = stretch.DefaultGHSStretch
	}
	sp := img.GHSSymmetry
	if sp <= 0 || sp >= 1 {
		sp = stretch.DefaultGHSSymmetry
	}
	if control.AsinhScaleEntry != nil {
		control.AsinhScaleEntry.SetValue(asinh)
	}
	if control.MTFMidtoneEntry != nil {
		control.MTFMidtoneEntry.SetValue(mtf)
	}
	if control.GHSStretchEntry != nil {
		control.GHSStretchEntry.SetValue(d)
	}
	if control.GHSLocalEntry != nil {
		control.GHSLocalEntry.SetValue(img.GHSLocal)
	}
	if control.GHSSymmetryEntry != nil {
		control.GHSSymmetryEntry.SetValue(sp)
	}
}

func modeToLabel(m stretch.Mode) string {
	switch m {
	case stretch.Linear:
		return "Linear"
	case stretch.Log:
		return "Log"
	case stretch.Asinh:
		return "Asinh"
	case stretch.Sqrt:
		return "Sqrt"
	case stretch.HistEq:
		return "HistEq"
	case stretch.MTF:
		return "MTF"
	case stretch.GHS:
		return "GHS"
	default:
		return "Linear"
	}
}

func labelToMode(label string) stretch.Mode {
	switch strings.ToLower(label) {
	case "log":
		return stretch.Log
	case "asinh":
		return stretch.Asinh
	case "sqrt":
		return stretch.Sqrt
	case "histeq":
		return stretch.HistEq
	case "mtf":
		return stretch.MTF
	case "ghs":
		return stretch.GHS
	default:
		return stretch.Linear
	}
}
