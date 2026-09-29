package ui

import (
	"fmt"
	"image"
	"math"

	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/utils"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

type rgbLevelsWindow struct {
	win        fyne.Window
	levels     *models.RgbLevels
	bins       [3][256]int
	hists      [3]*canvas.Raster
	minEntries [3]*widget.Entry
	maxEntries [3]*widget.Entry
}

func newRGBLevelsWindow(app fyne.App, levels *models.RgbLevels, onApply func()) *rgbLevelsWindow {
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

// Updated to ingest [3]histogram.Stats instead of the raw 256-int arrays
func (w *rgbLevelsWindow) setHistogram(stats [3]histogram.Stats) {
	// Extract the histogram arrays from the new stats structs
	for i := 0; i < 3; i++ {
		w.bins[i] = stats[i].Hist
	}

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
		if !isFiniteLevel(minVal) {
			minVal = 0
		}
		if !isFiniteLevel(maxVal) {
			maxVal = 255
		}
		if v, err := utils.ParseFloat(w.minEntries[i].Text); err == nil && isFiniteLevel(v) {
			minVal = utils.ClampLevel(v)
		}
		if v, err := utils.ParseFloat(w.maxEntries[i].Text); err == nil && isFiniteLevel(v) {
			maxVal = utils.ClampLevel(v)
		}
		if maxVal <= minVal {
			if minVal >= 255 {
				minVal, maxVal = 254, 255
			} else {
				maxVal = minVal + 1
				if maxVal > 255 {
					minVal, maxVal = 254, 255
				}
			}
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

func isFiniteLevel(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
