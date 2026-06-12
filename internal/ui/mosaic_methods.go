package ui

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

// freeInputPixels releases the full-resolution SCI and ERR pixel arrays held by
// every loaded input (and the reference baseline), keeping only lightweight
// metadata (header, dimensions, transforms). This is called after a drizzle
// build so the workspace does not pin one array per input in memory; the arrays
// are reloaded on demand via ensureInputPixelsLoaded when alignment or the star
// picker needs them.
func (ws *mosaicWorkspace) freeInputPixels() {
	for i := range ws.state.inputs {
		ws.state.inputs[i].HDU.Data.Pixels = nil
		ws.state.inputs[i].ERRPixels = nil
	}
	if ws.state.referenceInput != nil {
		ws.state.referenceInput.HDU.Data.Pixels = nil
		ws.state.referenceInput.ERRPixels = nil
	}
}

// ensureInputPixelsLoaded reloads pixel data for any input whose arrays were
// previously freed (e.g. after a drizzle build). Files are read once per path
// even when they contribute multiple SCI chips. Offsets, transforms, and
// exclusion flags on the in-memory inputs are preserved; only the pixel arrays
// are restored.
func (ws *mosaicWorkspace) ensureInputPixelsLoaded() error {
	cache := map[string][]mosaic.Input{}
	load := func(path string) ([]mosaic.Input, error) {
		if v, ok := cache[path]; ok {
			return v, nil
		}
		v, err := mosaic.LoadInputsFromPath(path)
		if err != nil {
			return nil, err
		}
		cache[path] = v
		return v, nil
	}
	restore := func(dst *mosaic.Input) error {
		if dst.HDU.Data.Pixels != nil {
			return nil
		}
		loaded, err := load(dst.Path)
		if err != nil {
			return fmt.Errorf("reload %s: %w", dst.Path, err)
		}
		for i := range loaded {
			if loaded[i].SCIExt == dst.SCIExt {
				dst.HDU.Data.Pixels = loaded[i].HDU.Data.Pixels
				dst.ERRPixels = loaded[i].ERRPixels
				return nil
			}
		}
		if len(loaded) == 1 {
			dst.HDU.Data.Pixels = loaded[0].HDU.Data.Pixels
			dst.ERRPixels = loaded[0].ERRPixels
			return nil
		}
		return fmt.Errorf("reload %s: no SCI ext %d", dst.Path, dst.SCIExt)
	}
	for i := range ws.state.inputs {
		if err := restore(&ws.state.inputs[i]); err != nil {
			return err
		}
	}
	if ws.state.referenceInput != nil {
		if err := restore(ws.state.referenceInput); err != nil {
			return err
		}
	}
	return nil
}

// inputsWithRef returns ws.state.inputs prepended with the reference baseline
// (if set). The reference is marked ReferenceOnly so Build() uses it only for
// WCS anchoring and excludes its pixels from the output.
func (ws *mosaicWorkspace) inputsWithRef() []mosaic.Input {
	var active []mosaic.Input
	for _, inp := range ws.state.inputs {
		if !inp.Excluded {
			active = append(active, inp)
		}
	}
	if ws.state.referenceInput == nil {
		return active
	}
	ref := *ws.state.referenceInput
	ref.ReferenceOnly = true
	return append([]mosaic.Input{ref}, active...)
}

func (ws *mosaicWorkspace) parseLevelEntries() (black, white, bg, peak, scaledPeak float64) {
	black = ws.blackEntry.Value()
	white = ws.whiteEntry.Value()
	bg = ws.bgEntry.Value()
	peak = ws.peakEntry.Value()
	scaledPeak = ws.scaledPeakEntry.Value()
	if peak <= 0 {
		peak = 1000
	}
	if scaledPeak <= 0 {
		scaledPeak = 1000
	}
	return
}

func (ws *mosaicWorkspace) prefKey(filter string) string { return "mosaicLevels_" + filter }

func (ws *mosaicWorkspace) syncStatusOffsets() {
	for i := range ws.state.statuses {
		if i >= len(ws.state.inputs) {
			break
		}
		ws.state.statuses[i].OffsetX = ws.state.inputs[i].OffsetX
		ws.state.statuses[i].OffsetY = ws.state.inputs[i].OffsetY
		ws.state.statuses[i].HasAffine = ws.state.inputs[i].HasManualTransform
		if ws.state.inputs[i].HasManualTransform {
			t := ws.state.inputs[i].ManualTransform
			ws.state.statuses[i].AffineRotDeg = math.Atan2(t.D, t.A) * 180 / math.Pi
		} else {
			ws.state.statuses[i].AffineRotDeg = 0
		}
	}
}

func (ws *mosaicWorkspace) currentFilterAndDir() (string, string, bool) {
	if len(ws.state.inputs) == 0 {
		return "", "", false
	}
	filter := mosaic.FilterNameForInput(ws.state.inputs[0])
	dir := filepath.Dir(ws.state.inputs[0].Path)
	for _, input := range ws.state.inputs[1:] {
		if mosaic.FilterNameForInput(input) != filter || filepath.Dir(input.Path) != dir {
			return "", "", false
		}
	}
	return filter, dir, true
}

func (ws *mosaicWorkspace) updateOffsetButtons() {
	if _, _, ok := ws.currentFilterAndDir(); ok {
		ws.saveOffsetsBtn.Enable()
		ws.loadOffsetsBtn.Enable()
	} else {
		ws.saveOffsetsBtn.Disable()
		ws.loadOffsetsBtn.Disable()
	}
}

func (ws *mosaicWorkspace) updateStatus() {
	ws.syncStatusOffsets()
	ws.statusLabel.SetText(strings.Join(mosaic.FormatStatusLines(ws.state.statuses), "\n"))
	ws.updateOffsetButtons()
}

func (ws *mosaicWorkspace) applyAutoLoadedOffsets(inputs []mosaic.Input) []string {
	_, messages := mosaic.AutoLoadOffsets(inputs)
	return messages
}

// pointInConvexQuad tests whether (px, py) lies inside the convex quad defined
// by corners in TL(0), TR(1), BL(2), BR(3) order.
func (ws *mosaicWorkspace) pointInConvexQuad(px, py float64, corners [4][2]float64) bool {
	// Reorder to a consistent winding: TL, TR, BR, BL.
	pts := [4][2]float64{corners[0], corners[1], corners[3], corners[2]}
	var sign float64
	for i := 0; i < 4; i++ {
		a := pts[i]
		b := pts[(i+1)%4]
		cross := (b[0]-a[0])*(py-a[1]) - (b[1]-a[1])*(px-a[0])
		if i == 0 {
			if cross >= 0 {
				sign = 1
			} else {
				sign = -1
			}
		} else if cross*sign < 0 {
			return false
		}
	}
	return true
}

// pickerToRefPixels converts stars from the picker's image-pixel space to
// canonical raw-reference-pixel space, undoing the displayed drizzle output's
// scale and origin so saved star files are stable across re-drizzles.
func (ws *mosaicWorkspace) pickerToRefPixels(stars []processing.Star) []processing.Star {
	r := ws.starModeRefResult
	if r == nil || r.Scale <= 0 {
		return stars
	}
	out := make([]processing.Star, len(stars))
	for i, s := range stars {
		out[i] = processing.Star{X: s.X/r.Scale + r.OriginX, Y: s.Y/r.Scale + r.OriginY}
	}
	return out
}

// refPixelsToPicker is the inverse of pickerToRefPixels.
func (ws *mosaicWorkspace) refPixelsToPicker(stars []processing.Star) []processing.Star {
	r := ws.starModeRefResult
	if r == nil || r.Scale <= 0 {
		return stars
	}
	out := make([]processing.Star, len(stars))
	for i, s := range stars {
		out[i] = processing.Star{X: (s.X - r.OriginX) * r.Scale, Y: (s.Y - r.OriginY) * r.Scale}
	}
	return out
}

// pathsContainingPoint returns the set of unique input paths whose footprint
// contains the given result-image pixel coordinate.
func (ws *mosaicWorkspace) pathsContainingPoint(px, py float64) map[string]bool {
	result := make(map[string]bool)
	if ws.state.result == nil {
		return result
	}
	for i, fp := range ws.state.result.InputFootprints {
		if i >= len(ws.state.result.InputFootprintPaths) {
			break
		}
		if ws.pointInConvexQuad(px, py, fp) {
			result[ws.state.result.InputFootprintPaths[i]] = true
		}
	}
	return result
}

// fitZoom returns the zoom level that fits the image in the preview scroll.
func (ws *mosaicWorkspace) fitZoom() float64 {
	var imgW, imgH int
	if ws.state.result != nil {
		imgW, imgH = ws.state.result.Width, ws.state.result.Height
	} else {
		imgW, imgH = 600, 500
	}
	sz := ws.previewScroll.Size()
	if sz.Width <= 1 || sz.Height <= 1 {
		sz = fyne.NewSize(600, 500)
	}
	z := math.Min(float64(sz.Width)/float64(imgW), float64(sz.Height)/float64(imgH))
	return math.Max(z, 1.0/16)
}

func (ws *mosaicWorkspace) openDrizzleSettings() {
	showDrizzleSettingsDialog(ws.win, ws.state.drizzleSettings, func(s models.DrizzleSettings) {
		ws.state.drizzleSettings = s
		ws.state.drizzleSettingsSet = true
	})
}

func (ws *mosaicWorkspace) openAlignmentSettings() {
	showAlignmentSettingsDialog(ws.win, ws.state.alignmentSettings, func(s models.AlignmentSettings) {
		ws.state.alignmentSettings = s
		ws.state.alignmentSettingsSet = true
	})
}

func (ws *mosaicWorkspace) openSkysubSettings() {
	showSkysubSettingsDialog(ws.win, ws.state.skysubSettings, func(s models.SkysubSettings) {
		ws.state.skysubSettings = s
		ws.state.skysubSettingsSet = true
	})
}
