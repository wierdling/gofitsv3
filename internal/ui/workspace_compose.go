package ui

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/export"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
	"gofitsv3/internal/utils"
)

func newComposeWorkspace(app fyne.App, win fyne.Window) fyne.CanvasObject {
	imgs := make([]*models.LoadedImage, 3)
	viewports := []*viewport{newViewport(), newViewport(), newViewport(), newViewport()}
	headerWins := make([]fyne.Window, 3)
	levels := defaultRGBLevels()
	var levelsWin *rgbLevelsWindow
	var latestRGBHist [3][256]int

	flipCheck := widget.NewCheck("Flip image vertically", func(bool) {})
	flipCheck.SetChecked(true)

	// New UI control for cosmic ray iterations
	crPassesSelect := widget.NewSelect([]string{"0", "1", "2", "3", "4", "5"}, func(string) {})
	crPassesSelect.SetSelected("2") // Default to 2 based on your testing

	pushRGBHist := func(bins [3][256]int) {
		latestRGBHist = bins
		if levelsWin != nil {
			levelsWin.setHistogram(bins)
		}
	}

	refresh := func() { updatePreviews(imgs, viewports, flipCheck.Checked, levels, pushRGBHist) }

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

	var updateMenus func()

	loadChannel := func(idx int) {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}

			path := r.URI().Path()
			app.Preferences().SetString("lastDir", filepath.Dir(path))

			// 1. Instantly show a progress dialog to acknowledge the click
			progressDialog := dialog.NewCustom(
				fmt.Sprintf("Loading Channel %d", idx+1),
				"Reading FITS and rejecting cosmic rays...",
				widget.NewProgressBarInfinite(),
				win,
			)
			progressDialog.Show()

			passes, _ := strconv.Atoi(crPassesSelect.Selected)

			// 2. Offload the heavy file I/O and math to a background thread
			go func() {
				img, loadErr := loadImageFromPath(path, passes)

				// 3. Handle errors and dismiss the dialog
				if loadErr != nil {
					progressDialog.Hide()
					dialog.ShowError(loadErr, win)
					return
				}

				// 4. Update the UI state with the loaded image
				imgs[idx] = img

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
		fd.Show()
	}

	controlSets := []*models.ChannelControl{
		channelControls("Channel 1", 0, imgs, viewports, refresh, flipCheck),
		channelControls("Channel 2", 1, imgs, viewports, refresh, flipCheck),
		channelControls("Channel 3", 2, imgs, viewports, refresh, flipCheck),
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
			uiPasses, _ := strconv.Atoi(crPassesSelect.Selected)
			for i := 0; i < 3; i++ {
				state := project.Channels[i]
				if state.Path == "" {
					imgs[i] = nil
					continue
				}
				img, err := loadImageFromPath(state.Path, uiPasses)
				if err != nil {
					dialog.ShowError(fmt.Errorf("channel %d: %w", i+1, err), win)
					continue
				}
				imgs[i] = img
				applyChannelState(i, state, imgs, viewports, controlSets)
			}
			flipCheck.SetChecked(project.Flip)
			refresh()
			for idx := range headerWins {
				closeHeaderWindow(idx)
			}
			if updateMenus != nil {
				updateMenus()
			}
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".json", ".gofits"}))
		fd.Show()
	}

	resetBtn := widget.NewButton("Reset Alignment", func() {
		hasReset := false
		for i := 0; i < 3; i++ {
			if imgs[i] != nil && imgs[i].OriginalPixels != nil {
				// Copy the pristine pixels back into the working HDU array
				copy(imgs[i].HDU.Data.Pixels, imgs[i].OriginalPixels)
				hasReset = true
			}
		}

		if !hasReset {
			dialog.ShowInformation("Reset", "No loaded channels to reset.", win)
			return
		}

		refresh()
		dialog.ShowInformation("Reset Complete", "Channels restored to original raw state.", win)
	})

	alignBtn := widget.NewButton("Align to Channel 2 (Green)", func() {
		if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
			dialog.ShowInformation("Missing Channels", "Load all three FITS channels before aligning.", win)
			return
		}

		// Display a loading dialog since alignment takes a moment
		progressDialog := dialog.NewCustom("Aligning", "Please wait...", widget.NewProgressBarInfinite(), win)
		progressDialog.Show()

		// Run processing in a goroutine so it doesn't freeze the Fyne UI thread
		go func() {
			refImg := imgs[1] // Channel 2 (Green) is the reference frame
			width := refImg.HDU.Data.Width
			height := refImg.HDU.Data.Height

			// Align Channel 1 (Blue) to Green
			alignedBlue, transformBlue, errBlue := processing.AlignChannel(imgs[0].HDU.Data.Pixels, refImg.HDU.Data.Pixels, width, height)

			// Align Channel 3 (Red) to Green
			alignedRed, transformRed, errRed := processing.AlignChannel(imgs[2].HDU.Data.Pixels, refImg.HDU.Data.Pixels, width, height)

			// Switch back to the UI thread to update the visuals
			win.Canvas().Refresh(win.Content()) // Force a quick UI tick

			if errBlue != nil || errRed != nil {
				progressDialog.Hide()
				errMsg := ""
				if errBlue != nil {
					errMsg += fmt.Sprintf("Channel 1 alignment failed: %v\n", errBlue)
				}
				if errRed != nil {
					errMsg += fmt.Sprintf("Channel 3 alignment failed: %v", errRed)
				}
				dialog.ShowError(fmt.Errorf(errMsg), win)
				return
			}

			// Apply the aligned data to the models
			imgs[0].HDU.Data.Pixels = alignedBlue
			imgs[2].HDU.Data.Pixels = alignedRed

			progressDialog.Hide()
			refresh() // Triggers the pipeline to redraw the histograms and composition
			msg := fmt.Sprintf("Alignment Complete.\n\nBlue Shift:\n  X: %+.2f px\n  Y: %+.2f px\n\nRed Shift:\n  X: %+.2f px\n  Y: %+.2f px",
				transformBlue.C, transformBlue.F,
				transformRed.C, transformRed.F)

			dialog.ShowInformation("Alignment Data", msg, win)
		}()
	})

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
	alignChannelsItem := fyne.NewMenuItem("Align to Channel 2", func() {
		// Trigger the button's action
		alignBtn.OnTapped()
	})

	// Update this existing line:
	channelsMenu := fyne.NewMenu("Channels", copySettingsItem, alignChannelsItem)

	headerItems := []*fyne.MenuItem{
		fyne.NewMenuItem("Channel 1", func() { showHeader(0) }),
		fyne.NewMenuItem("Channel 2", func() { showHeader(1) }),
		fyne.NewMenuItem("Channel 3", func() { showHeader(2) }),
	}
	headersMenu := fyne.NewMenu("Headers", headerItems...)
	openLevels := func() {
		if levelsWin == nil {
			levelsWin = newRGBLevelsWindow(app, levels, refresh)
		}
		levelsWin.setHistogram(latestRGBHist)
		levelsWin.updateEntries()
		levelsWin.win.Show()
		levelsWin.win.RequestFocus()
	}
	viewMenu := fyne.NewMenu("View", fyne.NewMenuItem("RGB Levels...", openLevels))
	updateMenus = func() {
		for i, item := range headerItems {
			item.Disabled = imgs[i] == nil
		}
		copySettingsItem.Disabled = imgs[0] == nil

		allLoaded := imgs[0] != nil && imgs[1] != nil && imgs[2] != nil
		alignChannelsItem.Disabled = !allLoaded
		if allLoaded {
			alignBtn.Enable()
		} else {
			alignBtn.Disable()
		}

		saveProjectItem.Disabled = imgs[0] == nil && imgs[1] == nil && imgs[2] == nil
		win.SetMainMenu(fyne.NewMainMenu(fileMenu, headersMenu, channelsMenu, viewMenu))
	}
	updateMenus()

	exportBtn := widget.NewButton("Export RGB", func() {
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
			_ = export.FromRGBABytes(path, finalBuf, w, h, format, export.Options{Quality: 92})
		}, win)
		save.SetFileName("composite.png")
		save.Show()
	})

	controls := container.NewVBox(
		widget.NewLabel("Options"),
		flipCheck,
		container.NewBorder(nil, nil, widget.NewLabel("Cosmic Ray Passes:"), nil, crPassesSelect),
		widget.NewSeparator(),
		widget.NewLabel("Per-channel controls"),
		controlSets[0].Content,
		controlSets[1].Content,
		controlSets[2].Content,
		alignBtn,
		resetBtn,
		exportBtn,
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

func loadImageFromPath(path string, crPasses int) (*models.LoadedImage, error) {
	file, err := fitsio.LoadFile(path)
	if err != nil {
		return nil, err
	}
	sci := file.SelectSCI()
	hdu := file.HDUs[0]
	if len(sci) == 1 {
		hdu = sci[0]
	} else if len(sci) > 1 {
		hdu = sci[0]
	}
	if cleaned, err := cleanHDUWithDQ(hdu, file); err == nil {
		hdu = cleaned
	}
	// 1. Calculate global noise for this specific image
	_, sigma := processing.EstimateBackground(hdu.Data.Pixels)

	// Pass the iteration count into the algorithm
	cleanPixels := processing.RemoveCosmicRays(hdu.Data.Pixels, hdu.Data.Width, hdu.Data.Height, sigma, crPasses)
	hdu.Data.Pixels = cleanPixels

	// 3. Calculate levels on the now-clean data
	minV, maxV := processing.AutoLevels(hdu.Data.Pixels)

	// Create a hard copy of the pristine (but cleaned) pixels for the reset button
	orig := make([]float64, len(hdu.Data.Pixels))
	copy(orig, hdu.Data.Pixels)

	return &models.LoadedImage{
		Path:           path,
		HDU:            hdu,
		Primary:        file.HDUs[0].Header,
		Mode:           stretch.Linear,
		Black:          minV,
		White:          maxV,
		Background:     minV,
		Peak:           maxV,
		ScaledPeak:     maxV,
		ShowClip:       true,
		OriginalPixels: orig, // Store the pristine copy
	}, nil
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

func channelControls(label string, idx int, imgs []*models.LoadedImage, views []*viewport, refresh func(), flipCheck *widget.Check) *models.ChannelControl {
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
			container.NewHBox(auto, apply),
			widget.NewSeparator(),
		),
		ModeSelect:      selectBox,
		BackgroundEntry: backgroundEntry,
		PeakEntry:       peakEntry,
		ScaledPeakEntry: scaledPeakEntry,
		ShowClip:        showClip,
	}
}

func updatePreviews(imgs []*models.LoadedImage, views []*viewport, flip bool, levels *models.RgbLevels, pushHist func([3][256]int)) {
	for i := 0; i < 3; i++ {
		if imgs[i] == nil {
			views[i].image.Image = blankImg()
			views[i].bins = [256]int{}
			views[i].blackBox.SetText("--")
			views[i].whiteBox.SetText("--")
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
		views[i].bins, _, _ = processing.Histogram(stretched.Pixels)
		views[i].blackBox.SetText(fmt.Sprintf("%.3f", imgs[i].Black))
		views[i].whiteBox.SetText(fmt.Sprintf("%.3f", imgs[i].White))
		views[i].histogram.Refresh()
		if views[i].zoomLabel.Selected == "fit in preview" {
			views[i].zoom = views[i].fitZoom()
		}
		views[i].applyZoom()
		views[i].image.Refresh()
	}

	buf, w, h, rgbHist := processing.ComposeRGB(imgs)
	if buf == nil {
		if pushHist != nil {
			pushHist([3][256]int{})
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
		pushHist(rgbHist)
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
