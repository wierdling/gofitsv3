package ui

import (
	"fmt"
)

type composePicker struct {
	channel int
	target  string
}

func (ws *composeWorkspace) clearPicker() {
	ws.activePicker = composePicker{channel: -1}
	for i := 0; i < 3; i++ {
		if ws.viewports[i] == nil || ws.viewports[i].overlay == nil {
			continue
		}
		ws.viewports[i].overlay.pickerActive = false
		ws.viewports[i].overlay.Refresh()
		ws.viewports[i].SetPickerValueText("Value: --")
	}
}

func (ws *composeWorkspace) setPicker(channel int, target string) {
	if ws.activePicker.channel == channel && ws.activePicker.target == target {
		ws.clearPicker()
		return
	}
	ws.clearPicker()
	ws.activePicker = composePicker{channel: channel, target: target}
	if ws.viewports[channel] != nil && ws.viewports[channel].overlay != nil {
		ws.viewports[channel].overlay.pickerActive = true
		ws.viewports[channel].overlay.Refresh()
	}
	ws.viewports[channel].SetPickerValueText(fmt.Sprintf("Pick %s: --", target))
}

func (ws *composeWorkspace) updatePickerValue(channel int, point imagePoint) {
	// While picking a level, report the median of a small region so the
	// readout matches the value that will be committed (see onTapped) and is
	// stable against single noisy pixels. A plain hover stays a single-pixel
	// probe.
	var (
		value float64
		ok    bool
	)
	if ws.activePicker.channel == channel {
		value, ok = composeRegionMedianAt(ws.imgs[channel], point, composePickRadius)
	} else {
		value, ok = composePixelValueAt(ws.imgs[channel], point)
	}
	if !ok {
		if ws.activePicker.channel == channel {
			ws.viewports[channel].SetPickerValueText(fmt.Sprintf("Pick %s: --", ws.activePicker.target))
		} else {
			ws.viewports[channel].SetPickerValueText("Value: --")
		}
		return
	}
	if ws.activePicker.channel == channel {
		ws.viewports[channel].SetPickerValueText(fmt.Sprintf("Pick %s: %.6g", ws.activePicker.target, value))
		return
	}
	ws.viewports[channel].SetPickerValueText(fmt.Sprintf("Value: %.6g", value))
}

func (ws *composeWorkspace) updateMeasurement() {
	ws.viewports[3].setMeasurementOverlay(ws.measureStart, ws.measureEnd, false)
	switch {
	case ws.measureStart != nil && ws.measureEnd != nil:
		m := measurePoints(*ws.measureStart, *ws.measureEnd)
		ws.measureLabel.SetText(fmt.Sprintf("A(%d,%d) B(%d,%d)\ndx=%+d dy=%+d d=%.2f px", m.Start.X, m.Start.Y, m.End.X, m.End.Y, m.DX, m.DY, m.Distance))
	case ws.measureStart != nil:
		ws.measureLabel.SetText(fmt.Sprintf("Measure: A=(%d,%d) — click B", ws.measureStart.X, ws.measureStart.Y))
	default:
		ws.measureLabel.SetText("Measure: --")
	}
}
