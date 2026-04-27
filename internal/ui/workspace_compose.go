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

	"gofitsv3/internal/export"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
	"gofitsv3/internal/utils"
)

// globalSendToChannel is registered by newComposeWorkspace and called by the
// preview window to load an image directly into a compose channel with all
// stretch settings already applied.
var globalSendToChannel func(channelIdx int, img *models.LoadedImage)

func newComposeWorkspace(app fyne.App, win fyne.Window) (fyne.CanvasObject, []*fyne.Menu) {
	imgs := make([]*models.LoadedImage, 3)
	var origPixels [3][]float32
	viewports := []*viewport{newViewport(), newViewport(), newViewport(), newViewport()}
	// Channel histograms: black background, channel-colored bars; compose: white bars.
	viewports[0].histColor = [4]uint8{100, 149, 237, 255} // blue
	viewports[1].histColor = [4]uint8{80, 200, 80, 255}   // green
	viewports[2].histColor = [4]uint8{237, 80, 80, 255}   // red
	viewports[3].histColor = [4]uint8{255, 255, 255, 255} // white (compose)
	headerWins := make([]fyne.Window, 3)
	levels := defaultRGBLevels()
	var levelsWin *rgbLevelsWindow

	// Updated to track the new struct
	var latestRGBStats [3]histogram.Stats
	suspendRefresh := false

	flipCheck := NewToggle(nil)
	flipCheck.SetChecked(true)

	// Updated signature to pass the stats
	pushRGBHist := func(stats [3]histogram.Stats) {
		latestRGBStats = stats
		if levelsWin != nil {
			levelsWin.setHistogram(stats)
		}
	}

	refresh := func() {
		if suspendRefresh {
			return
		}
		updatePreviews(imgs, viewports, flipCheck.Checked, levels, pushRGBHist)
	}
	withSuspendedRefresh := func(fn func()) {
		prev := suspendRefresh
		suspendRefresh = true
		defer func() { suspendRefresh = prev }()
		fn()
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
				origPixels[idx] = nil

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
		project := models.ComposeProject{Flip: flipCheck.Checked}
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
		save.SetFileName("project.gofits.json")
		save.SetFilter(storage.NewExtensionFileFilter([]string{".json", ".gofits"}))
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

				results := make(chan loadResult, 3)
				var wg sync.WaitGroup

				for i := 0; i < 3; i++ {
					state := project.Channels[i]
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

				errors := make([]string, 0, 3)
				for res := range results {
					if res.state.Path == "" {
						imgs[res.idx] = nil
						continue
					}
					if res.err != nil {
						errors = append(errors, fmt.Sprintf("Channel %d: %v", res.idx+1, res.err))
						continue
					}
					imgs[res.idx] = res.img
					origPixels[res.idx] = nil
				}

				fyne.Do(func() {
					withSuspendedRefresh(func() {
						for i, img := range imgs[:3] {
							if img != nil {
								applyChannelState(i, project.Channels[i], imgs, viewports, controlSets)
							}
						}
						flipCheck.SetChecked(project.Flip)
					})
					progressDialog.Hide()
					refresh()
					for idx := range headerWins {
						closeHeaderWindow(idx)
					}
					if updateMenus != nil {
						updateMenus()
					}
					if len(errors) > 0 {
						dialog.ShowError(fmt.Errorf("%s", strings.Join(errors, "\n")), win)
					}
				})
			}()
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".json", ".gofits"}))
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
				origPixels[i] = nil
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

			imgs[2].HDU.Data.Pixels = alignedRed
			imgs[2].HDU.Data.Width = width
			imgs[2].HDU.Data.Height = height

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

		progressDialog := dialog.NewCustom("Cleaning", "Building star mask and removing artifacts...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		go func() {
			width := imgs[1].HDU.Data.Width
			height := imgs[1].HDU.Data.Height

			var channels [][]float32
			var sigmas []float64

			for i := 0; i < 3; i++ {
				channels = append(channels, imgs[i].HDU.Data.Pixels)
				_, sig := processing.EstimateBackground(imgs[i].HDU.Data.Pixels)
				sigmas = append(sigmas, sig)
			}

			starMask := processing.BuildMasterMask(channels, width, height, sigmas)

			passes := 2

			cleanB := processing.RemoveCosmicRays(imgs[0].HDU.Data.Pixels, width, height, sigmas[0], passes, starMask)
			cleanG := processing.RemoveCosmicRays(imgs[1].HDU.Data.Pixels, width, height, sigmas[1], passes, starMask)
			cleanR := processing.RemoveCosmicRays(imgs[2].HDU.Data.Pixels, width, height, sigmas[2], passes, starMask)

			imgs[0].HDU.Data.Pixels = cleanB
			imgs[1].HDU.Data.Pixels = cleanG
			imgs[2].HDU.Data.Pixels = cleanR

			fyne.Do(func() {
				win.Canvas().Refresh(win.Content())
				progressDialog.Hide()
				refresh()
				dialog.ShowInformation("Complete", "Master mask generated and cosmic rays eradicated.", win)
			})
		}()
	}

	exportRGB := func() {
		buf, w, h, _ := processing.ComposeRGB(imgs)
		if buf == nil {
			dialog.ShowInformation("Missing", "Load three FITS first", win)
			return
		}
		finalBuf := processing.ApplyRGBLevels(buf, levels)
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			format := detectExportFormat(path)
			showExportOptionsDialog(format, win, func(opts export.Options) {
				_ = export.FromRGBABytes(path, finalBuf, w, h, format, opts)
			})
		}, win)
		save.SetFileName("composite.png")
		save.Show()
	}

	var measureEnabled bool
	var measureStart *imagePoint
	var measureEnd *imagePoint

	measureLabel := widget.NewLabel("Measure: --")
	measureLabel.TextStyle = fyne.TextStyle{Monospace: true}

	updateMeasurement := func() {
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

	measureCheck := NewToggle(func(v bool) {
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
				origPixels[i] = nil
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
		normalizeScaleItem,
		fyne.NewMenuItemSeparator(),
		sendToEditItem,
	)

	alignChannelsItem := fyne.NewMenuItem("Align to Channel 2", alignChannels)
	cleanChannelsItem := fyne.NewMenuItem("Cross-Channel Clean", crossChannelClean)
	resetDataItem := fyne.NewMenuItem("Reset Data (Undo Align & Clean)", resetData)
	processMenu := fyne.NewMenu("Process",
		alignChannelsItem,
		cleanChannelsItem,
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

		saveProjectItem.Disabled = imgs[0] == nil && imgs[1] == nil && imgs[2] == nil
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
		origPixels[channelIdx] = nil
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

	controls := container.NewVBox(
		widget.NewLabel("Options"),
		container.NewHBox(flipCheck, widget.NewLabel("Flip image vertically")),
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
	for i, vp := range viewports {
		rect := canvas.NewRectangle(color.Transparent)
		rect.StrokeColor = bColors[i]
		rect.StrokeWidth = 1
		rect.CornerRadius = 6
		borderRects[i] = rect
		borders[i] = container.NewMax(vp.container, rect)
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
		borders[i].Objects = []fyne.CanvasObject{viewports[i].container, borderRects[i]}
		borders[i].Refresh()
		maximizedIdx = -1
		split.Trailing = grid
		split.Refresh()
	}

	maximize = func(idx int) {
		if maximizedIdx >= 0 {
			i := maximizedIdx
			borders[i].Objects = []fyne.CanvasObject{viewports[i].container, borderRects[i]}
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

func channelControls(label string, col color.Color, idx int, imgs []*models.LoadedImage, origPixels *[3][]float32, views []*viewport, refresh func()) *models.ChannelControl {
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
		if origPixels[idx] == nil {
			src := imgs[idx].HDU.Data.Pixels
			cp := make([]float32, len(src))
			copy(cp, src)
			origPixels[idx] = cp
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
			pixels := processing.WarpImage(origPixels[idx], w, h, t)
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

// Updated signature to expect an array of histogram.Stats structs
func updatePreviews(imgs []*models.LoadedImage, views []*viewport, flip bool, levels *models.RgbLevels, pushHist func([3]histogram.Stats)) {
	for i := 0; i < 3; i++ {
		if imgs[i] == nil {
			views[i].image.Image = blankImg()
			views[i].bins = [256]int{}
			views[i].blackBox.SetValue(0)
			views[i].whiteBox.SetValue(0)

			if views[i].StatsLabel != nil {
				views[i].StatsLabel.SetText("μ --  σ --")
			}

			views[i].histogram.Refresh()
			views[i].image.Refresh()
			continue
		}

		stretched, mask := processing.ApplyStretchParallel(imgs[i])
		if flip {
			stretched = processing.FlipImageData(stretched)
			mask = processing.FlipMask(mask, stretched.Width, stretched.Height)
		}

		views[i].image.Image = processing.ToGrayRGBA(stretched, mask)
		views[i].origW, views[i].origH = stretched.Width, stretched.Height

		// Use the new struct to compute data
		stats := histogram.Compute(stretched.Pixels)
		views[i].bins = stats.Hist

		if views[i].StatsLabel != nil {
			views[i].StatsLabel.SetText(fmt.Sprintf("μ %.3f  σ %.3f", stats.Mean, stats.Std))
		}

		views[i].blackBox.SetValue(imgs[i].Black)
		views[i].whiteBox.SetValue(imgs[i].White)

		views[i].histogram.Refresh()
		if views[i].zoomLabel.Selected == "fit in preview" {
			views[i].zoom = views[i].fitZoom()
		}
		views[i].applyZoom()
		views[i].image.Refresh()
	}

	// NOTE: processing.ComposeRGB must be updated to return [3]histogram.Stats instead of [3][256]int
	buf, w, h, rgbStats := processing.ComposeRGB(imgs)

	if buf == nil {
		if pushHist != nil {
			pushHist([3]histogram.Stats{})
		}
		views[3].image.Image = blankImg()
		views[3].bins = [256]int{}
		views[3].blackBox.SetValue(0)
		views[3].whiteBox.SetValue(0)
		views[3].histogram.Refresh()
		views[3].image.Refresh()
		return
	}

	if pushHist != nil {
		pushHist(rgbStats)
	}

	buf = processing.ApplyRGBLevels(buf, levels)
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	if flip {
		buf = processing.FlipRGBA(buf, w, h)
	}
	copy(img.Pix, buf)

	views[3].image.Image = img
	views[3].origW, views[3].origH = w, h
	views[3].bins = [256]int{}
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
