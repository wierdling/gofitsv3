package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

func (ws *mosaicWorkspace) createStarMapDialog() {
	if ws.starMapRunning {
		dialog.ShowInformation("Star Map", "A star-map job is already running.", ws.win)
		return
	}
	mode := widget.NewSelect([]string{"Current drizzle result", "Saved mosaic FITS", "Review saved map"}, nil)
	mode.SetSelected("Current drizzle result")
	if ws.state.result == nil {
		mode.SetSelected("Saved mosaic FITS")
	}
	file := widget.NewEntry()
	file.SetPlaceHolder("Select a linear mosaic FITS")
	browse := widget.NewButton("Browse…", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, ws.win)
				return
			}
			if r == nil {
				return
			}
			path := r.URI().Path()
			r.Close()
			file.SetText(path)
			mode.SetSelected("Saved mosaic FITS")
		}, ws.win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		fd.Show()
	})
	originals := widget.NewMultiLineEntry()
	originals.SetPlaceHolder("Optional: original FLT/FLC paths separated by commas")
	ref := widget.NewEntry()
	ref.SetPlaceHolder("Alignment reference FITS; defaults to selected mosaic")
	if ws.state.referenceInput != nil {
		ref.SetText(ws.state.referenceInput.Path)
	}
	snr, residual, fwhm := widget.NewEntry(), widget.NewEntry(), widget.NewEntry()
	defaults := processing.DefaultStarMapOptions()
	snr.SetText(strconv.FormatFloat(defaults.MinSNR, 'g', -1, 64))
	residual.SetText(strconv.FormatFloat(defaults.MaxResidual, 'g', -1, 64))
	fwhm.SetText("0")
	fwhm.SetPlaceHolder("0 = automatic estimation")
	mode.OnChanged = func(value string) {
		for _, entry := range []*widget.Entry{snr, residual, fwhm} {
			if value == "Review saved map" {
				entry.Disable()
			} else {
				entry.Enable()
			}
		}
	}
	note := widget.NewLabel("Creates a conservative soft FITS mask in working/. Uncertain objects remain unselected.\nCreate replaces the existing map; choose Review saved map to keep previous edits.\nCurrent results use captured WFC3 exposure evidence when available.\nSaved mosaics use mosaic-only detection unless originals and valid alignment sidecars are supplied.")
	note.Wrapping = fyne.TextWrapWord
	content := container.NewVBox(note, widget.NewForm(widget.NewFormItem("Source", mode), widget.NewFormItem("Mosaic", container.NewBorder(nil, nil, nil, browse, file)), widget.NewFormItem("Original exposures", originals), widget.NewFormItem("Alignment reference", ref), widget.NewFormItem("Min. SNR (3–100)", snr), widget.NewFormItem("Max. residual (0–1, exclusive of 0)", residual), widget.NewFormItem("FWHM (pixels; 0 = auto, max 8)", fwhm)))
	d := dialog.NewCustomConfirm("Create Star Map", "Create and Review", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		opt := processing.DefaultStarMapOptions()
		if mode.Selected != "Review saved map" {
			var err error
			opt, err = parseStarMapOptions(snr.Text, residual.Text, fwhm.Text)
			if err != nil {
				dialog.ShowError(err, ws.win)
				return
			}
		}
		var current *mosaic.Result
		var ev *mosaic.StarMapEvidence
		path := strings.TrimSpace(file.Text)
		if mode.Selected == "Current drizzle result" {
			current = ws.state.result
			if current == nil {
				dialog.ShowError(fmt.Errorf("build a drizzle result or select a saved mosaic"), ws.win)
				return
			}
			if current == ws.starMapBuildResult {
				ev = ws.starMapBuildEvidence
			}
			filter, dir, ok := ws.currentFilterAndDir()
			if !ok {
				dialog.ShowError(fmt.Errorf("current inputs must identify one source directory and filter; alternatively save and select the mosaic"), ws.win)
				return
			}
			path = filepath.Join(dir, filter+"_drizzle.fits")
		} else if path == "" {
			dialog.ShowError(fmt.Errorf("select a saved mosaic FITS"), ws.win)
			return
		}
		ws.startStarMap(current, path, ev, originals.Text, ref.Text, mode.Selected == "Review saved map", opt)
	}, ws.win)
	d.Resize(fyne.NewSize(760, 550))
	d.Show()
}

// parseStarMapOptions mirrors the benchmark ranges and rejects non-finite input
// before starting an expensive background job.
func parseStarMapOptions(snr, residual, fwhm string) (processing.StarMapOptions, error) {
	opt := processing.StarMapOptions{}
	fields := []struct {
		name, text string
		target     *float64
		min, max   float64
		positive   bool
	}{
		{"Min. SNR", snr, &opt.MinSNR, 3, 100, false},
		{"Max. residual", residual, &opt.MaxResidual, 0, 1, true},
		{"FWHM", fwhm, &opt.FWHM, 0, 8, false},
	}
	for _, field := range fields {
		value, err := strconv.ParseFloat(strings.TrimSpace(field.text), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < field.min || value > field.max || (field.positive && value == 0) {
			suffix := ""
			if field.positive {
				suffix = " (greater than zero)"
			}
			return processing.StarMapOptions{}, fmt.Errorf("%s must be a finite number in %g..%g%s", field.name, field.min, field.max, suffix)
		}
		*field.target = value
	}
	return opt, nil
}

func (ws *mosaicWorkspace) startStarMap(current *mosaic.Result, path string, evidence *mosaic.StarMapEvidence, originals, refPath string, reviewOnly bool, opt processing.StarMapOptions) {
	ws.starMapRunning = true
	ctx, cancel := context.WithCancel(context.Background())
	pt := newProgressTrackerWithContextOnUI("Star Map", "Reading linear science data", ws.win, ctx, cancel)
	go func() {
		defer cancel()
		var data fitsio.ImageData
		var header fitsio.Header
		var err error
		if current != nil {
			data = fitsio.ImageData{Width: current.Width, Height: current.Height, Pixels: current.Pixels}
			header = current.OutputHeader
		} else {
			var file *fitsio.File
			file, err = fitsio.LoadFile(path)
			if err == nil {
				data, header = file.HDUs[0].Data, file.HDUs[0].Header
			}
		}
		if !reviewOnly && err == nil && strings.TrimSpace(originals) != "" {
			if strings.TrimSpace(refPath) == "" {
				refPath = path
			}
			evidence, err = mosaic.LoadStarMapEvidence(data, header, strings.Split(originals, ","), refPath)
		}
		var product *mosaic.StarMapProduct
		if err == nil && !reviewOnly {
			opt.Progress = pt.progress
			product, err = mosaic.CreateStarMap(ctx, data, header, evidence, opt)
		}
		out := mosaic.StarMapWorkingPath(path)
		if err == nil && reviewOnly {
			product, err = mosaic.LoadStarMapFITS(ctx, out, data, header)
		}
		// A completed result is an independent snapshot; never attach it to a
		// different result after a rebuild/reload while this job was running.
		stale := false
		fyne.DoAndWait(func() { stale = current != nil && ws.state.result != current })
		if err == nil && stale {
			err = fmt.Errorf("mosaic changed during detection; run Star Map again")
		}
		if err == nil && !reviewOnly {
			pt.progress("Writing FITS mask and catalog", 0, 0)
			err = mosaic.SaveStarMapFITS(ctx, out, product, data.Pixels)
		}
		pt.hide()
		fyne.Do(func() {
			ws.starMapRunning = false
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					dialog.ShowError(err, ws.win)
				}
				return
			}
			ws.reviewStarMap(product, data, out)
		})
	}()
}

func (ws *mosaicWorkspace) reviewStarMap(product *mosaic.StarMapProduct, data fitsio.ImageData, path string) {
	win := ws.app.NewWindow("Star Map Review — " + filepath.Base(path))
	summary := widget.NewLabel("")
	summary.Wrapping = fyne.TextWrapWord
	updateSummary := func() {
		n := 0
		for _, s := range product.Map.Sources {
			if s.Accepted() {
				n++
			}
		}
		summary.SetText(fmt.Sprintf("%d selected / %d candidates · %s\n%s\nGreen: selected; amber: uncertain. Changes require Save.", n, len(product.Map.Sources), product.EvidenceMode, path) + "\n" + strings.Join(product.Warnings, "\n"))
	}
	updateSummary()
	preview := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 97, 97)))
	preview.FillMode = canvas.ImageFillContain
	preview.ScaleMode = canvas.ImageScalePixels
	preview.SetMinSize(fyne.NewSize(420, 420))
	detail := widget.NewLabel("Select a source to inspect its footprint.")
	detail.Wrapping = fyne.TextWrapWord
	selected := -1
	var refresh func()
	var list *starReviewList
	order := "Brightness (brightest first)"
	indices := starReviewOrder(product.Map.Sources, order)
	radius := widget.NewSlider(2, 30)
	radius.Step = .5
	syncing := false
	refresh = func() {
		if selected < 0 {
			return
		}
		s := product.Map.Sources[selected]
		preview.Image = starMapCutout(data, s)
		preview.Refresh()
		detail.SetText(fmt.Sprintf("ID %d · %s · override: %s\n%s\nFWHM %.2f px · residual %.3f · local contrast %.1f\nExposure confirmations: %d / %d · saturation: %t", s.ID, s.Status, s.Override, s.Reason, s.FWHM, s.Residual, s.SNR, s.Confirmed, s.Usable, s.Saturated))
		syncing = true
		radius.SetValue(s.Radius)
		syncing = false
		updateSummary()
	}
	radius.OnChanged = func(v float64) {
		if syncing || selected < 0 {
			return
		}
		product.Map.Sources[selected].Radius = v
		refresh()
	}
	list = newStarReviewList(func() int { return len(indices) }, func() fyne.CanvasObject { return widget.NewLabel("Star 0000 · uncertain") }, func(id widget.ListItemID, o fyne.CanvasObject) {
		s := product.Map.Sources[indices[id]]
		status := "uncertain"
		if s.Accepted() {
			status = "selected"
		}
		o.(*widget.Label).SetText(fmt.Sprintf("%d · %s", s.ID, status))
	})
	list.OnSelected = func(id widget.ListItemID) { selected = indices[id]; refresh() }
	decision := func(value string) {
		if selected < 0 {
			return
		}
		product.Map.Sources[selected].Override = value
		refresh()
		list.Refresh()
	}
	list.reviewKey = func(key *fyne.KeyEvent) {
		switch key.Name {
		case fyne.KeyLeft:
			decision("accept")
		case fyne.KeyRight:
			decision("reject")
		case fyne.KeyUp, fyne.KeyDown:
			row := starReviewNext(indices, selected, key.Name == fyne.KeyDown)
			if row >= 0 {
				list.Select(row)
				list.ScrollTo(row)
			}
		}
	}
	sortSelect := widget.NewSelect([]string{"Brightness (brightest first)", "Source ID", "Uncertain first", "Saturated first"}, func(value string) {
		order = value
		indices = starReviewOrder(product.Map.Sources, order)
		keep := selected
		list.UnselectAll()
		list.Refresh()
		for row, index := range indices {
			if index == keep {
				list.Select(row)
				list.ScrollTo(row)
				break
			}
		}
		// The popup restores focus to its select as it closes. Transfer focus
		// after dismissal so the next arrow navigates the star list.
		fyne.Do(func() { win.Canvas().Focus(list) })
	})
	sortSelect.SetSelected(order)
	win.Canvas().SetOnTypedKey(func(key *fyne.KeyEvent) {
		if win.Canvas().Focused() == nil {
			list.reviewKey(key)
		}
	})
	buttons := container.NewHBox(widget.NewButton("Accept", func() { decision("accept") }), widget.NewButton("Exclude", func() { decision("reject") }), widget.NewButton("Automatic", func() { decision("") }))
	var save *widget.Button
	save = widget.NewButton("Save FITS", func() {
		snapshot := *product
		mapping := *product.Map
		mapping.Sources = append([]processing.StarMapSource(nil), product.Map.Sources...)
		snapshot.Map = &mapping
		save.Disable()
		ctx, cancel := context.WithCancel(context.Background())
		pt := newProgressTrackerWithContextOnUI("Save Star Map", "Writing reviewed mask", win, ctx, cancel)
		go func() {
			defer cancel()
			err := mosaic.SaveStarMapFITS(ctx, path, &snapshot, data.Pixels)
			pt.hide()
			fyne.Do(func() {
				save.Enable()
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						dialog.ShowError(err, win)
					}
				} else {
					dialog.ShowInformation("Saved", "Reviewed FITS mask and source catalog saved in working/.", win)
				}
			})
		}()
	})
	right := container.NewBorder(nil, container.NewVBox(detail, widget.NewLabel("Footprint radius (pixels)"), radius, buttons), nil, nil, preview)
	split := container.NewHSplit(container.NewBorder(container.NewVBox(widget.NewLabel("Sort stars"), sortSelect), nil, nil, nil, list), right)
	split.Offset = .24
	win.SetContent(container.NewBorder(container.NewVBox(summary, widget.NewLabel("List keys: ↑/↓ move · ← accept · → exclude. Save FITS to keep changes.")), save, nil, nil, split))
	win.Resize(fyne.NewSize(840, 760))
	win.Show()
	if len(indices) > 0 {
		list.Select(0)
		win.Canvas().Focus(list)
	}
}

// Override focused-list arrow handling; other controls retain their own keys.
type starReviewList struct {
	*widget.List
	reviewKey func(*fyne.KeyEvent)
}

func newStarReviewList(length func() int, create func() fyne.CanvasObject, update func(widget.ListItemID, fyne.CanvasObject)) *starReviewList {
	// Extend before any constructor or renderer binds the embedded BaseWidget.
	// NewList would bind it to the inner list, causing two renderer identities.
	l := &starReviewList{List: &widget.List{Length: length, CreateItem: create, UpdateItem: update}}
	l.ExtendBaseWidget(l)
	return l
}

func (l *starReviewList) TypedKey(key *fyne.KeyEvent) {
	switch key.Name {
	case fyne.KeyUp, fyne.KeyDown, fyne.KeyLeft, fyne.KeyRight:
		if l.reviewKey != nil {
			l.reviewKey(key)
		}
	default:
		l.List.TypedKey(key)
	}
}
func starReviewNext(indices []int, selected int, down bool) int {
	if len(indices) == 0 {
		return -1
	}
	for row, index := range indices {
		if index == selected {
			if down {
				return min(row+1, len(indices)-1)
			}
			return max(row-1, 0)
		}
	}
	return 0
}
func starReviewOrder(sources []processing.StarMapSource, order string) []int {
	indices := make([]int, len(sources))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(i, j int) bool {
		a, b := sources[indices[i]], sources[indices[j]]
		switch order {
		case "Source ID":
			return a.ID < b.ID
		case "Uncertain first":
			au, bu := a.Status == "uncertain" && a.Override == "", b.Status == "uncertain" && b.Override == ""
			if au != bu {
				return au
			}
		case "Saturated first":
			if a.Saturated != b.Saturated {
				return a.Saturated
			}
		}
		if a.Amplitude != b.Amplitude {
			return a.Amplitude > b.Amplitude
		}
		return a.ID < b.ID
	})
	return indices
}

// Only a small source stamp is rendered on the UI thread; full-frame detection
// and FITS serialization always run in the background.
func starMapCutout(data fitsio.ImageData, s processing.StarMapSource) *image.RGBA {
	const size = 97
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	vals := make([]float64, 0, size*size)
	cx, cy := int(math.Round(s.X)), int(math.Round(s.Y))
	for y := cy - 48; y <= cy+48; y++ {
		for x := cx - 48; x <= cx+48; x++ {
			if x < 0 || y < 0 || x >= data.Width || y >= data.Height {
				continue
			}
			v := float64(data.Pixels[y*data.Width+x])
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				vals = append(vals, v)
			}
		}
	}
	if len(vals) == 0 {
		return out
	}
	sort.Float64s(vals)
	lo, hi := vals[len(vals)/10], vals[len(vals)*995/1000]
	scale := math.Max((hi-lo)/10, 1e-12)
	for yy := 0; yy < size; yy++ {
		for xx := 0; xx < size; xx++ {
			x, y := cx+xx-48, cy+yy-48
			if x < 0 || y < 0 || x >= data.Width || y >= data.Height {
				continue
			}
			v := float64(data.Pixels[y*data.Width+x])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			g := uint8(math.Max(0, math.Min(255, 255*math.Asinh(math.Max(0, v-lo)/scale)/math.Asinh(10))))
			c := color.RGBA{g, g, g, 255}
			r := math.Hypot(float64(x)-s.X, float64(y)-s.Y)
			if math.Abs(r-s.Radius) < .65 {
				c = color.RGBA{255, 180, 0, 255}
				if s.Accepted() {
					c = color.RGBA{0, 255, 80, 255}
				}
			}
			out.SetRGBA(xx, size-1-yy, c)
		}
	}
	return out
}
