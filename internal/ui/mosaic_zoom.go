package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
)

const (
	minMosaicZoom = 1.0 / 16
	maxMosaicZoom = 16
)

func clampMosaicZoom(z float64) float64 {
	if math.IsNaN(z) || math.IsInf(z, 0) {
		return 1
	}
	return math.Max(math.Min(z, maxMosaicZoom), minMosaicZoom)
}

// setZoomSelectLabel updates the dropdown to reflect the current zoom without
// triggering OnChanged.
func (ws *mosaicWorkspace) setZoomSelectLabel(option string) {
	if ws.zoomSelectSyncing {
		return
	}
	ws.zoomSelectSyncing = true
	defer func() { ws.zoomSelectSyncing = false }()
	// If it's not a preset, manage a single custom slot at the end.
	isPreset := false
	for _, p := range ws.zoomPresets {
		if p == option {
			isPreset = true
			break
		}
	}
	if !isPreset {
		opts := ws.zoomSelect.Options
		if ws.zoomCustomOption != "" {
			filtered := opts[:0]
			for _, o := range opts {
				if o != ws.zoomCustomOption {
					filtered = append(filtered, o)
				}
			}
			opts = filtered
		}
		ws.zoomCustomOption = option
		opts = append(opts, option)
		ws.zoomSelect.Options = opts
	}
	ws.zoomSelect.SetSelected(option)
}

func (ws *mosaicWorkspace) updateZoom() {
	if ws.zoomFitMode {
		ws.zoomLevel = ws.fitZoom()
	}
	ws.zoomLevel = clampMosaicZoom(ws.zoomLevel)
	if ws.activeMeasure != nil {
		ws.activeMeasure.SetZoom(ws.zoomLevel)
		ws.pickerScroll.Refresh()
	} else if ws.activePicker != nil {
		ws.activePicker.SetZoom(ws.zoomLevel)
		ws.pickerScroll.Refresh()
	} else {
		var w, h float32
		if ws.state.result != nil {
			w = float32(float64(ws.state.result.Width) * ws.zoomLevel)
			h = float32(float64(ws.state.result.Height) * ws.zoomLevel)
		} else {
			w = float32(600 * ws.zoomLevel)
			h = float32(500 * ws.zoomLevel)
		}
		ws.preview.SetMinSize(fyne.NewSize(w, h))
		ws.preview.Refresh()
		ws.previewScroll.Refresh()
	}
	if !ws.zoomFitMode {
		pct := math.Round(ws.zoomLevel*100*10) / 10
		var label string
		if pct == math.Trunc(pct) {
			label = fmt.Sprintf("%d%%", int(pct))
		} else {
			label = fmt.Sprintf("%.1f%%", pct)
		}
		ws.setZoomSelectLabel(label)
	}
}

func (ws *mosaicWorkspace) applyCustomZoom() {
	s := strings.TrimSuffix(strings.TrimSpace(ws.zoomCustomEntry.Text), "%")
	pct, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(pct) || math.IsInf(pct, 0) || pct <= 0 {
		ws.zoomCustomEntry.SetText("")
		return
	}
	ws.zoomFitMode = false
	ws.zoomLevel = clampMosaicZoom(pct / 100.0)
	ws.zoomCustomEntry.SetText("")
	ws.updateZoom()
}
