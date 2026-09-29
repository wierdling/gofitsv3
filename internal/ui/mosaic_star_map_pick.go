package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

// pickMissedStarsDialog lets the user open a drizzled FITS file and manually
// add stars the automatic Star Map detector missed. Existing catalog entries
// are shown as red circles so already-mapped stars are never re-picked.
func (ws *mosaicWorkspace) pickMissedStarsDialog() {
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
		ws.openPickMissedStars(path)
	}, ws.win)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
	fd.Show()
}

// scienceFromStarMapSidecar detects the user having selected the working/*_starmap.fits
// sidecar itself (easy to do since it sits next to the science FITS) and
// resolves the actual drizzle science file it belongs to, so callers don't
// compute a doubled "*_starmap_starmap.fits" path from it.
func scienceFromStarMapSidecar(path string) (string, bool) {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if !strings.HasSuffix(strings.TrimSuffix(base, ext), "_starmap") {
		return "", false
	}
	dir := filepath.Dir(path)
	if !strings.EqualFold(filepath.Base(dir), mosaic.WorkingDirName) {
		return "", false
	}
	stem := strings.TrimSuffix(strings.TrimSuffix(base, ext), "_starmap")
	parent := filepath.Dir(dir)
	for _, candidate := range []string{stem + "_drizzle" + ext, stem + ext} {
		full := filepath.Join(parent, candidate)
		if _, err := os.Stat(full); err == nil {
			return full, true
		}
	}
	return "", false
}

func (ws *mosaicWorkspace) openPickMissedStars(path string) {
	if science, ok := scienceFromStarMapSidecar(path); ok {
		path = science
	}
	ctx, cancel := context.WithCancel(context.Background())
	pt := newProgressTrackerWithContextOnUI("Pick Missed Stars", "Loading FITS and star map", ws.win, ctx, cancel)
	go func() {
		defer cancel()
		file, err := fitsio.LoadFile(path)
		var data fitsio.ImageData
		var header fitsio.Header
		if err == nil {
			data, header = file.HDUs[0].Data, file.HDUs[0].Header
		}
		mapPath := mosaic.StarMapWorkingPath(path)
		var product *mosaic.StarMapProduct
		if err == nil {
			product, err = mosaic.LoadStarMapFITS(ctx, mapPath, data, header)
		}
		pt.hide()
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(fmt.Errorf("load star map for %s (select the drizzle science FITS, not the working/*_starmap.fits sidecar; run Create Star Map first if none exists): %w", filepath.Base(path), err), ws.win)
				return
			}
			ws.showPickMissedStars(product, data, mapPath)
		})
	}()
}

func nextStarMapID(sources []processing.StarMapSource) int {
	max := 0
	for _, s := range sources {
		if s.ID > max {
			max = s.ID
		}
	}
	return max + 1
}

func (ws *mosaicWorkspace) showPickMissedStars(product *mosaic.StarMapProduct, data fitsio.ImageData, mapPath string) {
	win := ws.app.NewWindow("Pick Missed Stars — " + filepath.Base(mapPath))

	levels := autoLevelsForPixels(data.Pixels)
	img := buildMosaicPreviewImageWithLevels(&mosaic.Result{Pixels: data.Pixels, Width: data.Width, Height: data.Height},
		levels.Black, levels.White, levels.Background, levels.Peak, levels.ScaledPeak, stretch.Asinh, stretch.DefaultMTFMidtone)

	picker := newStarPickerWidget(img, data.Width, data.Height)
	picker.MaxStars = 1
	picker.ExistingStars = product.Map.Sources
	picker.SetZoom(1)

	status := widget.NewLabel("")
	status.Wrapping = fyne.TextWrapWord
	refreshStatus := func() {
		mode := "Left-click to add a missed star. Right-click a red circle to remove it."
		if picker.BoxSelectMode {
			mode = "Drag a box to clear all mapped stars inside it."
		}
		status.SetText(fmt.Sprintf("%d stars already mapped (red circles). %s", len(product.Map.Sources), mode))
	}
	refreshStatus()

	boxMode := widget.NewCheck("Box-clear mode", func(v bool) {
		picker.BoxSelectMode = v
		refreshStatus()
	})

	scroll := container.NewScroll(picker)
	zoom := widget.NewSlider(0.25, 6)
	zoom.Step = .25
	zoom.SetValue(1)
	zoom.OnChanged = func(v float64) { picker.SetZoom(v) }

	picker.OnChanged = func() {
		if len(picker.Stars) == 0 {
			return
		}
		click := picker.Stars[len(picker.Stars)-1]
		picker.Stars = nil
		picker.Refresh()
		ws.confirmMissedStar(win, product, data, click.X, click.Y, func() {
			picker.ExistingStars = product.Map.Sources
			picker.Refresh()
			refreshStatus()
		})
	}
	picker.OnExistingTapped = func(idx int) {
		if idx < 0 || idx >= len(product.Map.Sources) {
			return
		}
		s := product.Map.Sources[idx]
		removeThisStar := func() {
			dialog.ShowConfirm("Remove Star", fmt.Sprintf("Remove star ID %d at %.1f, %.1f from the map? This is not final until you Save.", s.ID, s.X, s.Y), func(ok bool) {
				if !ok {
					return
				}
				// idx was resolved against product.Map.Sources at tap time; nothing
				// else mutates that slice while this dialog is open, so it is still
				// the same star's index.
				product.Map.Sources = append(product.Map.Sources[:idx], product.Map.Sources[idx+1:]...)
				picker.ExistingStars = product.Map.Sources
				picker.Refresh()
				refreshStatus()
			}, win)
		}
		reason := s.Reason
		if reason == "" {
			reason = "(none recorded)"
		}
		override := s.Override
		if override == "" {
			override = "(automatic)"
		}
		info := widget.NewLabel(fmt.Sprintf(
			"ID: %d\nPosition: %.2f, %.2f\nStatus: %s · override: %s\nAccepted: %t\nFWHM: %.2f px · Radius: %.2f px\nSNR: %.1f · Residual: %.3f · Amplitude: %.1f\nSaturated: %t · Confirmations: %d / %d\nReason: %s",
			s.ID, s.X, s.Y, s.Status, override, s.Accepted(), s.FWHM, s.Radius, s.SNR, s.Residual, s.Amplitude, s.Saturated, s.Confirmed, s.Usable, reason))
		info.Wrapping = fyne.TextWrapWord
		d := dialog.NewCustomConfirm("Star Info", "Remove Star...", "Close", info, func(remove bool) {
			if remove {
				removeThisStar()
			}
		}, win)
		d.Show()
	}
	picker.OnBoxSelect = func(x0, y0, x1, y1 float64) {
		var toRemove []int
		for i, s := range product.Map.Sources {
			if s.X >= x0 && s.X <= x1 && s.Y >= y0 && s.Y <= y1 {
				toRemove = append(toRemove, i)
			}
		}
		if len(toRemove) == 0 {
			return
		}
		dialog.ShowConfirm("Clear Box", fmt.Sprintf("Remove %d star(s) in the selected box from the map? This is not final until you Save.", len(toRemove)), func(ok bool) {
			if !ok {
				return
			}
			for i := len(toRemove) - 1; i >= 0; i-- {
				idx := toRemove[i]
				product.Map.Sources = append(product.Map.Sources[:idx], product.Map.Sources[idx+1:]...)
			}
			picker.ExistingStars = product.Map.Sources
			picker.Refresh()
			refreshStatus()
		}, win)
	}

	var save *widget.Button
	save = widget.NewButton("Save", func() {
		save.Disable()
		ctx, cancel := context.WithCancel(context.Background())
		pt := newProgressTrackerWithContextOnUI("Save Star Map", "Writing updated catalog", win, ctx, cancel)
		go func() {
			defer cancel()
			err := mosaic.SaveStarMapFITS(ctx, mapPath, product, data.Pixels)
			pt.hide()
			fyne.Do(func() {
				save.Enable()
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						dialog.ShowError(err, win)
					}
					return
				}
				dialog.ShowInformation("Saved", "Updated star map catalog saved.", win)
				notifyStarMapSaved()
			})
		}()
	})

	top := container.NewBorder(nil, nil, widget.NewLabel("Zoom"), boxMode, zoom)
	win.SetContent(container.NewBorder(container.NewVBox(status, top), save, nil, nil, scroll))
	win.Resize(fyne.NewSize(900, 780))
	win.Show()
}

// confirmMissedStar evaluates a clicked position with the same detector used
// for automatic Star Map candidates, then asks the user whether to add it
// (at the raw click position or the refined centroid) before appending it to
// the map. onAdded is called after a star is appended.
func (ws *mosaicWorkspace) confirmMissedStar(win fyne.Window, product *mosaic.StarMapProduct, data fitsio.ImageData, clickX, clickY float64, onAdded func()) {
	measured := processing.MeasureStarMapSource(data.Pixels, data.Width, data.Height, clickX, clickY, nil, product.Map.Options)

	meets := measured.Accepted()
	summary := widget.NewLabel(fmt.Sprintf(
		"Raw click: %.2f, %.2f\nRefined centroid: %.2f, %.2f\nFWHM %.2f px · SNR %.1f · residual %.3f\nMeets detection thresholds: %t",
		clickX, clickY, measured.X, measured.Y, measured.FWHM, measured.SNR, measured.Residual, meets))
	summary.Wrapping = fyne.TextWrapWord

	options := []string{"Use refined centroid", "Use raw click position"}
	choice := widget.NewRadioGroup(options, nil)
	choice.SetSelected(options[0])

	warning := widget.NewLabel("")
	warning.Wrapping = fyne.TextWrapWord
	if !meets {
		warning.SetText("This spot does not meet the map's automatic detection thresholds; adding it will force-accept it as manually confirmed.")
	}

	content := container.NewVBox(summary, choice, warning)
	dialog.NewCustomConfirm("Add Missed Star", "Add Star", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		x, y := measured.X, measured.Y
		if choice.Selected == options[1] {
			x, y = clickX, clickY
		}
		source := measured
		source.ID = nextStarMapID(product.Map.Sources)
		source.X, source.Y = x, y
		source.Override = "accept"
		source.Reason = "manually added (missed by detector)"
		product.Map.Sources = append(product.Map.Sources, source)
		if onAdded != nil {
			onAdded()
		}
	}, win).Show()
}
