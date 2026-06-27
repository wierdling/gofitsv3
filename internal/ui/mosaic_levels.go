package ui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

type mosaicSavedLevels struct {
	Black      string `json:"black"`
	White      string `json:"white"`
	Background string `json:"background"`
	Peak       string `json:"peak"`
	ScaledPeak string `json:"scaledPeak"`
	Mode       string `json:"mode"`
}

func (ws *mosaicWorkspace) applyLevelsToPreview() {
	black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
	if ws.activePicker != nil && ws.starModeRefResult != nil {
		img := buildMosaicPreviewImageWithLevels(ws.starModeRefResult, black, white, bg, peak, scaledPeak, ws.stretchMode)
		ws.activePicker.SetImage(img)
	} else if ws.state.result != nil {
		img := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode)
		ws.preview.Image = img
		ws.preview.Refresh()
	}
}

func (ws *mosaicWorkspace) autoLevels(pixels []float32) {
	img := &models.LoadedImage{
		HDU: fitsio.HDU{Data: fitsio.ImageData{Pixels: pixels}},
	}
	processing.AutoScaleLikeFitsLiberator(img)
	ws.blackEntry.SetValue(img.Black)
	ws.whiteEntry.SetValue(img.White)
	ws.bgEntry.SetValue(img.Background)
	ws.peakEntry.SetValue(img.Peak)
	ws.scaledPeakEntry.SetValue(img.ScaledPeak)
	ws.levelsSet = true
}

func (ws *mosaicWorkspace) saveLevelPrefs() {
	if ws.activeFilter == "" {
		return
	}
	data, err := json.Marshal(mosaicSavedLevels{
		Black:      fmt.Sprintf("%.4f", ws.blackEntry.Value()),
		White:      fmt.Sprintf("%.4f", ws.whiteEntry.Value()),
		Background: fmt.Sprintf("%.4f", ws.bgEntry.Value()),
		Peak:       fmt.Sprintf("%.4f", ws.peakEntry.Value()),
		ScaledPeak: fmt.Sprintf("%.4f", ws.scaledPeakEntry.Value()),
		Mode:       modeNameForMode(ws.stretchMode),
	})
	if err == nil {
		ws.app.Preferences().SetString(ws.prefKey(ws.activeFilter), string(data))
	}
}

func (ws *mosaicWorkspace) resetPreview() {
	ws.state.result = nil
	ws.saveBtn.Disable()
	ws.preview.Image = blankImg()
	ws.preview.Refresh()
	ws.statsLabel.SetText("Mean: -- | Std: -- | Size: --")
	ws.mosaicBins = [256]int{}
	ws.mosaicHistogram.Refresh()
}

// loadLevelPrefsAndMode loads saved level settings for filter, also updating the
// mode dropdown. Returns true if saved preferences were found and applied.
func (ws *mosaicWorkspace) loadLevelPrefsAndMode(filter string) bool {
	raw := ws.app.Preferences().String(ws.prefKey(filter))
	if raw == "" {
		return false
	}
	var sl mosaicSavedLevels
	if err := json.Unmarshal([]byte(raw), &sl); err != nil {
		return false
	}
	if v, err2 := strconv.ParseFloat(strings.TrimSpace(sl.Black), 64); err2 == nil {
		ws.blackEntry.SetValue(v)
	}
	if v, err2 := strconv.ParseFloat(strings.TrimSpace(sl.White), 64); err2 == nil {
		ws.whiteEntry.SetValue(v)
	}
	if v, err2 := strconv.ParseFloat(strings.TrimSpace(sl.Background), 64); err2 == nil {
		ws.bgEntry.SetValue(v)
	}
	if v, err2 := strconv.ParseFloat(strings.TrimSpace(sl.Peak), 64); err2 == nil {
		ws.peakEntry.SetValue(v)
	}
	if v, err2 := strconv.ParseFloat(strings.TrimSpace(sl.ScaledPeak), 64); err2 == nil {
		ws.scaledPeakEntry.SetValue(v)
	}
	if sl.Mode != "" {
		ws.modeSelect.SetSelected(sl.Mode)
	}
	ws.levelsSet = true
	return true
}
