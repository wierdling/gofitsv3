package ui

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"math"
	"path/filepath"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
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

func newComposeWorkspace(app fyne.App, win fyne.Window) fyne.CanvasObject {
	imgs := make([]*models.LoadedImage, 3)
	viewports := []*viewport{newViewport(), newViewport(), newViewport(), newViewport()}
	headerWins := make([]fyne.Window, 3)
	levels := defaultRGBLevels()
	var levelsWin *rgbLevelsWindow

	// Updated to track the new struct
	var latestRGBStats [3]histogram.Stats
	suspendRefresh := false

	flipCheck := widget.NewCheck("Flip image vertically", func(bool) {})
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
		format := export.PNG
		if len(path) >= 4 {
			switch path[len(path)-4:] {
			case ".png":
				format = export.PNG
			case ".tif":
				format = export.TIFF
			case "tiff":
				format = export.TIFF
			case ".jpg":
				format = export.JPEG
			case "jpeg":
				format = export.JPEG
			}
		}
		return format
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

			stretched, _ := processing.ApplyStretchParallel(imgs[idx])
			if flipCheck.Checked {
				stretched = processing.FlipImageData(stretched)
			}

			gray := processing.ToGrayRGBA(stretched, make([]byte, len(stretched.Pixels)))
			if err := export.FromImage(path, gray, detectExportFormat(path), export.Options{Quality: 92}); err != nil {
				dialog.ShowError(err, win)
			}
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

			progressDialog.Hide()
			refresh()
			dialog.ShowInformation("Complete", fmt.Sprintf("Rescaled %d channel(s) to match Channel 2 pixel scale.", resizedCount), win)
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
				if controlSets != nil {
					applyChannelState(idx, channelStateFromImage(img), imgs, viewports, controlSets)
				}

				progressDialog.Hide()
				refresh()
				closeHeaderWindow(idx)

				if updateMenus != nil {
					updateMenus()
				}
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
		channelControls("Channel 1 (Blue)", 0, imgs, viewports, refresh, saveChannelGray),
		channelControls("Channel 2 (Green)", 1, imgs, viewports, refresh, saveChannelGray),
		channelControls("Channel 3 (Red)", 2, imgs, viewports, refresh, saveChannelGray),
	}

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
				controlSets[idx].BackgroundEntry.SetText(fmt.Sprintf("%.3f", src.Background))
				controlSets[idx].PeakEntry.SetText(fmt.Sprintf("%.3f", src.Peak))
				controlSets[idx].ScaledPeakEntry.SetText(fmt.Sprintf("%.3f", src.ScaledPeak))
				controlSets[idx].ShowClip.SetChecked(src.ShowClip)
				viewports[idx].blackBox.SetText(fmt.Sprintf("%.3f", src.Black))
				viewports[idx].whiteBox.SetText(fmt.Sprintf("%.3f", src.White))
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
				withSuspendedRefresh(func() {
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
						applyChannelState(res.idx, res.state, imgs, viewports, controlSets)
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

			win.Canvas().Refresh(win.Content())

			if errBlue != nil || errRed != nil {
				progressDialog.Hide()
				errMsg := ""
				if errBlue != nil {
					errMsg += fmt.Sprintf("Channel 1 alignment failed: %v\n", errBlue)
				}
				if errRed != nil {
					errMsg += fmt.Sprintf("Channel 3 alignment failed: %v", errRed)
				}
				dialog.ShowError(fmt.Errorf("%s", errMsg), win)
				return
			}

			imgs[0].HDU.Data.Pixels = alignedBlue
			imgs[0].HDU.Data.Width = width
			imgs[0].HDU.Data.Height = height

			imgs[2].HDU.Data.Pixels = alignedRed
			imgs[2].HDU.Data.Width = width
			imgs[2].HDU.Data.Height = height

			progressDialog.Hide()
			refresh()
			msg := fmt.Sprintf("Alignment Complete.\n\nBlue Method: %s\nBlue Shift:\n  X: %+.2f px\n  Y: %+.2f px\n\nRed Method: %s\nRed Shift:\n  X: %+.2f px\n  Y: %+.2f px",
				blueMethod,
				transformBlue.C, transformBlue.F,
				redMethod,
				transformRed.C, transformRed.F)

			dialog.ShowInformation("Alignment Data", msg, win)
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

			win.Canvas().Refresh(win.Content())
			progressDialog.Hide()
			refresh()

			dialog.ShowInformation("Complete", "Master mask generated and cosmic rays eradicated.", win)
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
			_ = export.FromRGBABytes(path, finalBuf, w, h, detectExportFormat(path), export.Options{Quality: 92})
		}, win)
		save.SetFileName("composite.png")
		save.Show()
	}

	exportToEditBtn := widget.NewButton("Export to Edit", func() {
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

	//alignBtn := widget.NewButton("1. Align to Channel 2 (Green)", alignChannels)
	//crossCleanBtn := widget.NewButton("2. Cross-Channel Clean", crossChannelClean)

	saveProjectItem := fyne.NewMenuItem("Save Project", saveProject)
	loadProjectItem := fyne.NewMenuItem("Load Project", loadProject)
	fileMenu := fyne.NewMenu("File",
		loadProjectItem,
		saveProjectItem,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Load Channel 1", func() { loadChannel(0) }),
		fyne.NewMenuItem("Load Channel 2", func() { loadChannel(1) }),
		fyne.NewMenuItem("Load Channel 3", func() { loadChannel(2) }),
	)
	copySettingsItem := fyne.NewMenuItem("Copy Channel 1 settings to 2 & 3", copySettings)
	alignChannelsItem := fyne.NewMenuItem("Align to Channel 2", alignChannels)
	cleanChannelsItem := fyne.NewMenuItem("Cross-Channel Clean", crossChannelClean)

	//channelsMenu := fyne.NewMenu("Channels", copySettingsItem, alignChannelsItem, cleanChannelsItem)

	normalizeScaleItem := fyne.NewMenuItem("Normalize Scale to Channel 2", normalizeScale)
	resetDataItem := fyne.NewMenuItem("Reset Data (Undo Align & Clean)", resetData)
	exportRGBItem := fyne.NewMenuItem("Export RGB", exportRGB)
	processMenu := fyne.NewMenu("Process",
		copySettingsItem,
		normalizeScaleItem,
		alignChannelsItem,
		cleanChannelsItem,
		resetDataItem,
		fyne.NewMenuItemSeparator(),
		exportRGBItem,
	)

	viewHeaderItems := []*fyne.MenuItem{
		fyne.NewMenuItem("View Channel 1", func() { showHeader(0) }),
		fyne.NewMenuItem("View Channel 2", func() { showHeader(1) }),
		fyne.NewMenuItem("View Channel 3", func() { showHeader(2) }),
	}
	saveHeaderItems := []*fyne.MenuItem{
		fyne.NewMenuItem("Save Channel 1...", func() { saveHeader(0) }),
		fyne.NewMenuItem("Save Channel 2...", func() { saveHeader(1) }),
		fyne.NewMenuItem("Save Channel 3...", func() { saveHeader(2) }),
	}
	headersMenu := fyne.NewMenu("Headers",
		viewHeaderItems[0],
		viewHeaderItems[1],
		viewHeaderItems[2],
		fyne.NewMenuItemSeparator(),
		saveHeaderItems[0],
		saveHeaderItems[1],
		saveHeaderItems[2],
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

		// if allLoaded {
		// 	alignBtn.Enable()
		// 	crossCleanBtn.Enable()
		// } else {
		// 	alignBtn.Disable()
		// 	crossCleanBtn.Disable()
		// }

		saveProjectItem.Disabled = imgs[0] == nil && imgs[1] == nil && imgs[2] == nil
		win.SetMainMenu(fyne.NewMainMenu(fileMenu, headersMenu, processMenu, viewMenu))
	}
	updateMenus()

	// Register package-level callback so the preview window can inject an image
	// into any channel with its current stretch settings.
	globalSendToChannel = func(channelIdx int, img *models.LoadedImage) {
		if channelIdx < 0 || channelIdx >= 3 {
			return
		}
		imgs[channelIdx] = img
		applyChannelState(channelIdx, channelStateFromImage(img), imgs, viewports, controlSets)
		refresh()
		if updateMenus != nil {
			updateMenus()
		}
	}

	controls := container.NewVBox(
		widget.NewLabel("Options"),
		flipCheck,
		widget.NewSeparator(),
		//alignBtn,
		//crossCleanBtn,
		exportToEditBtn,
		widget.NewSeparator(),
		widget.NewLabel("Per-channel controls"),
		controlSets[0].Content,
		controlSets[1].Content,
		controlSets[2].Content,
	)

	controlsScroll := container.NewVScroll(controls)
	controlsScroll.SetMinSize(fyne.NewSize(260, 200))

	grid := container.NewGridWithColumns(2,
		viewports[0].container, viewports[1].container,
		viewports[2].container, viewports[3].container,
	)

	split := container.NewHSplit(controlsScroll, grid)
	split.SetOffset(0.32)
	return split
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
	controls[idx].BackgroundEntry.SetText(fmt.Sprintf("%.3f", img.Background))
	controls[idx].PeakEntry.SetText(fmt.Sprintf("%.3f", img.Peak))
	controls[idx].ScaledPeakEntry.SetText(fmt.Sprintf("%.3f", img.ScaledPeak))
	controls[idx].ShowClip.SetChecked(img.ShowClip)

	views[idx].blackBox.SetText(fmt.Sprintf("%.3f", img.Black))
	views[idx].whiteBox.SetText(fmt.Sprintf("%.3f", img.White))
}

func channelControls(label string, idx int, imgs []*models.LoadedImage, views []*viewport, refresh func(), saveChannelGray func(int)) *models.ChannelControl {
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

	backgroundEntry := widget.NewEntry()
	peakEntry := widget.NewEntry()
	scaledPeakEntry := widget.NewEntry()

	backgroundEntry.SetText("0")
	peakEntry.SetText("1")
	scaledPeakEntry.SetText("1")

	showClip := widget.NewCheck("Show clipped (blue/green/red)", func(v bool) {
		if imgs[idx] == nil {
			return
		}
		imgs[idx].ShowClip = v
		refresh()
	})
	showClip.SetChecked(true)

	apply := widget.NewButton("Apply values", func() {
		if imgs[idx] == nil {
			return
		}
		if v, err := utils.ParseFloat(backgroundEntry.Text); err == nil {
			imgs[idx].Background = v
		}
		if v, err := utils.ParseFloat(peakEntry.Text); err == nil {
			imgs[idx].Peak = v
		}
		if v, err := utils.ParseFloat(scaledPeakEntry.Text); err == nil {
			imgs[idx].ScaledPeak = v
		}
		if v, err := utils.ParseFloat(views[idx].blackBox.Text); err == nil {
			imgs[idx].Black = v
		}
		if v, err := utils.ParseFloat(views[idx].whiteBox.Text); err == nil {
			imgs[idx].White = v
		}
		refresh()
	})

	auto := widget.NewButton("Auto scaling", func() {
		if imgs[idx] == nil {
			return
		}
		blackVal := imgs[idx].Black
		if v, err := utils.ParseFloat(views[idx].blackBox.Text); err == nil {
			blackVal = v
		}
		whiteVal := imgs[idx].White
		if v, err := utils.ParseFloat(views[idx].whiteBox.Text); err == nil {
			whiteVal = v
		} else {
			_, whiteVal = processing.AutoLevels(imgs[idx].HDU.Data.Pixels)
		}
		imgs[idx].Background = blackVal
		imgs[idx].Peak = whiteVal
		imgs[idx].ScaledPeak = 10
		imgs[idx].White = whiteVal
		imgs[idx].Black = 0
		views[idx].blackBox.SetText("0")
		views[idx].whiteBox.SetText(fmt.Sprintf("%.2f", whiteVal))
		backgroundEntry.SetText(fmt.Sprintf("%.2f", blackVal))
		peakEntry.SetText(fmt.Sprintf("%.2f", whiteVal))
		scaledPeakEntry.SetText("10")
		refresh()
	})

	saveGray := widget.NewButton("Save Gray", func() {
		saveChannelGray(idx)
	})

	return &models.ChannelControl{
		Content: container.NewVBox(
			widget.NewLabel(label),
			selectBox,
			widget.NewForm(
				widget.NewFormItem("Background level", backgroundEntry),
				widget.NewFormItem("Peak level", peakEntry),
				widget.NewFormItem("Scaled peak level", scaledPeakEntry),
			),
			showClip,
			container.NewHBox(auto, apply, saveGray),
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
			views[i].blackBox.SetText("--")
			views[i].whiteBox.SetText("--")

			if views[i].StatsLabel != nil {
				views[i].StatsLabel.SetText("Mean: -- | Std: --")
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
			views[i].StatsLabel.SetText(fmt.Sprintf("Mean: %.4f | Std: %.4f", stats.Mean, stats.Std))
		}

		views[i].blackBox.SetText(fmt.Sprintf("%.3f", imgs[i].Black))
		views[i].whiteBox.SetText(fmt.Sprintf("%.3f", imgs[i].White))

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
		views[3].blackBox.SetText("--")
		views[3].whiteBox.SetText("--")
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
	views[3].blackBox.SetText("--")
	views[3].whiteBox.SetText("--")
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
