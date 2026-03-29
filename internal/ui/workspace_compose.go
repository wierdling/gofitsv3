package ui

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"math"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/export"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/render"
	"gofitsv3/internal/stretch"
)

type loadedImage struct {
	Path       string
	HDU        fitsio.HDU
	Primary    fitsio.Header
	Mode       stretch.Mode
	Black      float64
	White      float64
	Background float64
	Peak       float64
	ScaledPeak float64
	ShowClip   bool
}

type channelState struct {
	Path       string  `json:"path"`
	Mode       string  `json:"mode"`
	Black      float64 `json:"black"`
	White      float64 `json:"white"`
	Background float64 `json:"background"`
	Peak       float64 `json:"peak"`
	ScaledPeak float64 `json:"scaledPeak"`
	ShowClip   bool    `json:"showClip"`
}

type composeProject struct {
	Channels [3]channelState `json:"channels"`
	Flip     bool            `json:"flip"`
}

type channelControl struct {
	content         fyne.CanvasObject
	modeSelect      *widget.Select
	backgroundEntry *widget.Entry
	peakEntry       *widget.Entry
	scaledPeakEntry *widget.Entry
	showClip        *widget.Check
}

type viewport struct {
	image      *canvas.Image
	histogram  *canvas.Raster
	zoomLabel  *widget.Select
	zoomOut    *widget.Button
	zoomIn     *widget.Button
	blackBox   *widget.Entry
	whiteBox   *widget.Entry
	container  fyne.CanvasObject
	zoom       float64
	origW      int
	origH      int
	scroll     *container.Scroll
	bins       [256]int
	customZoom string
}

type rgbLevels struct {
	Min [3]float64
	Max [3]float64
}

type rgbLevelsWindow struct {
	win        fyne.Window
	levels     *rgbLevels
	bins       [3][256]int
	hists      [3]*canvas.Raster
	minEntries [3]*widget.Entry
	maxEntries [3]*widget.Entry
}

var presetZoomOptions = []string{"fit in preview", "1%", "5%", "10%", "20%", "25%", "50%", "75%", "100%", "200%", "300%"}

func newViewport() *viewport {
	img := canvas.NewImageFromImage(blankImg())
	img.FillMode = canvas.ImageFillContain

	vp := &viewport{image: img, zoom: 1}
	vp.histogram = canvas.NewRaster(vp.drawHist)
	vp.histogram.SetMinSize(fyne.NewSize(200, 48))

	drag := newDragLayer(nil, img)
	vp.scroll = container.NewScroll(container.NewMax(img, drag))
	drag.scroll = vp.scroll
	// Keep the previews compact so the compose form fits on smaller screens.
	vp.scroll.SetMinSize(fyne.NewSize(260, 180))

	vp.blackBox = widget.NewEntry()
	vp.blackBox.SetPlaceHolder("000000")
	vp.blackBox.SetText("--")
	vp.whiteBox = widget.NewEntry()
	vp.whiteBox.SetPlaceHolder("000000")
	vp.whiteBox.SetText("--")
	vp.zoomLabel = widget.NewSelect([]string{"fit in preview", "1%", "5%", "10%", "20%", "25%", "50%", "75%", "100%", "200%", "300%"}, func(s string) {
		vp.setZoomFromSelect(s)
	})
	vp.zoomOut = widget.NewButton("-", func() { vp.stepZoom(0.95) })
	vp.zoomIn = widget.NewButton("+", func() { vp.stepZoom(1.05) })

	header := container.NewVBox(
		vp.histogram,
		container.NewHBox(
			layout.NewSpacer(),
			widget.NewLabel("Black"),
			container.New(layout.NewGridWrapLayout(fyne.NewSize(110, vp.blackBox.MinSize().Height)), vp.blackBox),
			vp.zoomOut,
			vp.zoomLabel,
			vp.zoomIn,
			widget.NewLabel("White"),
			container.New(layout.NewGridWrapLayout(fyne.NewSize(110, vp.whiteBox.MinSize().Height)), vp.whiteBox),
			layout.NewSpacer(),
		),
	)
	vp.container = container.NewBorder(header, nil, nil, nil, vp.scroll)

	vp.zoomLabel.SetSelected("fit in preview")
	return vp
}

func isPresetZoom(option string) bool {
	for _, o := range presetZoomOptions {
		if o == option {
			return true
		}
	}
	return false
}

func (vp *viewport) setZoomLabelValue(option string) {
	if !isPresetZoom(option) {
		if vp.customZoom != "" {
			var opts []string
			for _, o := range vp.zoomLabel.Options {
				if o != vp.customZoom {
					opts = append(opts, o)
				}
			}
			vp.zoomLabel.Options = opts
		}
		vp.customZoom = option
		vp.zoomLabel.Options = append(vp.zoomLabel.Options, option)
	}
	vp.zoomLabel.SetSelected(option)
}
func (vp *viewport) setZoomFromSelect(sel string) {
	switch sel {
	case "fit in preview":
		vp.zoom = vp.fitZoom()
	default:
		sel = strings.TrimSuffix(sel, "%")
		if val, err := strconv.ParseFloat(sel, 64); err == nil {
			vp.zoom = val / 100.0
		}
	}
	vp.applyZoom()
}

func (vp *viewport) stepZoom(factor float64) {
	if vp.zoomLabel.Selected == "fit in preview" {
		// start from the current fitted zoom so increments are relative to the initial fit
		vp.zoom = vp.fitZoom()
	}
	vp.zoom *= factor
	vp.setZoomLabelValue(fmt.Sprintf("%d%%", int(math.Round(vp.zoom*100))))
	vp.applyZoom()
}

func (vp *viewport) applyZoom() {
	if vp == nil || vp.scroll == nil || vp.image == nil {
		return
	}
	if vp.zoom <= 0 {
		vp.zoom = 1
	}
	avail := vp.scroll.Size()
	if avail.Width <= 1 || avail.Height <= 1 {
		avail = fyne.NewSize(300, 300)
	}
	if vp.origW == 0 || vp.origH == 0 {
		vp.image.SetMinSize(avail)
		vp.image.Refresh()
		return
	}
	w := float32(vp.origW) * float32(vp.zoom)
	h := float32(vp.origH) * float32(vp.zoom)
	vp.image.SetMinSize(fyne.NewSize(w, h))
	vp.image.Refresh()
}
func (vp *viewport) fitZoom() float64 {
	if vp.origW == 0 || vp.origH == 0 {
		return 1
	}
	sz := vp.scroll.Size()
	if sz.Width <= 1 || sz.Height <= 1 {
		sz = fyne.NewSize(300, 300)
	}
	return math.Min(float64(sz.Width)/float64(vp.origW), float64(sz.Height)/float64(vp.origH))
}

func (vp *viewport) drawHist(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	maxCount := 0
	for _, c := range vp.bins {
		if c > maxCount {
			maxCount = c
		}
	}
	if maxCount == 0 {
		return img
	}
	for i, c := range vp.bins {
		x := i * w / len(vp.bins)
		barH := int(float64(c) / float64(maxCount) * float64(h))
		for y := h - 1; y >= h-barH; y-- {
			idx := (y*img.Stride + x*4)
			img.Pix[idx] = 80
			img.Pix[idx+1] = 80
			img.Pix[idx+2] = 80
			img.Pix[idx+3] = 255
		}
	}
	return img
}

func defaultRGBLevels() *rgbLevels {
	return &rgbLevels{
		Min: [3]float64{0, 0, 0},
		Max: [3]float64{255, 255, 255},
	}
}

func newRGBLevelsWindow(app fyne.App, levels *rgbLevels, onApply func()) *rgbLevelsWindow {
	w := &rgbLevelsWindow{levels: levels}
	colorBars := [3][3]uint8{
		{200, 60, 60},
		{60, 160, 60},
		{60, 100, 200},
	}
	labels := []string{"Red", "Green", "Blue"}
	var rows []fyne.CanvasObject
	for i := 0; i < 3; i++ {
		w.minEntries[i] = widget.NewEntry()
		w.maxEntries[i] = widget.NewEntry()
		w.hists[i] = canvas.NewRaster(w.drawHistFunc(i, colorBars[i]))
		w.hists[i].SetMinSize(fyne.NewSize(260, 70))
		rows = append(rows,
			widget.NewLabel(labels[i]),
			w.hists[i],
			container.NewGridWithColumns(4,
				widget.NewLabel("Min"),
				w.minEntries[i],
				widget.NewLabel("Max"),
				w.maxEntries[i],
			),
		)
	}
	w.updateEntries()
	applyBtn := widget.NewButton("Apply", func() {
		w.applyLevels(onApply)
	})
	info := widget.NewLabel("Levels operate on the composed RGB image (0-255).")
	content := container.NewVBox(rows...)
	w.win = app.NewWindow("RGB Levels")
	w.win.SetContent(container.NewBorder(nil, container.NewVBox(info, applyBtn), nil, nil, container.NewVScroll(content)))
	w.win.Resize(fyne.NewSize(380, 480))
	return w
}

func (w *rgbLevelsWindow) drawHistFunc(channel int, color [3]uint8) func(int, int) image.Image {
	return func(width, height int) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		for i := range img.Pix {
			img.Pix[i] = 255
		}
		maxCount := 0
		for _, c := range w.bins[channel] {
			if c > maxCount {
				maxCount = c
			}
		}
		if maxCount == 0 {
			return img
		}
		for i, c := range w.bins[channel] {
			x := i * width / len(w.bins[channel])
			barH := int(float64(c) / float64(maxCount) * float64(height))
			for y := height - 1; y >= height-barH; y-- {
				idx := (y*img.Stride + x*4)
				img.Pix[idx] = color[0]
				img.Pix[idx+1] = color[1]
				img.Pix[idx+2] = color[2]
				img.Pix[idx+3] = 255
			}
		}
		return img
	}
}

func (w *rgbLevelsWindow) setHistogram(bins [3][256]int) {
	w.bins = bins
	for _, h := range w.hists {
		if h != nil {
			h.Refresh()
		}
	}
}

func (w *rgbLevelsWindow) updateEntries() {
	for i := 0; i < 3; i++ {
		w.minEntries[i].SetText(fmt.Sprintf("%.0f", w.levels.Min[i]))
		w.maxEntries[i].SetText(fmt.Sprintf("%.0f", w.levels.Max[i]))
	}
}

func (w *rgbLevelsWindow) applyLevels(onApply func()) {
	changed := false
	for i := 0; i < 3; i++ {
		minVal := w.levels.Min[i]
		maxVal := w.levels.Max[i]
		if v, err := parseFloat(w.minEntries[i].Text); err == nil {
			minVal = clampLevel(v)
		}
		if v, err := parseFloat(w.maxEntries[i].Text); err == nil {
			maxVal = clampLevel(v)
		}
		if maxVal <= minVal {
			maxVal = minVal + 1
		}
		if minVal != w.levels.Min[i] || maxVal != w.levels.Max[i] {
			changed = true
		}
		w.levels.Min[i] = minVal
		w.levels.Max[i] = maxVal
	}
	w.updateEntries()
	if changed && onApply != nil {
		onApply()
	}
}

func newComposeWorkspace(app fyne.App, win fyne.Window) fyne.CanvasObject {
	imgs := make([]*loadedImage, 3)
	viewports := []*viewport{newViewport(), newViewport(), newViewport(), newViewport()}
	headerWins := make([]fyne.Window, 3)
	levels := defaultRGBLevels()
	var levelsWin *rgbLevelsWindow
	var latestRGBHist [3][256]int

	flipCheck := widget.NewCheck("Flip image vertically", func(bool) {})
	flipCheck.SetChecked(true)

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
		lines := formatHeadersLines(imgs[idx].Primary, imgs[idx].HDU.Header)
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
			img, err := loadImageFromPath(path)
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			imgs[idx] = img
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			refresh()
			closeHeaderWindow(idx)
			if updateMenus != nil {
				updateMenus()
			}
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

	controlSets := []*channelControl{
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

			controlSets[idx].modeSelect.SetSelected(modeToLabel(src.Mode))
			controlSets[idx].backgroundEntry.SetText(fmt.Sprintf("%.3f", src.Background))
			controlSets[idx].peakEntry.SetText(fmt.Sprintf("%.3f", src.Peak))
			controlSets[idx].scaledPeakEntry.SetText(fmt.Sprintf("%.3f", src.ScaledPeak))
			controlSets[idx].showClip.SetChecked(src.ShowClip)
			viewports[idx].blackBox.SetText(fmt.Sprintf("%.3f", src.Black))
			viewports[idx].whiteBox.SetText(fmt.Sprintf("%.3f", src.White))
		}
		refresh()
	}

	saveProject := func() {
		hasChannel := false
		project := composeProject{Flip: flipCheck.Checked}
		for i := 0; i < 3; i++ {
			if imgs[i] == nil {
				continue
			}
			hasChannel = true
			project.Channels[i] = channelState{
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
			var project composeProject
			if err := json.Unmarshal(data, &project); err != nil {
				dialog.ShowError(err, win)
				return
			}
			for i := 0; i < 3; i++ {
				state := project.Channels[i]
				if state.Path == "" {
					imgs[i] = nil
					continue
				}
				img, err := loadImageFromPath(state.Path)
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
	channelsMenu := fyne.NewMenu("Channels", copySettingsItem)

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
		saveProjectItem.Disabled = imgs[0] == nil && imgs[1] == nil && imgs[2] == nil
		win.SetMainMenu(fyne.NewMainMenu(fileMenu, headersMenu, channelsMenu, viewMenu))
	}
	updateMenus()

	exportBtn := widget.NewButton("Export RGB", func() {

		buf, w, h, _ := composeRGB(imgs)
		if buf == nil {
			dialog.ShowInformation("Missing", "Load three FITS first", win)
			return
		}
		finalBuf := applyRGBLevels(buf, levels)
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
		widget.NewSeparator(),
		widget.NewLabel("Per-channel controls"),
		controlSets[0].content,
		controlSets[1].content,
		controlSets[2].content,
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

func loadImageFromPath(path string) (*loadedImage, error) {
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
	minV, maxV := autoLevels(hdu.Data.Pixels)
	return &loadedImage{Path: path, HDU: hdu, Primary: file.HDUs[0].Header, Mode: stretch.Linear, Black: minV, White: maxV, Background: minV, Peak: maxV, ScaledPeak: maxV, ShowClip: true}, nil
}

func applyChannelState(idx int, state channelState, imgs []*loadedImage, views []*viewport, controls []*channelControl) {
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

	controls[idx].modeSelect.SetSelected(modeToLabel(img.Mode))
	controls[idx].backgroundEntry.SetText(fmt.Sprintf("%.3f", img.Background))
	controls[idx].peakEntry.SetText(fmt.Sprintf("%.3f", img.Peak))
	controls[idx].scaledPeakEntry.SetText(fmt.Sprintf("%.3f", img.ScaledPeak))
	controls[idx].showClip.SetChecked(img.ShowClip)

	views[idx].blackBox.SetText(fmt.Sprintf("%.3f", img.Black))
	views[idx].whiteBox.SetText(fmt.Sprintf("%.3f", img.White))
}

func channelControls(label string, idx int, imgs []*loadedImage, views []*viewport, refresh func(), flipCheck *widget.Check) *channelControl {
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
		if v, err := parseFloat(backgroundEntry.Text); err == nil {
			imgs[idx].Background = v
		}
		if v, err := parseFloat(peakEntry.Text); err == nil {
			imgs[idx].Peak = v
		}
		if v, err := parseFloat(scaledPeakEntry.Text); err == nil {
			imgs[idx].ScaledPeak = v
		}
		if v, err := parseFloat(views[idx].blackBox.Text); err == nil {
			imgs[idx].Black = v
		}
		if v, err := parseFloat(views[idx].whiteBox.Text); err == nil {
			imgs[idx].White = v
		}
		refresh()
	})

	auto := widget.NewButton("Auto scaling", func() {
		if imgs[idx] == nil {
			return
		}
		blackVal := imgs[idx].Black
		if v, err := parseFloat(views[idx].blackBox.Text); err == nil {
			blackVal = v
		}
		whiteVal := imgs[idx].White
		if v, err := parseFloat(views[idx].whiteBox.Text); err == nil {
			whiteVal = v
		} else {
			_, whiteVal = autoLevels(imgs[idx].HDU.Data.Pixels)
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

	return &channelControl{
		content: container.NewVBox(
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
		modeSelect:      selectBox,
		backgroundEntry: backgroundEntry,
		peakEntry:       peakEntry,
		scaledPeakEntry: scaledPeakEntry,
		showClip:        showClip,
	}
}

func updatePreviews(imgs []*loadedImage, views []*viewport, flip bool, levels *rgbLevels, pushHist func([3][256]int)) {
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
		stretched, mask := applyStretchParallel(imgs[i])
		if flip {
			stretched = flipImageData(stretched)
			mask = flipMask(mask, stretched.Width, stretched.Height)
		}
		views[i].image.Image = toGrayRGBA(stretched, mask)
		views[i].origW, views[i].origH = stretched.Width, stretched.Height
		views[i].bins, _, _ = histogram(stretched.Pixels)
		views[i].blackBox.SetText(fmt.Sprintf("%.3f", imgs[i].Black))
		views[i].whiteBox.SetText(fmt.Sprintf("%.3f", imgs[i].White))
		views[i].histogram.Refresh()
		if views[i].zoomLabel.Selected == "fit in preview" {
			views[i].zoom = views[i].fitZoom()
		}
		views[i].applyZoom()
		views[i].image.Refresh()
	}

	buf, w, h, rgbHist := composeRGB(imgs)
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
	buf = applyRGBLevels(buf, levels)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if flip {
		buf = flipRGBA(buf, w, h)
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

func composeRGB(imgs []*loadedImage) ([]byte, int, int, [3][256]int) {
	if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
		return nil, 0, 0, [3][256]int{}
	}
	w := imgs[2].HDU.Data.Width
	h := imgs[2].HDU.Data.Height
	// Channel mapping: 3->R, 2->G, 1->B
	rData, _ := applyStretchParallel(imgs[2])
	gData, _ := applyStretchParallel(imgs[1])
	bData, _ := applyStretchParallel(imgs[0])
	buf := render.ComposeRGB(rData.Pixels, gData.Pixels, bData.Pixels, w, h, imgs[2].Mode, imgs[1].Mode, imgs[0].Mode)
	return buf, w, h, histogramRGB(buf)
}

func applyRGBLevels(buf []byte, levels *rgbLevels) []byte {
	if buf == nil || levels == nil {
		return buf
	}
	out := make([]byte, len(buf))
	for i := 0; i+3 < len(buf); i += 4 {
		for c := 0; c < 3; c++ {
			val := float64(buf[i+c])
			minV := levels.Min[c]
			maxV := levels.Max[c]
			if maxV <= minV {
				out[i+c] = clampByte(maxV)
				continue
			}
			if val < minV {
				val = minV
			}
			if val > maxV {
				val = maxV
			}
			scaled := (val - minV) / (maxV - minV) * 255
			out[i+c] = clampByte(scaled)
		}
		out[i+3] = 255
	}
	return out
}

func histogramRGB(buf []byte) [3][256]int {
	var bins [3][256]int
	if len(buf) == 0 {
		return bins
	}
	for i := 0; i+3 < len(buf); i += 4 {
		bins[0][buf[i]]++
		bins[1][buf[i+1]]++
		bins[2][buf[i+2]]++
	}
	return bins
}

func clampLevel(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func clampByte(v float64) byte {
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return byte(math.Round(v))
}

func applyStretchParallel(img *loadedImage) (fitsio.ImageData, []byte) {
	data := img.HDU.Data
	numPixels := len(data.Pixels)
	pixels := make([]float64, numPixels)
	var mask []byte
	if img.ShowClip {
		mask = make([]byte, numPixels)
	}

	// 1. Pre-calculate constants (same as before)
	denom := img.Peak - img.Background
	if denom <= 0 {
		denom = 1
	}
	if img.ScaledPeak <= 0 {
		img.ScaledPeak = 1
	}
	stretchMul := img.ScaledPeak / denom

	invLogPeak := 1.0 / math.Log1p(img.ScaledPeak)
	invAsinhPeak := 1.0 / math.Asinh(img.ScaledPeak)
	invSqrtPeak := 1.0 / math.Sqrt(img.ScaledPeak)
	invLinearPeak := 1.0 / img.ScaledPeak

	// 2. Setup Parallelism
	numWorkers := runtime.NumCPU()
	chunkSize := (numPixels + numWorkers - 1) / numWorkers
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= numPixels {
			break
		}
		if end > numPixels {
			end = numPixels
		}

		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				v := data.Pixels[i]

				// Handle NaN
				if math.IsNaN(v) {
					if mask != nil {
						mask[i] = 3
					}
					pixels[i] = 0
					continue
				}

				// Clipping
				if v < img.Black {
					if mask != nil {
						mask[i] = 1
					}
					v = img.Black
				} else if v > img.White {
					if mask != nil {
						mask[i] = 2
					}
					v = img.White
				}

				val := (v - img.Background) * stretchMul
				if val < 0 {
					val = 0
				}

				// Stretch logic
				var result float64
				switch img.Mode {
				case stretch.Log:
					result = math.Log1p(val) * invLogPeak
				case stretch.Asinh:
					result = math.Asinh(val) * invAsinhPeak
				case stretch.Sqrt:
					result = math.Sqrt(val) * invSqrtPeak
				default: // Linear and HistEq pre-pass
					result = val * invLinearPeak
				}

				// Final Clamp and Assignment
				if result > 1.0 {
					result = 1.0
				}
				pixels[i] = result
			}
		}(start, end)
	}

	wg.Wait()

	// 3. Post-process Histogram Equalization if needed
	if img.Mode == stretch.HistEq {
		pixels = stretch.Apply(pixels, img.Mode)
	}

	return fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: pixels}, mask
}

func toGrayRGBA(data fitsio.ImageData, mask []byte) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, data.Width, data.Height))
	for i, v := range data.Pixels {
		idx := i * 4
		switch mask[i] {
		case 1:
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = 0, 0, 255
		case 2:
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = 0, 255, 0
		case 3:
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = 255, 0, 0
		default:
			b := byte(clamp01(v) * 255)
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = b, b, b
		}
		img.Pix[idx+3] = 255
	}
	return img
}

func histogram(pixels []float64) ([256]int, float64, float64) {
	var bins [256]int
	if len(pixels) == 0 {
		return bins, 0, 0
	}
	min, max := pixels[0], pixels[0]
	for _, v := range pixels {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		bin := int(clamp01(v) * 255)
		bins[bin]++
	}
	return bins, min, max
}

func blankImg() *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, 10, 10))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func autoLevels(pixels []float64) (float64, float64) {
	if len(pixels) == 0 {
		return 0, 1
	}
	min, max := pixels[0], pixels[0]
	for _, v := range pixels {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	if min == max {
		max = min + 1
	}
	return min, max
}

func flipImageData(data fitsio.ImageData) fitsio.ImageData {
	w, h := data.Width, data.Height
	out := make([]float64, len(data.Pixels))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			srcIdx := (h-1-y)*w + x
			dstIdx := y*w + x
			out[dstIdx] = data.Pixels[srcIdx]
		}
	}
	return fitsio.ImageData{Width: w, Height: h, Pixels: out}
}

func flipMask(mask []byte, w, h int) []byte {
	out := make([]byte, len(mask))
	for y := 0; y < h; y++ {
		copy(out[y*w:(y+1)*w], mask[(h-1-y)*w:(h-y)*w])
	}
	return out
}

func flipRGBA(buf []byte, w, h int) []byte {
	row := w * 4
	out := make([]byte, len(buf))
	for y := 0; y < h; y++ {
		copy(out[y*row:(y+1)*row], buf[(h-1-y)*row:(h-y)*row])
	}
	return out
}

type dragLayer struct {
	widget.BaseWidget
	scroll  *container.Scroll
	content fyne.CanvasObject
}

func newDragLayer(scroll *container.Scroll, content fyne.CanvasObject) *dragLayer {
	d := &dragLayer{scroll: scroll, content: content}
	d.ExtendBaseWidget(d)
	return d
}

func (d *dragLayer) Dragged(e *fyne.DragEvent) {
	if d.scroll == nil || d.content == nil {
		return
	}
	sz := d.content.Size()
	viewport := d.scroll.Size()
	maxX := float32(math.Max(0, float64(sz.Width-viewport.Width)))
	maxY := float32(math.Max(0, float64(sz.Height-viewport.Height)))
	nx := d.scroll.Offset.X - e.Dragged.DX
	ny := d.scroll.Offset.Y - e.Dragged.DY
	if nx < 0 {
		nx = 0
	}
	if ny < 0 {
		ny = 0
	}
	if nx > maxX {
		nx = maxX
	}
	if ny > maxY {
		ny = maxY
	}
	d.scroll.Offset = fyne.NewPos(nx, ny)
	d.scroll.Refresh()
}

func (d *dragLayer) DragEnd() {}

func (d *dragLayer) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(d.content)
}

func (d *dragLayer) Cursor() desktop.Cursor {
	return desktop.PointerCursor
}

func (d *dragLayer) MinSize() fyne.Size {
	if d.content == nil {
		return fyne.NewSize(10, 10)
	}
	return d.content.MinSize()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func formatHeadersLines(primary, sci fitsio.Header) []string {
	lines := make([]string, 0, len(primary.Cards)+len(sci.Cards)+4)
	lines = append(lines, "Primary header")
	for _, key := range sortedKeys(primary.Cards) {
		lines = append(lines, fmt.Sprintf("%-8s = %s", key, primary.Cards[key]))
	}
	lines = append(lines, "")
	lines = append(lines, "SCI header")
	for _, key := range sortedKeys(sci.Cards) {
		lines = append(lines, fmt.Sprintf("%-8s = %s", key, sci.Cards[key]))
	}
	return lines
}
