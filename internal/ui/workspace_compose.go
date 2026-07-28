package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
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

// globalSendToChannel is registered by newComposeWorkspace and called by the
// preview window to load an image directly into a compose channel with all
// stretch settings already applied.
var globalSendToChannel func(channelIdx int, img *models.LoadedImage)

// globalSelectComposeTab is registered by app.go and called by loadProject in
// workspace_compose.go to switch to the Compose tab when loading a Compose project.
var globalSelectComposeTab func()

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
	// Any source, alignment, stretch, or overlay edit invalidates a previously
	// calculated result. The persisted transforms remain inspectable but are
	// never silently applied to changed pixels.
	invalidateCalibration := func() { markComposeCalibrationStale(&colorCalibration) }
	ensureOverlayCalibration := func(n int) {
		for len(colorCalibration.Overlays) <= n {
			colorCalibration.Overlays = append(colorCalibration.Overlays, models.OverlayCalibrationState{Mode: models.OverlayArtistic, Status: models.CalibrationDisabled, Strength: 1})
		}
	}

	// Updated to track the new struct
	var latestRGBStats [3]histogram.Stats
	suspendRefresh := false
	var composeRGB func(ctx context.Context, calibrationSnapshot *models.ColorCalibrationState) ([]byte, int, int, [3]histogram.Stats, *processing.ComposeRenderResult, error)
	// renderImages returns an offset-applied view of imgs (Manual Offsets applied
	// at render time). Forward-declared so refresh/compose can use it; assigned
	// once controlSets exists.
	var renderImages func() []*models.LoadedImage
	var previewMu sync.Mutex
	previewSeq := 0
	var genCancel context.CancelFunc
	var calibrationJob composeCalibrationJob
	var calibrationGeneration uint64
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
		imgSnapshot := renderImages()
		blinkSourcesSnapshot := composeBlinkSources()
		blinkSelectionSnapshot := append([]int(nil), blinkChannels...)
		blinkEnabled := blinkCheck.Checked
		if blinkEnabled && blinkChannels == nil {
			blinkSelectionSnapshot = resolveComposeBlinkSelection(blinkSourcesSnapshot, nil, true, blinkExcludedIdx)
		}
		if blinkEnabled && len(filterComposeBlinkSelection(blinkSelectionSnapshot, blinkSourcesSnapshot)) < 2 {
			blinkSelectionSnapshot = nil
		}
		levelsSnapshot := *levels
		sharedHistScale := sharedHistCheck.Checked
		buildComposite := buildCompositeCheck.Checked
		for i := range overlayLayers {
			ensureOverlayCalibration(i)
		}
		calibrationSnapshot := cloneCalibrationForPreview()
		go func() {
			start := time.Now()
			debuglog.Log("compose refresh async: starting preview computation")
			var renderedResult *processing.ComposeRenderResult
			data := buildComposePreviewData(ctx, imgSnapshot, sharedHistScale, buildComposite, &levelsSnapshot, func(c context.Context) ([]byte, int, int, [3]histogram.Stats, error) {
				b, w, h, s, rendered, e := composeRGB(c, calibrationSnapshot)
				renderedResult = rendered
				return b, w, h, s, e
			})
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
					if onDone != nil {
						onDone()
					}
					return
				}
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
	refresh := func() {
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
		rendered, err := composeRenderWithCalibration(ctx, renderImages(), calibrationSnapshot)
		return rendered.Preview, rendered.Width, rendered.Height, rendered.Stats, &rendered, err
	}

	saveChannelGray := func(idx int) {
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

			go func() {
				img, loadErr := loadImageFromPath(path)

				if loadErr != nil {
					progressDialog.Hide()
					dialog.ShowError(loadErr, win)
					return
				}

				replaceComposeChannelImage(imgs, idx, img)
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
		data, err := buildComposeOverlayPreviewData(context.Background(), imgs[l.idx])
		if err != nil {
			return
		}
		applyLayerPreview(l, data)
		refresh()
	}

	saveLayerGray := func(l *overlayLayer) {
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
			go func() {
				img, loadErr := loadImageFromPath(path)
				if loadErr != nil {
					progressDialog.Hide()
					dialog.ShowError(loadErr, win)
					return
				}
				imgs[l.idx] = img
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
		l.control = channelControls(l.name+" Image", color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255}, l.idx, imgs, &origPixels, layerViews(l), func() { refreshLayerPreview(l) }, composeMagicPreset, false)

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
			go func() {
				img, loadErr := loadImageFromPath(path)
				fyne.Do(func() {
					progressDialog.Hide()
					if loadErr != nil {
						dialog.ShowError(loadErr, win)
						return
					}
					l, ok := createOverlayLayer(defaultOverlayLayerSettings(len(overlayLayers)))
					if !ok {
						dialog.ShowInformation("Layer limit", fmt.Sprintf("A maximum of %d colored layers is supported.", maxOverlayLayers), win)
						return
					}
					imgs[l.idx] = img
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

	controlSets = []*models.ChannelControl{
		channelControls("Channel 1 (Blue)", color.RGBA{R: 100, G: 149, B: 237, A: 255}, 0, imgs, &origPixels, viewports, refresh, composeMagicPreset, true),
		channelControls("Channel 2 (Green)", color.RGBA{R: 80, G: 200, B: 80, A: 255}, 1, imgs, &origPixels, viewports, refresh, composeMagicPreset, true),
		channelControls("Channel 3 (Red)", color.RGBA{R: 237, G: 80, B: 80, A: 255}, 2, imgs, &origPixels, viewports, refresh, composeMagicPreset, true),
	}

	// The Manual Offset (X/Y/Rot) fields are the source of truth for each channel's
	// placement, applied at RENDER time only — imgs[idx] always holds the original
	// loaded pixels and is never warped/baked. renderImages() returns an
	// offset-applied view used for the composite (merge) and the per-channel
	// previews the blink shows. Results are cached so an unchanged offset isn't
	// re-warped on every refresh.
	channelOffsetFields := func(idx int) (dx, dy, rot float64, ok bool) {
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
		dx, dy, rot, ok := channelOffsetFields(idx)
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
	viewports[3].SetCenterAction("Composite", "C", color.RGBA{R: 200, G: 110, B: 30, A: 255},
		"Export to Edit", func() {
			if globalExportToEdit == nil {
				return
			}
			img := compositeImageForEdit()
			if img == nil {
				dialog.ShowInformation("Nothing to export", "Compose all three channels first.", win)
				return
			}
			globalExportToEdit(img)
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
			dx, dy, rot, _ := channelOffsetFields(i)
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
					idx   int
					img   *models.LoadedImage
					state models.ChannelState
					err   error
				}

				total := len(imgs)
				results := make(chan loadResult, total)
				var wg sync.WaitGroup

				stateForIdx := func(i int) models.ChannelState {
					if i < 3 {
						return project.Channels[i]
					}
					return layerStates[i-3].Channel
				}

				for i := 0; i < total; i++ {
					state := stateForIdx(i)
					if state.Path == "" {
						results <- loadResult{idx: i, state: state}
						continue
					}
					wg.Add(1)
					go func(idx int, state models.ChannelState) {
						defer wg.Done()
						img, loadErr := loadImageFromPath(state.Path)
						results <- loadResult{idx: idx, img: img, state: state, err: loadErr}
					}(i, state)
				}

				go func() {
					wg.Wait()
					close(results)
				}()

				errors := make([]string, 0, total)
				for res := range results {
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
					clearComposeOrigPixels(&origPixels, res.idx)
				}

				fyne.Do(func() {
					debuglog.Log("load compose project: applying loaded project state")
					withSuspendedRefresh(func() {
						for i, img := range imgs[:3] {
							if img != nil {
								restoreComposeChannelRotation(img, project.Channels[i].Rotation90)
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
						blinkCheck.SetChecked(project.BlinkFilters)
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
						if blinkCheck.Checked && startBlink != nil {
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
		img := compositeImageForEdit()
		if img == nil {
			dialog.ShowInformation("Nothing to send", "Compose all three channels first.", win)
			return
		}
		globalExportToEdit(img)
	}

	clearChannels := func() {
		dialog.ShowConfirm("Clear Channels", "Free all three channel images from memory?", func(ok bool) {
			if !ok {
				return
			}
			oldSources := composeBlinkSources()
			for i := 0; i < 3; i++ {
				imgs[i] = nil
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
				imgs[i] = nil
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
			calibrationSnapshot.Overlays = append([]models.OverlayCalibrationState(nil), colorCalibration.Overlays...)
			for i := range calibrationSnapshot.Overlays {
				calibrationSnapshot.Overlays[i].Diagnostics.Warnings = append([]string(nil), colorCalibration.Overlays[i].Diagnostics.Warnings...)
			}
			priorForJob := prior
			imageSnapshots := make([]*models.LoadedImage, 3)
			for i := range imageSnapshots {
				imageSnapshots[i] = cloneLoadedImageForStretchMatch(imgs[i])
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
			var inputErr error
			for i := range inputs {
				img := imageSnapshots[i]
				pixels := append([]float32(nil), img.HDU.Data.Pixels...)
				inputs[i] = processing.CalibrationInput{SourceIdentity: img.Path, Width: img.HDU.Data.Width, Height: img.HDU.Data.Height, Pixels: pixels, Valid: make([]bool, len(pixels)), Alignment: fmt.Sprintf("channel-%d", i)}
				for j := range inputs[i].Valid {
					inputs[i].Valid[j] = true
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
					inputs[i].Metadata = metadata
					inputs[i].Photometry = &photometry
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
									alignedPlanes, aw, ah, alignErr := processing.AlignedPlanesForCalibration(ctx, imageSnapshots)
									if alignErr != nil {
										err = alignErr
									} else {
										stars := processing.ExtractAndLimitStars(alignedPlanes[1].Pixels, aw, ah, 5, 3, 500)
										debuglog.Log(fmt.Sprintf("Gaia UI alignment/detection: aligned=%dx%d detected_stars=%d", aw, ah, len(stars)))
										planes := [3]processing.GaiaPlane{}
										for c := range planes {
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
										greq := processing.GaiaCalibrationRequest{Query: query, Settings: gaiaRequestSettings(calibrationSnapshot.Gaia, matchRadius, epoch, magnitude), DetectedStars: stars, Planes: planes, PixelToSky: pixelToSky}
										greq.Settings.ObservationEpoch = query.ObservationEpoch
										gaiaResult, runErr := composeGaiaJobService.Run(ctx, GaiaJobRequest{Provider: provider, Query: query, Settings: greq.Settings, Calibration: greq}, func(p GaiaJobProgress) {
											debuglog.Log(fmt.Sprintf("Gaia UI stage: %s", p.Stage))
											fyne.Do(func() { status.SetText(fmt.Sprintf("Calibration: calculating — Gaia %s", p.Stage)) })
										})
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
				if err != nil {
					// Metadata parsing above produced the actionable unsupported reason.
				} else if ctx.Err() != nil {
					err = ctx.Err()
				} else {
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
				if err == nil && !gaiaDone {
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
		calibrationWindow.SetContent(container.NewVScroll(content))
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

		allLoaded := imgs[0] != nil && imgs[1] != nil && imgs[2] != nil

		normalizeScaleItem.Disabled = !allLoaded
		alignChannelsItem.Disabled = !allLoaded
		cleanChannelsItem.Disabled = !allLoaded
		resetDataItem.Disabled = !allLoaded
		exportRGBItem.Disabled = !allLoaded
		sendToEditItem.Disabled = !allLoaded

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
		saveProjectItem.Disabled = imgs[0] == nil && imgs[1] == nil && imgs[2] == nil && !anyOverlayLoaded
		if m := win.MainMenu(); m != nil {
			m.Refresh()
		}
	}
	updateMenus()

	// Register package-level callback so the preview window can inject an image
	// into any channel with its current stretch settings.
	globalSendToChannel = func(channelIdx int, img *models.LoadedImage) {
		if channelIdx < 0 || channelIdx >= 3 {
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
						var cc *models.ChannelControl
						if target.idx < len(controlSets) {
							cc = controlSets[target.idx]
						} else {
							for _, layer := range overlayLayers {
								if layer != nil && layer.idx == target.idx {
									cc = layer.control
									break
								}
							}
						}
						if cc != nil {
							cc.BackgroundEntry.SetValue(target.img.Background)
							cc.PeakEntry.SetValue(target.img.Peak)
							cc.MTFMidtoneEntry.SetValue(target.img.MTFMidtone)
							cc.ModeSelect.SetSelected("MTF")
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

func snapshotMagicStretch(img *models.LoadedImage) magicStretchSnapshot {
	return magicStretchSnapshot{img.Background, img.Peak, img.Black, img.White, img.MTFMidtone, img.Mode}
}
func magicStretchUnchanged(img *models.LoadedImage, s magicStretchSnapshot) bool {
	return snapshotMagicStretch(img) == s
}
func installMagicStretch(dst *models.LoadedImage, src models.LoadedImage) {
	dst.Background, dst.Peak, dst.Black, dst.White, dst.MTFMidtone, dst.Mode = src.Background, src.Peak, src.Black, src.White, src.MTFMidtone, src.Mode
}

func channelControls(label string, col color.Color, idx int, imgs []*models.LoadedImage, origPixels *[][]float32, views []*viewport, refresh func(), magicPreset *widget.Select, allowRotate bool) *models.ChannelControl {
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

	selectBox := widget.NewSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq", "MTF", "GHS"}, func(value string) {
		if imgs[idx] == nil {
			updateStretchParams(labelToMode(value))
			return
		}
		imgs[idx].Mode = labelToMode(value)
		updateStretchParams(imgs[idx].Mode)
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
		imgs[idx].ShowClip = v
		refresh()
	})
	showClip.SetChecked(true)

	var apply *widget.Button
	apply = widget.NewButton("Apply", func() {
		if imgs[idx] == nil {
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
		refresh()
	})
	rotate := widget.NewButton("Rotate 90°", func() {
		if imgs[idx] == nil {
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
