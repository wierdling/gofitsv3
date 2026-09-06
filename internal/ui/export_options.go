package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/export"
)

// showExportOptionsDialog presents format-specific save options and calls
// onConfirm with the chosen Options. onConfirm is not called if the user
// cancels. Formats without options call onConfirm immediately.
func showExportOptionsDialog(format export.Format, win fyne.Window, onConfirm func(export.Options), estimate ...func(int) int64) {
	switch format {
	case export.JPEG:
		quality := 90
		label := widget.NewLabel(fmt.Sprintf("Quality: %d", quality))
		sizeLabel := widget.NewLabel("")
		slider := widget.NewSlider(1, 100)
		slider.SetValue(float64(quality))
		slider.OnChanged = func(v float64) {
			quality = int(v)
			label.SetText(fmt.Sprintf("Quality: %d", quality))
		}
		if len(estimate) > 0 {
			var estimateGeneration uint64
			update := func(q int) {
				generation := atomic.AddUint64(&estimateGeneration, 1)
				go func() {
					time.Sleep(150 * time.Millisecond)
					n := estimate[0](q)
					fyne.Do(func() {
						if atomic.LoadUint64(&estimateGeneration) == generation {
							sizeLabel.SetText(fmt.Sprintf("Estimated size: %s", formatBytes(n)))
						}
					})
				}()
			}
			slider.OnChanged = func(v float64) { quality = int(v); label.SetText(fmt.Sprintf("Quality: %d", quality)); update(quality) }
			update(quality)
		}
		content := container.NewVBox(label, slider, sizeLabel)
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

func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}
func jpegEstimate(img image.Image, quality int) int64 {
	if img == nil {
		return 0
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: quality})
	return int64(b.Len())
}
