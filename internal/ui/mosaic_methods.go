package ui

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
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
		ws.state.inputs[i].WeightPixels = nil
	}
	if ws.state.referenceInput != nil {
		ws.state.referenceInput.HDU.Data.Pixels = nil
		ws.state.referenceInput.ERRPixels = nil
		ws.state.referenceInput.WeightPixels = nil
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
		return assignPixelsFromLoaded(dst, loaded)
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

// ensureInputPixelsLoadedFor restores pixel arrays for a standalone project
// load. It intentionally does not touch workspace state.
func ensureInputPixelsLoadedFor(inputs []mosaic.Input, reference *mosaic.Input) error {
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
		if dst == nil || dst.HDU.Data.Pixels != nil {
			return nil
		}
		loaded, err := load(dst.Path)
		if err != nil {
			return fmt.Errorf("reload %s: %w", dst.Path, err)
		}
		return assignPixelsFromLoaded(dst, loaded)
	}
	for i := range inputs {
		if err := restore(&inputs[i]); err != nil {
			return err
		}
	}
	return restore(reference)
}

func freeInputPixelsFor(inputs []mosaic.Input, reference *mosaic.Input) {
	for i := range inputs {
		inputs[i].HDU.Data.Pixels = nil
		inputs[i].ERRPixels = nil
		inputs[i].WeightPixels = nil
	}
	if reference != nil {
		reference.HDU.Data.Pixels = nil
		reference.ERRPixels = nil
		reference.WeightPixels = nil
	}
}

// ensureInputPixelsLoadedAt reloads pixel data for a single input by index when
// its arrays were previously freed or never loaded (e.g. after a metadata-only
// load or a drizzle build). Use this when only one frame is needed — such as the
// star-picker reference preview — so the whole dataset is not pulled into memory.
func (ws *mosaicWorkspace) ensureInputPixelsLoadedAt(idx int) error {
	if idx < 0 || idx >= len(ws.state.inputs) {
		return fmt.Errorf("input index %d out of range (%d inputs)", idx, len(ws.state.inputs))
	}
	dst := &ws.state.inputs[idx]
	if dst.HDU.Data.Pixels != nil {
		return nil
	}
	loaded, err := mosaic.LoadInputsFromPath(dst.Path)
	if err != nil {
		return fmt.Errorf("reload %s: %w", dst.Path, err)
	}
	return assignPixelsFromLoaded(dst, loaded)
}

// assignPixelsFromLoaded copies the SCI/ERR pixel arrays for dst out of a freshly
// loaded set, matching on SCI extension (falling back to the sole input when the
// file has only one).
func assignPixelsFromLoaded(dst *mosaic.Input, loaded []mosaic.Input) error {
	for i := range loaded {
		if loaded[i].SCIExt == dst.SCIExt {
			dst.HDU.Data.Pixels = loaded[i].HDU.Data.Pixels
			dst.ERRPixels = loaded[i].ERRPixels
			dst.WeightPixels = loaded[i].WeightPixels
			return nil
		}
	}
	if len(loaded) == 1 {
		dst.HDU.Data.Pixels = loaded[0].HDU.Data.Pixels
		dst.ERRPixels = loaded[0].ERRPixels
		dst.WeightPixels = loaded[0].WeightPixels
		return nil
	}
	return fmt.Errorf("reload %s: no SCI ext %d", dst.Path, dst.SCIExt)
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
	dir := inputSourceDir(ws.state.inputs[0])
	for _, input := range ws.state.inputs[1:] {
		if mosaic.FilterNameForInput(input) != filter || inputSourceDir(input) != dir {
			return "", "", false
		}
	}
	return filter, dir, true
}

// inputSourceDir returns the directory of an input's original source file. For
// combined inputs the Path points at the working/ copy, so the source directory
// (where offset files and sibling exposures live) comes from SourcePath.
func inputSourceDir(input mosaic.Input) string {
	if input.SourcePath != "" {
		return filepath.Dir(input.SourcePath)
	}
	return filepath.Dir(input.Path)
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

func (ws *mosaicWorkspace) updateActionButtons() {
	hasInputs := len(ws.state.inputs) > 0
	hasRef := ws.state.referenceInput != nil

	if ws.batchBtn != nil {
		if ws.queueRunning {
			ws.batchBtn.Disable()
		} else {
			ws.batchBtn.Enable()
		}
		if hasInputs {
			ws.batchBtn.Importance = widget.MediumImportance
		} else {
			ws.batchBtn.Importance = widget.HighImportance
		}
		ws.batchBtn.Refresh()
	}
	if ws.directoryBtn != nil {
		if ws.queueRunning {
			ws.directoryBtn.Disable()
		} else {
			ws.directoryBtn.Enable()
		}
		ws.directoryBtn.Refresh()
	}
	if ws.buildBtn != nil {
		if ws.queueRunning {
			ws.buildBtn.Disable()
		} else {
			ws.buildBtn.Enable()
		}
		if hasInputs {
			ws.buildBtn.Importance = widget.HighImportance
		} else {
			ws.buildBtn.Importance = widget.MediumImportance
		}
		ws.buildBtn.Refresh()
	}
	if ws.clearBtn != nil {
		if ws.queueRunning {
			ws.clearBtn.Disable()
		} else if hasInputs {
			ws.clearBtn.Enable()
		} else {
			ws.clearBtn.Disable()
		}
		ws.clearBtn.Refresh()
	}
	if ws.setRefBtn != nil {
		if ws.queueRunning {
			ws.setRefBtn.Disable()
		} else {
			ws.setRefBtn.Enable()
		}
		if hasRef {
			ws.setRefBtn.Importance = widget.MediumImportance
		} else {
			ws.setRefBtn.Importance = widget.HighImportance
		}
		ws.setRefBtn.Refresh()
	}
	if ws.clearRefBtn != nil {
		if ws.queueRunning {
			ws.clearRefBtn.Disable()
		} else if hasRef {
			ws.clearRefBtn.Enable()
		} else {
			ws.clearRefBtn.Disable()
		}
		ws.clearRefBtn.Refresh()
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
	showDrizzleSettingsDialog(ws.win, ws.state.drizzleSettings, ws.state.inputs, func(s models.DrizzleSettings) {
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

func (ws *mosaicWorkspace) openExposureReview() {
	showExposureReviewDialog(ws.win, ws.state.inputs, ws.state.exposureNormMode, func(mode mosaic.NormalizationMode) {
		ws.state.exposureNormMode = mode
		ws.updateStatus()
	})
}
