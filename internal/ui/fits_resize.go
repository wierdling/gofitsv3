package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
)

// showBatchFITSResizePicker opens a FITS file picker. Fyne's file dialog is
// single-select, so further files can be added from the resize dialog.
func showBatchFITSResizePicker(app fyne.App, win fyne.Window) {
	picker := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		if reader == nil {
			return
		}
		path := reader.URI().Path()
		reader.Close()
		app.Preferences().SetString("lastDir", filepath.Dir(path))
		width, height, validateErr := fitsio.ValidateResizeInputs([]string{path}, 2)
		if validateErr != nil {
			dialog.ShowError(validateErr, win)
			return
		}
		if len(resizeFactors(width, height)) == 0 {
			dialog.ShowInformation("Cannot Resize FITS", "The selected image is too small for a power-of-two reduction.", win)
			return
		}
		showBatchFITSResizeDialog(app, win, []string{path})
	}, win)
	picker.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
	if lastDir := app.Preferences().String("lastDir"); lastDir != "" {
		if location, err := storage.ListerForURI(storage.NewFileURI(lastDir)); err == nil {
			picker.SetLocation(location)
		}
	}
	sizeFileDialog(picker)
	picker.Show()
}

func showBatchFITSResizeDialog(app fyne.App, win fyne.Window, paths []string) {
	if len(paths) == 0 {
		dialog.ShowInformation("No FITS Files", "Choose at least one FITS image to resize.", win)
		return
	}
	selected := make(map[string]bool, len(paths))
	for _, path := range paths {
		selected[path] = true
	}
	width, height, err := fitsio.ValidateResizeInputs([]string{paths[0]}, 2)
	if err != nil {
		dialog.ShowError(err, win)
		return
	}
	factors := resizeFactors(width, height)
	if len(factors) == 0 {
		dialog.ShowInformation("Cannot Resize FITS", "The selected image is too small for a power-of-two reduction.", win)
		return
	}
	options := make([]string, len(factors))
	for i, factor := range factors {
		options[i] = fmt.Sprintf("x%d (%d × %d)", factor, width/factor, height/factor)
	}
	factorSelect := widget.NewSelect(options, nil)
	factorSelect.SetSelected(options[0])
	discardNote := widget.NewLabel(resizeDiscardNote(width, height, factors[0]))
	factorSelect.OnChanged = func(_ string) {
		discardNote.SetText(resizeDiscardNote(width, height, factors[factorSelect.SelectedIndex()]))
	}
	rows := container.NewVBox()
	for _, path := range paths {
		path := path
		check := widget.NewCheck(filepath.Base(path), func(checked bool) { selected[path] = checked })
		check.SetChecked(true)
		rows.Add(check)
	}
	var resizeDialog *dialog.CustomDialog
	addFile := func() {
		picker := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			if reader == nil {
				return
			}
			path := reader.URI().Path()
			reader.Close()
			for _, existing := range paths {
				if existing == path {
					return
				}
			}
			app.Preferences().SetString("lastDir", filepath.Dir(path))
			paths = append(paths, path)
			selected[path] = true
			check := widget.NewCheck(filepath.Base(path), func(checked bool) { selected[path] = checked })
			check.SetChecked(true)
			rows.Add(check)
			rows.Refresh()
		}, win)
		picker.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
		if lastDir := app.Preferences().String("lastDir"); lastDir != "" {
			if location, err := storage.ListerForURI(storage.NewFileURI(lastDir)); err == nil {
				picker.SetLocation(location)
			}
		}
		sizeFileDialog(picker)
		picker.Show()
	}
	run := widget.NewButton("Resize", func() {
		chosen := make([]string, 0, len(paths))
		for _, path := range paths {
			if selected[path] {
				chosen = append(chosen, path)
			}
		}
		factor := factors[factorSelect.SelectedIndex()]
		if _, _, err := fitsio.ValidateResizeInputs(chosen, factor); err != nil {
			dialog.ShowError(err, win)
			return
		}
		resizeDialog.Hide()
		showBatchFITSResizeProgress(win, chosen, factor)
	})
	run.Importance = widget.HighImportance
	content := container.NewBorder(
		container.NewVBox(widget.NewLabel("Add same-size FITS files and choose a power-of-two reduction."), widget.NewForm(widget.NewFormItem("Reduction", factorSelect)), discardNote, widget.NewButton("Add FITS File...", addFile), widget.NewSeparator()),
		container.NewHBox(widget.NewButton("Cancel", func() { resizeDialog.Hide() }), run),
		nil, nil, container.NewVScroll(rows),
	)
	resizeDialog = dialog.NewCustomWithoutButtons("Resize FITS Files", content, win)
	resizeDialog.Resize(fyne.NewSize(520, 460))
	resizeDialog.Show()
}

func resizeFactors(width, height int) []int {
	factors := make([]int, 0, 4)
	for factor := 2; width/factor >= 1 && height/factor >= 1; factor *= 2 {
		factors = append(factors, factor)
	}
	return factors
}

func resizeDiscardNote(width, height, factor int) string {
	discardedWidth, discardedHeight := width%factor, height%factor
	if discardedWidth == 0 && discardedHeight == 0 {
		return "All source pixels are included."
	}
	parts := make([]string, 0, 2)
	if discardedWidth > 0 {
		parts = append(parts, fmt.Sprintf("rightmost %d column(s)", discardedWidth))
	}
	if discardedHeight > 0 {
		parts = append(parts, fmt.Sprintf("bottom %d row(s)", discardedHeight))
	}
	return "Discarded edge pixels: " + strings.Join(parts, " and ") + "."
}

func showBatchFITSResizeProgress(win fyne.Window, paths []string, factor int) {
	ctx, cancel := context.WithCancel(context.Background())
	bar := widget.NewProgressBar()
	status := widget.NewLabel("Preparing resize...")
	finished := false
	var progressDialog *dialog.CustomDialog
	progressDialog = dialog.NewCustomWithoutButtons("Resizing FITS Files", container.NewVBox(status, bar, widget.NewButton("Cancel", func() { cancel() })), win)
	progressDialog.SetOnClosed(func() {
		if !finished {
			cancel()
		}
	})
	progressDialog.Show()
	go func() {
		outputs, err := fitsio.ResizeFITSFiles(ctx, paths, factor, func(done, total int) {
			fyne.Do(func() {
				bar.SetValue(float64(done) / float64(total))
				status.SetText(fmt.Sprintf("Resized %d of %d files...", done, total))
			})
		})
		fyne.Do(func() {
			finished = true
			progressDialog.Hide()
			if err != nil {
				if ctx.Err() == nil {
					dialog.ShowError(err, win)
				}
				return
			}
			dialog.ShowInformation("Resize Complete", fmt.Sprintf("Created %d reduced FITS file(s).", len(outputs)), win)
		})
	}()
}
