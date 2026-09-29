package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"strconv"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// showComposePSFDialog measures all loaded RGB channels and optionally applies
// Gaussian matching. It intentionally operates only on normal in-memory data;
// disk-backed Compose displays an actionable limitation instead.
func showComposePSFDialog(win fyne.Window, imgs []*models.LoadedImage, refresh func(), largeMode bool, settings *models.PSFSettings) {
	if largeMode {
		dialog.ShowInformation("PSF matching unavailable", "PSF matching currently requires in-memory channel pixels. Disk-backed Compose is unchanged.", win)
		return
	}
	measurements := make([]processing.PSFMeasurement, 3)
	labels := make([]*widget.Label, 3)
	before, after := make([]image.Image, 3), make([]image.Image, 3)
	labelObjects := make([]fyne.CanvasObject, 3)
	for i := 0; i < 3; i++ {
		labels[i] = widget.NewLabel(fmt.Sprintf("Channel %d: not measured", i+1))
		labelObjects[i] = labels[i]
		if i < len(imgs) && imgs[i] != nil {
			m := processing.MeasurePSF(imgs[i].HDU.Data.Pixels, imgs[i].HDU.Data.Width, imgs[i].HDU.Data.Height, 4, imageMaximum(imgs[i]))
			before[i] = composeStarCrop(imgs[i])
			measurements[i] = m
			labels[i].SetText(fmt.Sprintf("Channel %d: FWHM %.2f × %.2f px (%d stars)", i+1, m.FWHMX, m.FWHMY, m.Samples))
		}
	}
	target := processing.SuggestPSFTarget(measurements)
	x := widget.NewEntry()
	y := widget.NewEntry()
	x.SetText(strconv.FormatFloat(target.FWHMX, 'f', 2, 64))
	y.SetText(strconv.FormatFloat(target.FWHMY, 'f', 2, 64))
	protect := widget.NewCheck("Protect saturated stars", nil)
	protect.SetChecked(settings != nil && settings.ProtectSaturated)
	content := container.NewVBox(widget.NewLabel("Measured PSF (before)"), container.NewVBox(labelObjects...), container.NewGridWithColumns(4, widget.NewLabel("Target X"), x, widget.NewLabel("Target Y"), y), protect, widget.NewLabel("Sharper channels are convolved to the target. A before/after measurement is shown after Apply."))
	d := dialog.NewCustomConfirm("Match Channel PSF", "Apply", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		tx, _ := strconv.ParseFloat(x.Text, 64)
		ty, _ := strconv.ParseFloat(y.Text, 64)
		if tx <= 0 || ty <= 0 {
			return
		}
		for i, img := range imgs {
			if img == nil || i >= len(measurements) {
				continue
			}
			out, err := processing.ConvolveToPSF(context.Background(), img.HDU.Data.Pixels, img.HDU.Data.Width, img.HDU.Data.Height, measurements[i], processing.PSFTarget{FWHMX: tx, FWHMY: ty}, protect.Checked, imageMaximum(img))
			if err == nil {
				after[i] = composeStarCropPixels(out, img.HDU.Data.Width, img.HDU.Data.Height)
				after := processing.MeasurePSF(out, img.HDU.Data.Width, img.HDU.Data.Height, 4, imageMaximum(img))
				labels[i].SetText(fmt.Sprintf("Channel %d: %.2f × %.2f → %.2f × %.2f px", i+1, measurements[i].FWHMX, measurements[i].FWHMY, after.FWHMX, after.FWHMY))
			}
		}
		if settings != nil {
			maxSat := 0.0
			for _, image := range imgs {
				if m := imageMaximum(image); m > maxSat {
					maxSat = m
				}
			}
			*settings = models.PSFSettings{Enabled: true, TargetFWHMX: tx, TargetFWHMY: ty, ProtectSaturated: protect.Checked, Saturation: maxSat}
		}
		if refresh != nil {
			refresh()
		}
		showPSFAfterDialog(win, labels, before, after)
	}, win)
	d.Show()
}

func imageMaximum(img *models.LoadedImage) float64 {
	max := 0.0
	if img != nil {
		for _, v := range img.HDU.Data.Pixels {
			f := float64(v)
			if !math.IsNaN(f) && !math.IsInf(f, 0) && f > max {
				max = f
			}
		}
	}
	return max
}

func showPSFAfterDialog(win fyne.Window, labels []*widget.Label, before, after []image.Image) {
	content := container.NewVBox(widget.NewLabel("PSF matching preview (after)"))
	for i, l := range labels {
		content.Add(l)
		if i < len(before) && before[i] != nil && after[i] != nil {
			content.Add(container.NewGridWithColumns(2, canvas.NewImageFromImage(before[i]), canvas.NewImageFromImage(after[i])))
		}
	}
	dialog.ShowCustom("Before / after star preview", "Close", content, win)
}

func composeStarCrop(img *models.LoadedImage) image.Image {
	if img == nil {
		return nil
	}
	return composeStarCropPixels(img.HDU.Data.Pixels, img.HDU.Data.Width, img.HDU.Data.Height)
}
func composeStarCropPixels(p []float32, w, h int) image.Image {
	if w <= 0 || h <= 0 || len(p) < w*h {
		return nil
	}
	max, idx := float32(0), 0
	for i, v := range p {
		if v > max {
			max = v
			idx = i
		}
	}
	cx, cy := idx%w, idx/w
	out := image.NewGray(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			sx := cx + (x-32)/2
			sy := cy + (y-32)/2
			v := float32(0)
			if sx >= 0 && sy >= 0 && sx < w && sy < h {
				v = p[sy*w+sx]
			}
			if v < 0 {
				v = 0
			}
			if max > 0 {
				v /= max
			}
			if v > 1 {
				v = 1
			}
			out.SetGray(x, y, color.Gray{Y: uint8(v * 255)})
		}
	}
	return out
}

func showComposeLRGBDialog(win fyne.Window, settings *models.LRGBSettings, refresh func(), settingsMu *sync.RWMutex) {
	enabled := widget.NewCheck("Enable LRGB combination", nil)
	enabled.SetChecked(settings.Enabled)
	weight := widget.NewEntry()
	weight.SetText(strconv.FormatFloat(settings.LuminanceWeight, 'f', 2, 64))
	smooth := widget.NewEntry()
	smooth.SetText(strconv.FormatFloat(settings.ChrominanceSmoothing, 'f', 2, 64))
	weights := settings.SyntheticWeights
	if weights == [3]float64{} {
		weights = [3]float64{.2126, .7152, .0722}
	}
	// The persisted weight slots double as source-selection state: a zero
	// slot excludes that filter from synthetic luminance.
	rSource := widget.NewCheck("R", nil)
	gSource := widget.NewCheck("G", nil)
	bSource := widget.NewCheck("B", nil)
	rSource.SetChecked(weights[0] != 0)
	gSource.SetChecked(weights[1] != 0)
	bSource.SetChecked(weights[2] != 0)
	path := widget.NewEntry()
	path.SetText(settings.DedicatedLPath)
	path.SetPlaceHolder("Optional dedicated L input path")
	content := container.NewVBox(enabled, container.NewGridWithColumns(2, widget.NewLabel("L contribution (0–1)"), weight, widget.NewLabel("Chrominance smoothing"), smooth), widget.NewLabel("Synthetic luminance sources"), container.NewHBox(bSource, gSource, rSource), widget.NewLabel("Synthetic luminance uses the selected RGB filters; a dedicated L path is reserved for a loaded L channel."), path)
	d := dialog.NewCustomConfirm("LRGB Combination", "Apply", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		w, _ := strconv.ParseFloat(weight.Text, 64)
		s, _ := strconv.ParseFloat(smooth.Text, 64)
		if w < 0 {
			w = 0
		}
		if w > 1 {
			w = 1
		}
		if s < 0 {
			s = 0
		}
		selected := weights
		if !rSource.Checked {
			selected[0] = 0
		}
		if !gSource.Checked {
			selected[1] = 0
		}
		if !bSource.Checked {
			selected[2] = 0
		}
		canonical := [3]float64{.2126, .7152, .0722}
		if rSource.Checked && selected[0] <= 0 {
			selected[0] = canonical[0]
		}
		if gSource.Checked && selected[1] <= 0 {
			selected[1] = canonical[1]
		}
		if bSource.Checked && selected[2] <= 0 {
			selected[2] = canonical[2]
		}
		if selected == [3]float64{} {
			dialog.ShowError(errors.New("select at least one synthetic luminance source (B, G, or R)"), win)
			return
		}
		if settingsMu != nil {
			settingsMu.Lock()
		}
		*settings = models.LRGBSettings{Enabled: enabled.Checked, LuminanceWeight: w, ChrominanceSmoothing: s, DedicatedLPath: path.Text, SyntheticWeights: selected}
		if settingsMu != nil {
			settingsMu.Unlock()
		}
		if refresh != nil {
			refresh()
		}
	}, win)
	d.Show()
}
