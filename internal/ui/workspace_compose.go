package ui

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/export"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
	"gofitsv3/internal/utils"
)

const starlessComposeTemporarilyDisabled = true

var composeBlinkFilterNames = []string{"Blue", "Green", "Red"}

// globalSendToChannel is registered by newComposeWorkspace and called by the
// preview window to load an image directly into a compose channel with all
// stretch settings already applied.
var globalSendToChannel func(channelIdx int, img *models.LoadedImage)

func newComposeWorkspace(app fyne.App, win fyne.Window) (fyne.CanvasObject, []*fyne.Menu) {
	imgs := make([]*models.LoadedImage, 4)
	origPixels := make([][]float32, 4)
	viewports := []*viewport{newViewport(), newViewport(), newViewport(), newViewport()}
	// Channel histograms: black background, channel-colored bars; compose: white bars.
	viewports[0].histColor = [4]uint8{100, 149, 237, 255} // blue
	viewports[1].histColor = [4]uint8{80, 200, 80, 255}   // green
	viewports[2].histColor = [4]uint8{237, 80, 80, 255}   // red
	viewports[3].histColor = [4]uint8{255, 255, 255, 255} // white (compose)
	headerWins := make([]fyne.Window, 3)
	levels := defaultRGBLevels()
	var levelsWin *rgbLevelsWindow
	starlessSettings := defaultStarlessComposeSettings()
	orangeSettings := defaultOrangeLayerSettings()
	var orangeWin fyne.Window
	var orangeViewport *viewport
	var orangeControl *models.ChannelControl

	// Updated to track the new struct
	var latestRGBStats [3]histogram.Stats
	suspendRefresh := false
	var composeRGBWithOptionalStarless func() ([]byte, int, int, [3]histogram.Stats, *processing.StarlessResult, error)
	// renderImages returns an offset-applied view of imgs (Manual Offsets applied
	// at render time). Forward-declared so refresh/compose can use it; assigned
	// once controlSets exists.
	var renderImages func() []*models.LoadedImage
	var previewMu sync.Mutex
	previewSeq := 0

	flipCheck := NewToggle(nil)
	flipCheck.SetChecked(true)
	sharedHistCheck := NewToggle(nil)
	sharedHistCheck.SetChecked(false)
	buildCompositeCheck := NewToggle(nil)
	buildCompositeCheck.SetChecked(false)
	blinkCheck := NewToggle(nil)
	blinkCheck.SetChecked(false)
	blinkExcludedIdx := 0
	var blinkExcludeSelect *SafeSelect
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

	// Updated signature to pass the stats
	pushRGBHist := func(stats [3]histogram.Stats) {
		latestRGBStats = stats
		if levelsWin != nil {
			levelsWin.setHistogram(stats)
		}
	}

	composeRGBCurrent := func(composeImgs []*models.LoadedImage) ([]byte, int, int, [3]histogram.Stats) {
		if orangeWin != nil && imgs[3] != nil {
			return processing.ComposeRGBWithOrange(composeImgs, imgs[3], orangeSettings)
		}
		return processing.ComposeRGB(composeImgs)
	}

	refresh := func() {
		if suspendRefresh {
			return
		}
		start := time.Now()
		debuglog.Log("compose refresh: starting preview update")
		data := buildComposePreviewData(renderImages(), flipCheck.Checked, sharedHistCheck.Checked, buildCompositeCheck.Checked, levels, composeRGBWithOptionalStarless)
		applyComposePreviewData(data, viewports, pushRGBHist)
		if refreshBlinkFrame != nil {
			refreshBlinkFrame()
		}
		debuglog.Log(fmt.Sprintf("compose refresh: finished preview update in %s", time.Since(start)))
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
		if suspendRefresh {
			if onDone != nil {
				onDone()
			}
			return
		}
		previewMu.Lock()
		previewSeq++
		seq := previewSeq
		previewMu.Unlock()
		imgSnapshot := renderImages()
		levelsSnapshot := *levels
		flip := flipCheck.Checked
		sharedHistScale := sharedHistCheck.Checked
		buildComposite := buildCompositeCheck.Checked
		go func() {
			start := time.Now()
			debuglog.Log("compose refresh async: starting preview computation")
			data := buildComposePreviewData(imgSnapshot, flip, sharedHistScale, buildComposite, &levelsSnapshot, composeRGBWithOptionalStarless)
			debuglog.Log(fmt.Sprintf("compose refresh async: preview computation took %s", time.Since(start)))
			fyne.Do(func() {
				previewMu.Lock()
				currentSeq := previewSeq
				previewMu.Unlock()
				if seq != currentSeq {
					debuglog.Log("compose refresh async: skipped stale preview result")
					if onDone != nil {
						onDone()
					}
					return
				}
				applyComposePreviewData(data, viewports, pushRGBHist)
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
			point, ok := viewports[idx].imagePointAtPosition(pos, flipCheck.Checked)
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
			point, ok := viewports[idx].imagePointAtPosition(pos, flipCheck.Checked)
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

	composeRGBWithOptionalStarless = func() ([]byte, int, int, [3]histogram.Stats, *processing.StarlessResult, error) {
		// Apply Manual Offsets at render time; imgs stays original.
		rimgs := renderImages()
		if starlessComposeTemporarilyDisabled {
			// Starless/white-star processing is intentionally disabled for now.
			// The implementation below remains in the codebase so it can be
			// revisited later, but Compose must not call it from the menu or
			// from saved project settings.
			if starlessSettings.Enabled {
				debuglog.Log("composeRGBWithOptionalStarless: starless temporarily disabled, using normal compose")
			}
			buf, w, h, stats := composeRGBCurrent(rimgs)
			return buf, w, h, stats, nil, nil
		}

		if !starlessSettings.Enabled {
			debuglog.Log("composeRGBWithOptionalStarless: starless disabled, using normal compose")
			buf, w, h, stats := composeRGBCurrent(rimgs)
			return buf, w, h, stats, nil, nil
		}
		if rimgs[0] == nil || rimgs[1] == nil || rimgs[2] == nil {
			debuglog.Log("composeRGBWithOptionalStarless: missing RGB channels")
			return nil, 0, 0, [3]histogram.Stats{}, nil, nil
		}
		debuglog.Log("composeRGBWithOptionalStarless: building aligned reference-grid channel set")
		ref := rimgs[1]
		blueStretched := processing.StretchedImageDataForReferenceGrid(rimgs[0], ref)
		greenStretched := processing.StretchedImageDataForReferenceGrid(rimgs[1], ref)
		redStretched := processing.StretchedImageDataForReferenceGrid(rimgs[2], ref)

		maskSettings := processing.DefaultStarMaskSettings()
		maskSettings.DetectionMode = starlessSettings.DetectionMode
		maskSettings.DetectionPreprocessMode = starlessSettings.DetectionPreprocessMode
		maskSettings.DetectionMergeMode = starlessSettings.DetectionMergeMode
		maskSettings.DetectionSigma = starlessSettings.ThresholdSigma
		maskSettings.BackgroundTileSize = starlessSettings.BackgroundTileSize
		maskSettings.UseNoDataFloor = starlessSettings.UseNoDataFloor
		maskSettings.NoDataFloor = float32(starlessSettings.NoDataFloor)
		maskSettings.SeedMinProminence = starlessSettings.SeedMinProminence
		maskSettings.MinDetectedChannels = starlessSettings.MinDetectedChannels
		maskSettings.MinSeedFootprintArea = starlessSettings.MinSeedFootprintArea
		maskSettings.MinSharedChannels = starlessSettings.MinSharedChannels
		maskSettings.SuppressionRadius = starlessSettings.SuppressionRadius
		maskSettings.MaskGrowRadius = starlessSettings.MaskBaseRadius
		maskSettings.MaskMaxRadius = starlessSettings.MaxRadius
		maskSettings.MaskSoftEdgeRadius = starlessSettings.FeatherRadius
		maskSettings.InpaintRadius = starlessSettings.InpaintRadius
		debuglog.Log(fmt.Sprintf("composeRGBWithOptionalStarless: pipeline settings mode=%s sigma=%.2f tile=%d base=%d max=%d feather=%d inpaint=%d",
			maskSettings.DetectionMode,
			maskSettings.DetectionSigma,
			maskSettings.BackgroundTileSize,
			maskSettings.MaskGrowRadius,
			maskSettings.MaskMaxRadius,
			maskSettings.MaskSoftEdgeRadius,
			maskSettings.InpaintRadius,
		))

		result, err := processing.CreateStarlessChannels([][]float32{
			redStretched.Pixels,
			greenStretched.Pixels,
			blueStretched.Pixels,
		}, ref.HDU.Data.Width, ref.HDU.Data.Height, maskSettings)
		if err != nil {
			debuglog.Log(fmt.Sprintf("composeRGBWithOptionalStarless: pipeline failed: %v", err))
			return nil, 0, 0, [3]histogram.Stats{}, nil, err
		}
		debuglog.Log(fmt.Sprintf("composeRGBWithOptionalStarless: pipeline produced %d components", len(result.Components)))
		recombined, err := processing.RecombineStarlessRGB(result.Starless, result.Stars, result.AlphaMask, result.Width, result.Height, processing.StarRecombineSettings{
			StarBrightness: float32(starlessSettings.StarBrightness),
			StarSaturation: float32(starlessSettings.StarSaturation),
			ValidMask:      result.RecombineValid,
		})
		if err != nil {
			debuglog.Log(fmt.Sprintf("composeRGBWithOptionalStarless: recombine failed: %v", err))
			return nil, 0, 0, [3]histogram.Stats{}, nil, err
		}
		debuglog.Log(fmt.Sprintf("composeRGBWithOptionalStarless: recombined stars brightness=%.2f saturation=%.2f", starlessSettings.StarBrightness, starlessSettings.StarSaturation))

		makeClone := func(src *models.LoadedImage, pixels []float32) *models.LoadedImage {
			clone := *src
			clone.HDU = src.HDU
			clone.HDU.Header = ref.HDU.Header
			clone.HDU.Data = fitsio.ImageData{Width: result.Width, Height: result.Height, Pixels: pixels}
			clone.Mode = stretch.Linear
			clone.Black = 0
			clone.White = 1
			clone.Background = 0
			clone.Peak = 1
			clone.ScaledPeak = 1
			clone.ShowClip = false
			return &clone
		}
		composedImgs := []*models.LoadedImage{
			makeClone(imgs[0], recombined[2]),
			makeClone(imgs[1], recombined[1]),
			makeClone(imgs[2], recombined[0]),
		}
		debuglog.Log("composeRGBWithOptionalStarless: composing final RGB preview")
		buf, w, h, stats := composeRGBCurrent(composedImgs)
		return buf, w, h, stats, result, nil
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
			if flipCheck.Checked {
				stretched = processing.FlipImageData(stretched)
			}
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

				imgs[idx] = img
				clearComposeOrigPixels(&origPixels, idx)

				fyne.Do(func() {
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
		fd.Show()
	}

	refreshOrangePreview := func() {
		if orangeViewport == nil {
			refresh()
			return
		}
		if imgs[3] == nil {
			orangeViewport.image.Image = blankImg()
			orangeViewport.bins = [256]int{}
			orangeViewport.histMax = 0
			orangeViewport.blackBox.SetValue(0)
			orangeViewport.whiteBox.SetValue(0)
			if orangeViewport.StatsLabel != nil {
				orangeViewport.StatsLabel.SetText("Sky --  μ --  σ --")
			}
			orangeViewport.histogram.Refresh()
			orangeViewport.image.Refresh()
			refresh()
			return
		}
		stretched, mask := processing.ApplyStretchParallel(imgs[3])
		stats := histogram.Compute(stretched.Pixels)
		sky, _ := processing.EstimateBackground(stretched.Pixels)
		orangeViewport.image.Image = processing.ToGrayRGBA(stretched, mask)
		orangeViewport.bins = stats.Hist
		orangeViewport.histMax = 0
		orangeViewport.origW = stretched.Width
		orangeViewport.origH = stretched.Height
		orangeViewport.blackBox.SetValue(imgs[3].Black)
		orangeViewport.whiteBox.SetValue(imgs[3].White)
		if orangeViewport.StatsLabel != nil {
			orangeViewport.StatsLabel.SetText(fmt.Sprintf("Sky %.3f  μ %.3f  σ %.3f", sky, stats.Mean, stats.Std))
		}
		orangeViewport.histogram.Refresh()
		if orangeViewport.zoomLabel.Selected == "fit in preview" {
			orangeViewport.zoom = orangeViewport.fitZoom()
		}
		orangeViewport.applyZoom()
		orangeViewport.image.Refresh()
		refresh()
	}

	saveOrangeGray := func() {
		if imgs[3] == nil {
			dialog.ShowInformation("Missing", "Load the orange image first", win)
			return
		}
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()
			format := detectExportFormat(path)
			stretched, _ := processing.ApplyStretchParallel(imgs[3])
			gray := processing.ToGrayRGBA(stretched, make([]byte, len(stretched.Pixels)))
			showExportOptionsDialog(format, win, func(opts export.Options) {
				if err := export.FromImage(path, gray, format, opts); err != nil {
					dialog.ShowError(err, win)
				}
			})
		}, win)
		save.SetFileName("orange_gray.png")
		save.Show()
	}

	loadOrange := func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			progressDialog := dialog.NewCustom("Loading Orange Image", "Reading FITS data...", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()
			go func() {
				img, loadErr := loadImageFromPath(path)
				if loadErr != nil {
					progressDialog.Hide()
					dialog.ShowError(loadErr, win)
					return
				}
				imgs[3] = img
				clearComposeOrigPixels(&origPixels, 3)
				fyne.Do(func() {
					if orangeControl != nil {
						orangeViews := []*viewport{nil, nil, nil, orangeViewport}
						applyChannelState(3, channelStateFromImage(img), imgs, orangeViews, []*models.ChannelControl{nil, nil, nil, orangeControl})
					}
					progressDialog.Hide()
					refreshOrangePreview()
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
		fd.Show()
	}

	openOrangeWindow := func() {
		if orangeWin != nil {
			orangeWin.Show()
			orangeWin.RequestFocus()
			refresh()
			return
		}
		orangeViewport = newViewport()
		orangeViewport.histColor = [4]uint8{orangeSettings.ColorR, orangeSettings.ColorG, orangeSettings.ColorB, 255}
		orangeViewport.SetLoadSave("Orange", "O", color.RGBA{R: orangeSettings.ColorR, G: orangeSettings.ColorG, B: orangeSettings.ColorB, A: 255}, loadOrange, saveOrangeGray)
		orangeViews := []*viewport{nil, nil, nil, orangeViewport}
		orangeControl = channelControls("Orange Image", color.RGBA{R: orangeSettings.ColorR, G: orangeSettings.ColorG, B: orangeSettings.ColorB, A: 255}, 3, imgs, &origPixels, orangeViews, refreshOrangePreview)

		swatch := canvas.NewRectangle(color.RGBA{R: orangeSettings.ColorR, G: orangeSettings.ColorG, B: orangeSettings.ColorB, A: 255})
		swatch.SetMinSize(fyne.NewSize(36, 18))
		updateSwatch := func() {
			col := color.RGBA{R: orangeSettings.ColorR, G: orangeSettings.ColorG, B: orangeSettings.ColorB, A: 255}
			swatch.FillColor = col
			swatch.Refresh()
			orangeViewport.histColor = [4]uint8{orangeSettings.ColorR, orangeSettings.ColorG, orangeSettings.ColorB, 255}
			refreshOrangePreview()
		}
		colorSlider := func(label string, value uint8, set func(uint8)) fyne.CanvasObject {
			slider := widget.NewSlider(0, 255)
			slider.Step = 1
			slider.Value = float64(value)
			valueLabel := widget.NewLabel(fmt.Sprintf("%d", value))
			slider.OnChanged = func(v float64) {
				n := uint8(math.Round(v))
				set(n)
				valueLabel.SetText(fmt.Sprintf("%d", n))
				updateSwatch()
			}
			return container.NewBorder(nil, nil, widget.NewLabel(label), valueLabel, slider)
		}
		opacitySlider := widget.NewSlider(0, 100)
		opacitySlider.Step = 1
		opacitySlider.Value = orangeSettings.Opacity * 100
		opacityValue := widget.NewLabel(fmt.Sprintf("%.0f%%", opacitySlider.Value))
		opacitySlider.OnChanged = func(v float64) {
			orangeSettings.Opacity = v / 100
			opacityValue.SetText(fmt.Sprintf("%.0f%%", v))
			refresh()
		}

		if imgs[3] != nil {
			applyChannelState(3, channelStateFromImage(imgs[3]), imgs, orangeViews, []*models.ChannelControl{nil, nil, nil, orangeControl})
		}
		colorControls := container.NewVBox(
			canvas.NewText("Overlay", color.RGBA{R: orangeSettings.ColorR, G: orangeSettings.ColorG, B: orangeSettings.ColorB, A: 255}),
			container.NewHBox(widget.NewLabel("Color"), swatch),
			colorSlider("Red", orangeSettings.ColorR, func(v uint8) { orangeSettings.ColorR = v }),
			colorSlider("Green", orangeSettings.ColorG, func(v uint8) { orangeSettings.ColorG = v }),
			colorSlider("Blue", orangeSettings.ColorB, func(v uint8) { orangeSettings.ColorB = v }),
			container.NewBorder(nil, nil, widget.NewLabel("Opacity"), opacityValue, opacitySlider),
		)
		controls := container.NewVScroll(container.NewVBox(orangeControl.Content, colorControls))
		controls.SetMinSize(fyne.NewSize(260, 200))
		orangeWin = app.NewWindow("Orange Image")
		orangeWin.SetContent(container.NewBorder(nil, nil, controls, nil, orangeViewport.container))
		orangeWin.Resize(fyne.NewSize(900, 600))
		orangeWin.SetCloseIntercept(func() {
			orangeWin.SetCloseIntercept(nil)
			orangeWin.Close()
			orangeWin = nil
			orangeViewport = nil
			orangeControl = nil
			refresh()
			if updateMenus != nil {
				updateMenus()
			}
		})
		refreshOrangePreview()
		orangeWin.Show()
	}

	controlSets = []*models.ChannelControl{
		channelControls("Channel 1 (Blue)", color.RGBA{R: 100, G: 149, B: 237, A: 255}, 0, imgs, &origPixels, viewports, refresh),
		channelControls("Channel 2 (Green)", color.RGBA{R: 80, G: 200, B: 80, A: 255}, 1, imgs, &origPixels, viewports, refresh),
		channelControls("Channel 3 (Red)", color.RGBA{R: 237, G: 80, B: 80, A: 255}, 2, imgs, &origPixels, viewports, refresh),
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
		if buildCompositeCheck.Checked {
			if img, ok := viewports[3].image.Image.(*image.RGBA); ok && img != nil {
				return img
			}
		}
		buf, w, h, _, _, err := composeRGBWithOptionalStarless()
		if err != nil || buf == nil {
			return nil
		}
		buf = processing.ApplyRGBLevels(buf, levels)
		if flipCheck.Checked {
			buf = processing.FlipRGBA(buf, w, h)
		}
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
			Flip:                 flipCheck.Checked,
			SharedHistogramScale: sharedHistCheck.Checked,
			DisableComposite:     !buildCompositeCheck.Checked,
			MeasureComposite:     measureEnabled,
			BlinkFilters:         blinkCheck.Checked,
			BlinkExcludedFilter:  blinkExcludedIdx,
			StarlessSettings:     starlessSettings,
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
		if orangeWin != nil {
			project.OrangeLayer = orangeSettings
			project.OrangeLayer.Open = true
			if imgs[3] != nil {
				hasChannel = true
				project.OrangeLayer.Channel = channelStateFromImage(imgs[3])
			}
		}
		if !hasChannel {
			dialog.ShowInformation("Nothing to save", "Load at least one channel before saving", win)
			return
		}
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			defer uc.Close()
			data, err := json.MarshalIndent(project, "", "  ")
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			if _, err := uc.Write(data); err != nil {
				dialog.ShowError(err, win)
				return
			}
		}, win)
		save.SetFileName("project.gfprj")
		save.SetFilter(storage.NewExtensionFileFilter([]string{".gfprj"}))
		save.Show()
	}

	loadProject := func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			defer r.Close()
			data, err := io.ReadAll(r)
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			var project models.ComposeProject
			if err := json.Unmarshal(data, &project); err != nil {
				dialog.ShowError(err, win)
				return
			}

			progressDialog := dialog.NewCustom("Loading Project", "Reading FITS files and restoring saved stretch settings...", widget.NewProgressBarInfinite(), win)
			progressDialog.Show()

			go func() {
				type loadResult struct {
					idx   int
					img   *models.LoadedImage
					state models.ChannelState
					err   error
				}

				results := make(chan loadResult, 4)
				var wg sync.WaitGroup

				for i := 0; i < 4; i++ {
					state := models.ChannelState{}
					if i < 3 {
						state = project.Channels[i]
					} else if project.OrangeLayer.Open {
						state = project.OrangeLayer.Channel
					}
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

				errors := make([]string, 0, 4)
				for res := range results {
					if res.state.Path == "" {
						imgs[res.idx] = nil
						continue
					}
					if res.err != nil {
						label := fmt.Sprintf("Channel %d", res.idx+1)
						if res.idx == 3 {
							label = "Orange"
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
								applyChannelState(i, project.Channels[i], imgs, viewports, controlSets)
							}
						}
						orangeSettings = project.OrangeLayer
						if orangeSettings.ColorR == 0 && orangeSettings.ColorG == 0 && orangeSettings.ColorB == 0 && orangeSettings.Opacity == 0 {
							orangeSettings = defaultOrangeLayerSettings()
						}
						flipCheck.SetChecked(project.Flip)
						sharedHistCheck.SetChecked(project.SharedHistogramScale)
						buildCompositeCheck.SetChecked(!project.DisableComposite)
						if stopBlink != nil {
							stopBlink()
						}
						blinkCheck.SetChecked(false)
						blinkExcludedIdx = clampComposeBlinkFilter(project.BlinkExcludedFilter)
						if blinkExcludeSelect != nil {
							blinkExcludeSelect.SetSelected(composeBlinkFilterNames[blinkExcludedIdx])
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
						starlessSettings = normalizeStarlessComposeSettings(project.StarlessSettings)
						// Keep saved starless settings for compatibility, but force the
						// experimental pipeline off while it is removed from the UI.
						starlessSettings.Enabled = false
					})
					debuglog.Log("load compose project: loaded images applied; refreshing previews asynchronously")
					for idx := range headerWins {
						closeHeaderWindow(idx)
					}
					if !project.OrangeLayer.Open && orangeWin != nil {
						orangeWin.SetCloseIntercept(nil)
						orangeWin.Close()
						orangeWin = nil
						orangeViewport = nil
						orangeControl = nil
					}
					if updateMenus != nil {
						updateMenus()
					}
					if project.OrangeLayer.Open {
						openOrangeWindow()
						if imgs[3] != nil && orangeControl != nil && orangeViewport != nil {
							orangeViews := []*viewport{nil, nil, nil, orangeViewport}
							applyChannelState(3, project.OrangeLayer.Channel, imgs, orangeViews, []*models.ChannelControl{nil, nil, nil, orangeControl})
							refreshOrangePreview()
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
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".gfprj"}))
		fd.SetView(dialog.ListView)
		fd.Show()
	}

	resetData := func() {
		loaded := false
		errors := make([]string, 0, 3)

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
				applyChannelState(i, state, imgs, viewports, controlSets)
			}
		})

		if !loaded {
			dialog.ShowInformation("Reset", "No loaded channels to reset.", win)
			return
		}

		refresh()
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

		// Reference is Channel 2 (green); align Channel 1 (blue) and Channel 3 (red)
		// to it. imgs always holds the ORIGINAL pixels (offsets are applied only at
		// render time), so the computed offset is absolute. Store it in the Manual
		// Offset fields (the source of truth); the refresh below renders it.
		width := imgs[1].HDU.Data.Width
		height := imgs[1].HDU.Data.Height
		refBase := imgs[1].HDU.Data.Pixels
		baseBlue := imgs[0].HDU.Data.Pixels
		baseRed := imgs[2].HDU.Data.Pixels
		bw, bh := imgs[0].HDU.Data.Width, imgs[0].HDU.Data.Height
		rw, rh := imgs[2].HDU.Data.Width, imgs[2].HDU.Data.Height

		progressDialog := dialog.NewCustom("Aligning", "Please wait...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			// alignOne returns the backward (output→source) transform that registers
			// base to the reference, using the same robust pixel-space star matcher
			// the mosaic builder uses (WCS-independent: channel WCS headers can
			// disagree with the real pixel registration by ~100 px).
			alignOne := func(base []float32, w, h int) (processing.AffineTransform, string, error) {
				_, t, stats, err := processing.AlignChannelByStars(base, w, h, refBase, width, height, 30.0, "general")
				if err != nil {
					return processing.AffineTransform{}, "", err
				}
				back, ierr := processing.InvertAffineTransform(t)
				if ierr != nil {
					return processing.AffineTransform{}, "", ierr
				}
				return back, fmt.Sprintf("matched=%d inliers=%d rms=%.2f", stats.MatchedStars, stats.GlobalInliers, stats.RMS), nil
			}

			backBlue, blueDetail, errBlue := alignOne(baseBlue, bw, bh)
			backRed, redDetail, errRed := alignOne(baseRed, rw, rh)

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

				blueLine := blueDetail
				var bdx, bdy, brot float64
				if errBlue == nil {
					bdx, bdy, brot = setAndApply(0, backBlue)
				} else {
					blueLine = "FAILED: " + errBlue.Error()
				}
				redLine := redDetail
				var rdx, rdy, rrot float64
				if errRed == nil {
					rdx, rdy, rrot = setAndApply(2, backRed)
				} else {
					redLine = "FAILED: " + errRed.Error()
				}

				refresh()

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

			fyne.Do(func() {
				win.Canvas().Refresh(win.Content())
				progressDialog.Hide()
				refresh()
				dialog.ShowInformation("Complete", fmt.Sprintf("Star masks generated and cosmic rays eradicated in the shared %dx%d region.", sharedWidth, sharedHeight), win)
			})
		}()
	}

	exportRGB := func() {
		buf, w, h, _, starlessResult, err := composeRGBWithOptionalStarless()
		if buf == nil {
			dialog.ShowInformation("Missing", "Load three FITS first", win)
			return
		}
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		finalBuf := processing.ApplyRGBLevels(buf, levels)
		// Pre-compute float32 channels for potential 16-bit PNG export.
		rF, gF, bF, _, _ := processing.ComposeRGBFloat32(imgs)
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
				if starlessSettings.Enabled && starlessSettings.ExportDebugMasks && starlessResult != nil {
					debugSettings := starlessDebugSettingsForRGBExport(path, format, opts)
					if err := processing.ExportStarlessDebug(starlessResult, debugSettings); err != nil {
						dialog.ShowError(err, win)
						return
					}
					debuglog.Log(fmt.Sprintf("exportRGB: wrote starless debug images to %s", debugSettings.Dir))
				}
			})
		}, win)
		save.SetFileName("composite.png")
		save.Show()
	}

	measureLabel := widget.NewLabel("Measure: --")
	measureLabel.TextStyle = fyne.TextStyle{Monospace: true}

	updateMeasurement = func() {
		viewports[3].setMeasurementOverlay(measureStart, measureEnd, flipCheck.Checked)
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
		point, ok := viewports[3].imagePointAtPosition(pos, flipCheck.Checked)
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
	var blinkMu sync.Mutex
	blinkSeq := 0
	blinkFrame := 0

	applyBlinkFrame := func() {
		if !blinkCheck.Checked {
			return
		}
		a, b := composeBlinkPair(blinkExcludedIdx)
		if a < 0 || b < 0 {
			return
		}
		srcIdx := a
		if blinkFrame%2 == 1 {
			srcIdx = b
		}
		src := viewports[srcIdx]
		dst := viewports[3]
		if src == nil || dst == nil || src.image == nil || src.image.Image == nil || src.origW == 0 || src.origH == 0 {
			if updateBlinkStatus != nil {
				updateBlinkStatus()
			}
			return
		}
		dst.image.Image = src.image.Image
		dst.origW, dst.origH = src.origW, src.origH
		dst.bins = src.bins
		dst.histMax = src.histMax
		if dst.StatsLabel != nil {
			dst.StatsLabel.SetText(fmt.Sprintf("Blink: %s", composeBlinkFilterNames[srcIdx]))
		}
		dst.histogram.Refresh()
		if dst.zoomLabel.Selected == "fit in preview" {
			dst.zoom = dst.fitZoom()
		}
		dst.applyZoom()
		dst.image.Refresh()
	}

	updateBlinkStatus = func() {
		a, b := composeBlinkPair(blinkExcludedIdx)
		if a < 0 || b < 0 {
			blinkStatus.SetText("Blink: choose one filter to turn off")
			return
		}
		if !blinkCheck.Checked {
			blinkStatus.SetText(fmt.Sprintf("Blink: off (%s would be excluded)", composeBlinkFilterNames[blinkExcludedIdx]))
			return
		}
		if imgs[a] == nil || imgs[b] == nil {
			blinkStatus.SetText(fmt.Sprintf("Blink: load %s and %s", composeBlinkFilterNames[a], composeBlinkFilterNames[b]))
			return
		}
		blinkStatus.SetText(fmt.Sprintf("Blink: %s <-> %s (%s off)", composeBlinkFilterNames[a], composeBlinkFilterNames[b], composeBlinkFilterNames[blinkExcludedIdx]))
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
	blinkExcludeSelect = NewSafeSelect(composeBlinkFilterNames, func(s string) {
		blinkExcludedIdx = composeBlinkFilterIndex(s)
		updateBlinkStatus()
		if blinkCheck.Checked {
			startBlink()
		}
	})
	blinkExcludeSelect.SetSelected(composeBlinkFilterNames[blinkExcludedIdx])
	updateBlinkStatus()

	//alignBtn := widget.NewButton("1. Align to Channel 2 (Green)", alignChannels)
	//crossCleanBtn := widget.NewButton("2. Cross-Channel Clean", crossChannelClean)

	saveProjectItem := fyne.NewMenuItem("Save Compose Project", saveProject)
	loadProjectItem := fyne.NewMenuItem("Load Compose Project", loadProject)
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
			for i := 0; i < 3; i++ {
				imgs[i] = nil
				clearComposeOrigPixels(&origPixels, i)
				viewports[i].image.Image = blankImg()
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

	copySettingsItem := fyne.NewMenuItem("Copy Channel 1 Settings to 2 & 3", copySettings)
	matchStretchItem := fyne.NewMenuItem("Match Channel Stretch...", showMatchStretchDialog)
	addOrangeItem := fyne.NewMenuItem("Add Orange Image...", openOrangeWindow)
	normalizeScaleItem := fyne.NewMenuItem("Normalize Scale to Channel 2", normalizeScale)
	sendToEditItem := fyne.NewMenuItem("Send Composite to Edit", sendToEdit)
	alignChannelsItem := fyne.NewMenuItem("Align to Channel 2", alignChannels)
	cleanChannelsItem := fyne.NewMenuItem("Cross-Channel Clean", crossChannelClean)
	resetDataItem := fyne.NewMenuItem("Reset Data (Undo Align & Clean)", resetData)
	// Starless Settings is intentionally omitted from the Compose menu.
	// The experimental starless/white-star code remains in the repository for
	// future investigation, but it should not be reachable from the UI for now.

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
		addOrangeItem,
	)
	// View: display tuning plus the per-channel FITS header viewers/savers.
	viewMenu := fyne.NewMenu("View",
		fyne.NewMenuItem("RGB Levels...", openLevels),
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

		saveProjectItem.Disabled = imgs[0] == nil && imgs[1] == nil && imgs[2] == nil && !(orangeWin != nil && imgs[3] != nil)
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
		imgs[channelIdx] = img
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

	controls := container.NewVBox(
		widget.NewLabel("Options"),
		container.NewHBox(flipCheck, widget.NewLabel("Flip image vertically")),
		container.NewHBox(sharedHistCheck, widget.NewLabel("Shared histogram scale")),
		histScaleStatus,
		container.NewHBox(buildCompositeCheck, widget.NewLabel("Build color composite")),
		container.NewHBox(blinkCheck, widget.NewLabel("Blink filters")),
		container.NewBorder(nil, nil, widget.NewLabel("Off"), nil, blinkExcludeSelect),
		blinkStatus,
		container.NewHBox(measureCheck, widget.NewLabel("Measure composite")),
		clearBtn,
		widget.NewSeparator(),
		measureLabel,
		widget.NewSeparator(),
		channelTabs,
	)

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

func channelControls(label string, col color.Color, idx int, imgs []*models.LoadedImage, origPixels *[][]float32, views []*viewport, refresh func()) *models.ChannelControl {
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
	selectBox.SetSelected("Linear")

	backgroundEntry := NewNumberEntry(0.001, 4)
	peakEntry := NewNumberEntry(0.001, 4)
	scaledPeakEntry := NewNumberEntry(1, 1)

	backgroundEntry.SetValue(0)
	peakEntry.SetValue(1)
	scaledPeakEntry.SetValue(1)

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

	return &models.ChannelControl{
		Content: container.NewVBox(
			func() fyne.CanvasObject {
				t := canvas.NewText(label, col)
				t.TextStyle = fyne.TextStyle{Bold: true}
				return t
			}(),
			selectBox,
			widget.NewForm(
				widget.NewFormItem("Background level", backgroundEntry),
				widget.NewFormItem("Peak level", peakEntry),
				widget.NewFormItem("Scaled peak level", scaledPeakEntry),
			),
			asinhRow,
			mtfRow,
			ghsDRow,
			ghsBRow,
			ghsSPRow,
			container.NewHBox(showClip, widget.NewLabel("Show clipped pixels")),
			container.NewHBox(auto, autoMTF, apply),
			widget.NewLabel("Manual Offset"),
			offsetRow("X", xOffsetEntry),
			offsetRow("Y", yOffsetEntry),
			offsetRow("Rot°", rotOffsetEntry),
			applyOffset,
			widget.NewSeparator(),
		),
		ModeSelect:       selectBox,
		BackgroundEntry:  backgroundEntry,
		PeakEntry:        peakEntry,
		ScaledPeakEntry:  scaledPeakEntry,
		AsinhScaleEntry:  asinhScaleEntry,
		MTFMidtoneEntry:  mtfMidtoneEntry,
		GHSStretchEntry:  ghsStretchEntry,
		GHSLocalEntry:    ghsLocalEntry,
		GHSSymmetryEntry: ghsSymmetryEntry,
		XOffsetEntry:     xOffsetEntry,
		YOffsetEntry:     yOffsetEntry,
		RotOffsetEntry:   rotOffsetEntry,
		ShowClip:         showClip,
	}
}

type composeViewportPreview struct {
	Image     *image.RGBA
	Bins      [256]int
	HistMax   int
	StatsText string
	Black     float64
	White     float64
	OrigW     int
	OrigH     int
}

type composePreviewData struct {
	Views          [4]composeViewportPreview
	RGBStats       [3]histogram.Stats
	StarlessResult *processing.StarlessResult
}

func buildComposePreviewData(imgs []*models.LoadedImage, flip bool, sharedHistScale bool, buildComposite bool, levels *models.RgbLevels, composeRGB func() ([]byte, int, int, [3]histogram.Stats, *processing.StarlessResult, error)) composePreviewData {
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
		if flip {
			stretched = processing.FlipImageData(stretched)
			mask = processing.FlipMask(mask, stretched.Width, stretched.Height)
		}
		stats := histogram.Compute(stretched.Pixels)
		sky, _ := processing.EstimateBackground(stretched.Pixels)
		channelPixels[i] = stretched.Pixels
		channelStats[i] = stats
		out.Views[i] = composeViewportPreview{
			Image:     processing.ToGrayRGBA(stretched, mask),
			Bins:      stats.Hist,
			StatsText: fmt.Sprintf("Sky %.3f  μ %.3f  σ %.3f", sky, stats.Mean, stats.Std),
			Black:     imgs[i].Black,
			White:     imgs[i].White,
			OrigW:     stretched.Width,
			OrigH:     stretched.Height,
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

	buf, w, h, rgbStats := processing.ComposeRGB(imgs)
	if composeRGB != nil {
		starlessStart := time.Now()
		if altBuf, altW, altH, altStats, starlessResult, err := composeRGB(); err == nil {
			buf, w, h, rgbStats = altBuf, altW, altH, altStats
			out.StarlessResult = starlessResult
			debuglog.Log(fmt.Sprintf("buildComposePreviewData: optional starless compose took %s", time.Since(starlessStart)))
		} else {
			debuglog.Log(fmt.Sprintf("buildComposePreviewData: optional starless compose failed after %s: %v", time.Since(starlessStart), err))
		}
	}
	if buf == nil {
		out.Views[3] = composeViewportPreview{Image: blankImg(), StatsText: "Sky --  μ --  σ --"}
		return out
	}
	out.RGBStats = rgbStats
	buf = processing.ApplyRGBLevels(buf, levels)
	if flip {
		buf = processing.FlipRGBA(buf, w, h)
	}
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

func defaultOrangeLayerSettings() models.OrangeLayerState {
	return models.OrangeLayerState{
		ColorR:  159,
		ColorG:  140,
		ColorB:  80,
		Opacity: 1,
	}
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
		views[i].histogram.Refresh()
		if views[i].zoomLabel.Selected == "fit in preview" {
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
func updatePreviews(imgs []*models.LoadedImage, views []*viewport, flip bool, levels *models.RgbLevels, pushHist func([3]histogram.Stats), composeRGB func() ([]byte, int, int, [3]histogram.Stats, *processing.StarlessResult, error)) {
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
		if flip {
			flipStart := time.Now()
			stretched = processing.FlipImageData(stretched)
			mask = processing.FlipMask(mask, stretched.Width, stretched.Height)
			debuglog.Log(fmt.Sprintf("updatePreviews: channel %d flip took %s", i+1, time.Since(flipStart)))
		}

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
		if views[i].zoomLabel.Selected == "fit in preview" {
			views[i].zoom = views[i].fitZoom()
		}
		views[i].applyZoom()
		views[i].image.Refresh()
		debuglog.Log(fmt.Sprintf("updatePreviews: channel %d total took %s", i+1, time.Since(channelStart)))
	}

	// NOTE: processing.ComposeRGB must be updated to return [3]histogram.Stats instead of [3][256]int
	composeStart := time.Now()
	buf, w, h, rgbStats := processing.ComposeRGB(imgs)
	debuglog.Log(fmt.Sprintf("updatePreviews: ComposeRGB took %s", time.Since(composeStart)))
	if composeRGB != nil {
		starlessStart := time.Now()
		if altBuf, altW, altH, altStats, _, err := composeRGB(); err == nil {
			buf, w, h, rgbStats = altBuf, altW, altH, altStats
			debuglog.Log(fmt.Sprintf("updatePreviews: optional starless compose took %s", time.Since(starlessStart)))
		} else {
			debuglog.Log(fmt.Sprintf("updatePreviews: optional starless compose failed after %s: %v", time.Since(starlessStart), err))
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

	if flip {
		buf = processing.FlipRGBA(buf, w, h)
	}
	copy(img.Pix, buf)
	debuglog.Log(fmt.Sprintf("updatePreviews: RGB levels/final image took %s", time.Since(rgbLevelsStart)))

	views[3].image.Image = img
	views[3].origW, views[3].origH = w, h
	views[3].bins = [256]int{}
	views[3].histMax = 0
	views[3].blackBox.SetValue(0)
	views[3].whiteBox.SetValue(0)
	views[3].histogram.Refresh()

	if views[3].zoomLabel.Selected == "fit in preview" {
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

func starlessDebugSettingsForRGBExport(path string, format export.Format, opts export.Options) processing.StarDebugExportSettings {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if strings.TrimSpace(base) == "" {
		base = "composite"
	}
	dir := filepath.Dir(path)
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	if format == "" {
		format = export.PNG
	}
	return processing.StarDebugExportSettings{
		Dir:     filepath.Join(dir, base),
		Prefix:  "starless",
		Format:  format,
		Options: opts,
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
