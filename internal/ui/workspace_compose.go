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
	var previewMu sync.Mutex
	previewSeq := 0

	flipCheck := NewToggle(nil)
	flipCheck.SetChecked(true)
	sharedHistCheck := NewToggle(nil)
	sharedHistCheck.SetChecked(false)
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
		data := buildComposePreviewData(imgs, flipCheck.Checked, sharedHistCheck.Checked, levels, composeRGBWithOptionalStarless)
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
		imgSnapshot := append([]*models.LoadedImage(nil), imgs...)
		levelsSnapshot := *levels
		flip := flipCheck.Checked
		sharedHistScale := sharedHistCheck.Checked
		go func() {
			start := time.Now()
			debuglog.Log("compose refresh async: starting preview computation")
			data := buildComposePreviewData(imgSnapshot, flip, sharedHistScale, &levelsSnapshot, composeRGBWithOptionalStarless)
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
		value, ok := composePixelValueAt(imgs[channel], point)
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
			value, ok := composePixelValueAt(imgs[idx], point)
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
		if starlessComposeTemporarilyDisabled {
			// Starless/white-star processing is intentionally disabled for now.
			// The implementation below remains in the codebase so it can be
			// revisited later, but Compose must not call it from the menu or
			// from saved project settings.
			if starlessSettings.Enabled {
				debuglog.Log("composeRGBWithOptionalStarless: starless temporarily disabled, using normal compose")
			}
			buf, w, h, stats := composeRGBCurrent(imgs)
			return buf, w, h, stats, nil, nil
		}

		if !starlessSettings.Enabled {
			debuglog.Log("composeRGBWithOptionalStarless: starless disabled, using normal compose")
			buf, w, h, stats := composeRGBCurrent(imgs)
			return buf, w, h, stats, nil, nil
		}
		if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
			debuglog.Log("composeRGBWithOptionalStarless: missing RGB channels")
			return nil, 0, 0, [3]histogram.Stats{}, nil, nil
		}
		debuglog.Log("composeRGBWithOptionalStarless: building aligned reference-grid channel set")
		ref := imgs[1]
		blueStretched := processing.StretchedImageDataForReferenceGrid(imgs[0], ref)
		greenStretched := processing.StretchedImageDataForReferenceGrid(imgs[1], ref)
		redStretched := processing.StretchedImageDataForReferenceGrid(imgs[2], ref)

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

	viewports[0].SetLoadSave("Blue", "B", color.RGBA{R: 100, G: 149, B: 237, A: 255},
		func() { loadChannel(0) }, func() { saveChannelGray(0) })
	viewports[1].SetLoadSave("Green", "G", color.RGBA{R: 80, G: 200, B: 80, A: 255},
		func() { loadChannel(1) }, func() { saveChannelGray(1) })
	viewports[2].SetLoadSave("Red", "R", color.RGBA{R: 237, G: 80, B: 80, A: 255},
		func() { loadChannel(2) }, func() { saveChannelGray(2) })
	viewports[3].SetCenterAction("Composite", "C", color.RGBA{R: 200, G: 110, B: 30, A: 255},
		"Export to Edit", func() {
			if globalExportToEdit == nil {
				return
			}
			img := viewports[3].image.Image
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

				controlSets[idx].ModeSelect.SetSelected(modeToLabel(src.Mode))
				controlSets[idx].BackgroundEntry.SetValue(src.Background)
				controlSets[idx].PeakEntry.SetValue(src.Peak)
				controlSets[idx].ScaledPeakEntry.SetValue(src.ScaledPeak)
				controlSets[idx].ShowClip.SetChecked(src.ShowClip)
				viewports[idx].blackBox.SetValue(src.Black)
				viewports[idx].whiteBox.SetValue(src.White)
			}
		})
		refresh()
	}

	saveProject := func() {
		hasChannel := false
		project := models.ComposeProject{
			Flip:                 flipCheck.Checked,
			SharedHistogramScale: sharedHistCheck.Checked,
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
			project.Channels[i] = models.ChannelState{
				Path:       imgs[i].Path,
				Mode:       modeToLabel(imgs[i].Mode),
				Black:      imgs[i].Black,
				White:      imgs[i].White,
				Background: imgs[i].Background,
				Peak:       imgs[i].Peak,
				ScaledPeak: imgs[i].ScaledPeak,
				ShowClip:   imgs[i].ShowClip,
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

		progressDialog := dialog.NewCustom("Aligning", "Please wait...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			refImg := imgs[1]
			width := refImg.HDU.Data.Width
			height := refImg.HDU.Data.Height

			alignWithFallback := func(target *models.LoadedImage) ([]float32, processing.AffineTransform, string, error) {
				aligned, transform, err := processing.AlignChannelUsingWCS(
					target.HDU.Data.Pixels,
					target.HDU.Data.Width,
					target.HDU.Data.Height,
					target.HDU.Header,
					refImg.HDU.Data.Pixels,
					width,
					height,
					refImg.HDU.Header,
				)
				if err == nil {
					return aligned, transform, "WCS", nil
				}
				aligned, transform, starErr := processing.AlignChannel(
					target.HDU.Data.Pixels,
					target.HDU.Data.Width,
					target.HDU.Data.Height,
					refImg.HDU.Data.Pixels,
					width,
					height,
				)
				if starErr != nil {
					return nil, processing.AffineTransform{}, "", fmt.Errorf("WCS failed: %v; star match failed: %w", err, starErr)
				}
				return aligned, transform, "stars", nil
			}

			alignedBlue, transformBlue, blueMethod, errBlue := alignWithFallback(imgs[0])
			alignedRed, transformRed, redMethod, errRed := alignWithFallback(imgs[2])

			if errBlue != nil || errRed != nil {
				errMsg := ""
				if errBlue != nil {
					errMsg += fmt.Sprintf("Channel 1 alignment failed: %v\n", errBlue)
				}
				if errRed != nil {
					errMsg += fmt.Sprintf("Channel 3 alignment failed: %v", errRed)
				}
				fyne.Do(func() {
					progressDialog.Hide()
					dialog.ShowError(fmt.Errorf("%s", errMsg), win)
				})
				return
			}

			imgs[0].HDU.Data.Pixels = alignedBlue
			imgs[0].HDU.Data.Width = width
			imgs[0].HDU.Data.Height = height
			clearComposeOrigPixels(&origPixels, 0)

			imgs[2].HDU.Data.Pixels = alignedRed
			imgs[2].HDU.Data.Width = width
			imgs[2].HDU.Data.Height = height
			clearComposeOrigPixels(&origPixels, 2)

			msg := fmt.Sprintf("Alignment Complete.\n\nBlue Method: %s\nBlue Shift:\n  X: %+.2f px\n  Y: %+.2f px\n\nRed Method: %s\nRed Shift:\n  X: %+.2f px\n  Y: %+.2f px",
				blueMethod,
				transformBlue.C, transformBlue.F,
				redMethod,
				transformRed.C, transformRed.F)
			fyne.Do(func() {
				progressDialog.Hide()
				refresh()
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
	fileMenu := fyne.NewMenu("File",
		loadProjectItem,
		saveProjectItem,
		fyne.NewMenuItemSeparator(),
		exportRGBItem,
	)

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
		img := viewports[3].image.Image
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
	addOrangeItem := fyne.NewMenuItem("Add Orange Image...", openOrangeWindow)
	normalizeScaleItem := fyne.NewMenuItem("Normalize Scale to Channel 2", normalizeScale)
	sendToEditItem := fyne.NewMenuItem("Send Composite to Edit", sendToEdit)
	channelsMenu := fyne.NewMenu("Channels",
		viewHeaderItems[0],
		viewHeaderItems[1],
		viewHeaderItems[2],
		fyne.NewMenuItemSeparator(),
		saveHeaderItems[0],
		saveHeaderItems[1],
		saveHeaderItems[2],
		fyne.NewMenuItemSeparator(),
		copySettingsItem,
		addOrangeItem,
		normalizeScaleItem,
		fyne.NewMenuItemSeparator(),
		sendToEditItem,
	)

	alignChannelsItem := fyne.NewMenuItem("Align to Channel 2", alignChannels)
	cleanChannelsItem := fyne.NewMenuItem("Cross-Channel Clean", crossChannelClean)
	// Starless Settings is intentionally omitted from the Process menu.
	// The experimental starless/white-star code remains in the repository for
	// future investigation, but it should not be reachable from the UI for now.
	postRGBCleanItem := fyne.NewMenuItem("Post-RGB Clean in Edit", sendToEdit)
	resetDataItem := fyne.NewMenuItem("Reset Data (Undo Align & Clean)", resetData)
	processMenu := fyne.NewMenu("Process",
		alignChannelsItem,
		cleanChannelsItem,
		postRGBCleanItem,
		fyne.NewMenuItemSeparator(),
		resetDataItem,
	)

	openLevels := func() {
		if levelsWin == nil {
			levelsWin = newRGBLevelsWindow(app, levels, refresh)
		}
		levelsWin.setHistogram(latestRGBStats)
		levelsWin.updateEntries()
		levelsWin.win.Show()
		levelsWin.win.RequestFocus()
	}
	viewMenu := fyne.NewMenu("View", fyne.NewMenuItem("RGB Levels...", openLevels))

	updateMenus = func() {
		for i := range viewHeaderItems {
			disabled := imgs[i] == nil
			viewHeaderItems[i].Disabled = disabled
			saveHeaderItems[i].Disabled = disabled
		}
		copySettingsItem.Disabled = imgs[0] == nil

		allLoaded := imgs[0] != nil && imgs[1] != nil && imgs[2] != nil

		normalizeScaleItem.Disabled = !allLoaded
		alignChannelsItem.Disabled = !allLoaded
		cleanChannelsItem.Disabled = !allLoaded
		postRGBCleanItem.Disabled = !allLoaded
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
	return split, []*fyne.Menu{fileMenu, channelsMenu, processMenu, viewMenu}
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

	controls[idx].ModeSelect.SetSelected(modeToLabel(img.Mode))
	controls[idx].BackgroundEntry.SetValue(img.Background)
	controls[idx].PeakEntry.SetValue(img.Peak)
	controls[idx].ScaledPeakEntry.SetValue(img.ScaledPeak)
	controls[idx].ShowClip.SetChecked(img.ShowClip)

	views[idx].blackBox.SetValue(img.Black)
	views[idx].whiteBox.SetValue(img.White)
}

func channelControls(label string, col color.Color, idx int, imgs []*models.LoadedImage, origPixels *[][]float32, views []*viewport, refresh func()) *models.ChannelControl {
	selectBox := widget.NewSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq"}, func(value string) {
		if imgs[idx] == nil {
			return
		}
		switch value {
		case "Linear":
			imgs[idx].Mode = stretch.Linear
		case "Log":
			imgs[idx].Mode = stretch.Log
		case "Asinh":
			imgs[idx].Mode = stretch.Asinh
		case "Sqrt":
			imgs[idx].Mode = stretch.Sqrt
		case "HistEq":
			imgs[idx].Mode = stretch.HistEq
		}
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

	xOffsetEntry := NewNumberEntry(1, 0)
	yOffsetEntry := NewNumberEntry(1, 0)
	rotOffsetEntry := NewNumberEntry(0.1, 1)

	var applyOffset *widget.Button
	applyOffset = widget.NewButton("Apply Offset", func() {
		if imgs[idx] == nil {
			return
		}
		if (*origPixels)[idx] == nil {
			src := imgs[idx].HDU.Data.Pixels
			cp := make([]float32, len(src))
			copy(cp, src)
			(*origPixels)[idx] = cp
		}
		dx := xOffsetEntry.Value()
		dy := yOffsetEntry.Value()
		rot := rotOffsetEntry.Value()
		w := imgs[idx].HDU.Data.Width
		h := imgs[idx].HDU.Data.Height
		cx := float64(w) / 2
		cy := float64(h) / 2
		rad := rot * math.Pi / 180
		cosA := math.Cos(rad)
		sinA := math.Sin(rad)
		t := processing.AffineTransform{
			A: cosA, B: sinA,
			C: -cosA*(cx+dx) - sinA*(cy+dy) + cx,
			D: -sinA, E: cosA,
			F: sinA*(cx+dx) - cosA*(cy+dy) + cy,
		}
		applyOffset.SetText("Working…")
		applyOffset.Disable()
		go func() {
			pixels := processing.WarpImage((*origPixels)[idx], w, h, t)
			fyne.Do(func() {
				imgs[idx].HDU.Data.Pixels = pixels
				refresh()
				applyOffset.SetText("Apply Offset")
				applyOffset.Enable()
			})
		}()
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
			container.NewHBox(showClip, widget.NewLabel("Show clipped pixels")),
			container.NewHBox(auto, apply),
			widget.NewLabel("Manual Offset"),
			offsetRow("X", xOffsetEntry),
			offsetRow("Y", yOffsetEntry),
			offsetRow("Rot°", rotOffsetEntry),
			applyOffset,
			widget.NewSeparator(),
		),
		ModeSelect:      selectBox,
		BackgroundEntry: backgroundEntry,
		PeakEntry:       peakEntry,
		ScaledPeakEntry: scaledPeakEntry,
		ShowClip:        showClip,
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

func buildComposePreviewData(imgs []*models.LoadedImage, flip bool, sharedHistScale bool, levels *models.RgbLevels, composeRGB func() ([]byte, int, int, [3]histogram.Stats, *processing.StarlessResult, error)) composePreviewData {
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
	default:
		return stretch.Linear
	}
}
