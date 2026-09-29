package ui

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

type mosaicSavedLevels struct {
	Black      string `json:"black"`
	White      string `json:"white"`
	Background string `json:"background"`
	Peak       string `json:"peak"`
	ScaledPeak string `json:"scaledPeak"`
	Mode       string `json:"mode"`
	MTFMidtone string `json:"mtfMidtone,omitempty"`
}

func (ws *mosaicWorkspace) applyLevelsToPreview() {
	black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
	if ws.activePicker != nil && ws.starModeRefResult != nil {
		img := buildMosaicPreviewImageWithLevels(ws.starModeRefResult, black, white, bg, peak, scaledPeak, ws.stretchMode, ws.mtfMidtone)
		ws.activePicker.SetImage(img)
	} else if ws.state.result != nil {
		img := buildMosaicPreviewImageWithLevels(ws.state.result, black, white, bg, peak, scaledPeak, ws.stretchMode, ws.mtfMidtone)
		ws.preview.Image = img
		ws.preview.Refresh()
	}
}

func autoLevelsForPixels(pixels []float32) models.LoadedImage {
	img := &models.LoadedImage{
		HDU: fitsio.HDU{Data: fitsio.ImageData{Pixels: pixels}},
	}
	processing.AutoScaleLikeFitsLiberator(img)
	return *img
}

func (ws *mosaicWorkspace) autoLevels(pixels []float32) {
	img := autoLevelsForPixels(pixels)
	ws.blackEntry.SetValue(img.Black)
	ws.whiteEntry.SetValue(img.White)
	ws.bgEntry.SetValue(img.Background)
	ws.peakEntry.SetValue(img.Peak)
	ws.scaledPeakEntry.SetValue(img.ScaledPeak)
	ws.levelsSet = true
}

func (ws *mosaicWorkspace) currentPreviewResult() *mosaic.Result {
	if ws.activePicker != nil && ws.starModeRefResult != nil {
		return ws.starModeRefResult
	}
	if ws.state.result != nil {
		return ws.state.result
	}
	return ws.starModeRefResult
}

func (ws *mosaicWorkspace) loadedImageForMosaicResult(result *mosaic.Result) *models.LoadedImage {
	if result == nil {
		return nil
	}
	black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
	return &models.LoadedImage{
		HDU: fitsio.HDU{
			Data: fitsio.ImageData{
				Pixels: result.Pixels,
				Width:  result.Width,
				Height: result.Height,
			},
		},
		Mode:       ws.stretchMode,
		Black:      black,
		White:      white,
		Background: bg,
		Peak:       peak,
		ScaledPeak: scaledPeak,
		MTFMidtone: ws.mtfMidtoneEntry.Value(),
	}
}

func (ws *mosaicWorkspace) applyImageLevelsToControls(img *models.LoadedImage) {
	ws.blackEntry.SetValue(img.Black)
	ws.whiteEntry.SetValue(img.White)
	ws.bgEntry.SetValue(img.Background)
	ws.peakEntry.SetValue(img.Peak)
	ws.scaledPeakEntry.SetValue(img.ScaledPeak)
	ws.mtfMidtoneEntry.SetValue(img.MTFMidtone)
	ws.mtfMidtone = img.MTFMidtone
	ws.levelsSet = true
}

func (ws *mosaicWorkspace) autoMTFLevels(result *mosaic.Result) {
	img := ws.loadedImageForMosaicResult(result)
	if img == nil {
		return
	}
	processing.AutoMTFMidtone(img)
	ws.applyImageLevelsToControls(img)
	ws.modeSelect.SetSelected("MTF")
	ws.applyLevelsToPreview()
}

func (ws *mosaicWorkspace) magicLevels(result *mosaic.Result, preset processing.MagicPreset) {
	img := ws.loadedImageForMosaicResult(result)
	if img == nil {
		return
	}
	res := processing.ApplyMagicLevels(img, preset)
	ws.blackEntry.SetValue(img.Black)
	ws.whiteEntry.SetValue(img.White)
	ws.bgEntry.SetValue(img.Background)
	ws.peakEntry.SetValue(img.Peak)
	ws.levelsSet = true
	debuglog.Log(fmt.Sprintf(
		"Magic[Mosaic %s]: black=%.4g white=%.4g sky=%.4g sigma=%.4g clipLow=%.3f%% clipHigh=%.3f%% stars=%v(%.2f%%) whiteSrc=%s whiteN=%d(%.2f%%)",
		res.Preset, res.Black, res.White, res.Background, res.Sigma,
		res.ClipLowPercent, res.ClipHighPercent, res.StarsExcluded, res.StarPixelPercent,
		res.WhiteSampleSource, res.WhiteSampleCount, res.WhiteSamplePercent))
	ws.autoMTFLevels(result)
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
		MTFMidtone: fmt.Sprintf("%.4f", ws.mtfMidtone),
	})
	if err == nil {
		ws.app.Preferences().SetString(ws.prefKey(ws.activeFilter), string(data))
	}
}

func (ws *mosaicWorkspace) resetMTFMidtone() {
	ws.mtfMidtone = stretch.DefaultMTFMidtone
	if ws.mtfMidtoneEntry != nil {
		ws.mtfMidtoneEntry.SetValue(stretch.DefaultMTFMidtone)
	}
}

func (ws *mosaicWorkspace) resetPreview() {
	ws.state.result = nil
	ws.state.resultName = ""
	ws.saveBtn.Disable()
	ws.preview.Image = blankImg()
	ws.preview.Refresh()
	ws.statsLabel.SetText(mosaicEmptyStatsText())
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
	parse := func(s string) (float64, bool) {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
	}
	black, okB := parse(sl.Black)
	white, okW := parse(sl.White)
	bg, okBg := parse(sl.Background)
	peak, okP := parse(sl.Peak)
	scaled, okS := parse(sl.ScaledPeak)
	if !(okB && okW && okBg && okP && okS) {
		return false
	}
	mtf, okM := parse(sl.MTFMidtone)
	if okM && !(mtf > 0 && mtf < 1) {
		okM = false
	}
	ws.blackEntry.SetValue(black)
	ws.whiteEntry.SetValue(white)
	ws.bgEntry.SetValue(bg)
	ws.peakEntry.SetValue(peak)
	ws.scaledPeakEntry.SetValue(scaled)
	if okM {
		ws.mtfMidtone = mtf
		ws.mtfMidtoneEntry.SetValue(mtf)
	}
	if sl.Mode != "" {
		ws.modeSelect.SetSelected(sl.Mode)
	}
	ws.levelsSet = true
	return true
}
