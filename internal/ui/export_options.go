package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/export"
)

// showExportOptionsDialog presents format-specific save options and calls
// onConfirm with the chosen Options. onConfirm is not called if the user
// cancels. Formats without options call onConfirm immediately.
func showExportOptionsDialog(format export.Format, win fyne.Window, onConfirm func(export.Options)) {
	switch format {
	case export.JPEG:
		quality := 90
		label := widget.NewLabel(fmt.Sprintf("Quality: %d", quality))
		slider := widget.NewSlider(1, 100)
		slider.SetValue(float64(quality))
		slider.OnChanged = func(v float64) {
			quality = int(v)
			label.SetText(fmt.Sprintf("Quality: %d", quality))
		}
		content := container.NewVBox(label, slider)
		d := dialog.NewCustomConfirm("JPEG Options", "Save", "Cancel", content, func(ok bool) {
			if ok {
				onConfirm(export.Options{Quality: quality})
			}
		}, win)
		d.Resize(fyne.NewSize(320, 120))
		d.Show()

	case export.PNG:
		bitDepth := 8
		check := widget.NewCheck("16-bit depth", func(checked bool) {
			if checked {
				bitDepth = 16
			} else {
				bitDepth = 8
			}
		})
		d := dialog.NewCustomConfirm("PNG Options", "Save", "Cancel", check, func(ok bool) {
			if ok {
				onConfirm(export.Options{BitDepth: bitDepth})
			}
		}, win)
		d.Resize(fyne.NewSize(280, 100))
		d.Show()

	case export.WEBP:
		label := widget.NewLabel("WebP lossless — no additional options.")
		d := dialog.NewCustomConfirm("WebP Options", "Save", "Cancel", label, func(ok bool) {
			if ok {
				onConfirm(export.Options{})
			}
		}, win)
		d.Resize(fyne.NewSize(320, 80))
		d.Show()

	default:
		onConfirm(export.Options{Quality: 92})
	}
}
