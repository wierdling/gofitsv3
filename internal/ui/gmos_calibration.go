package ui

import (
	"context"
	"fmt"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/mosaic"
)

func (ws *mosaicWorkspace) configureGMOSCalibration() {
	if ws.queueRunning {
		return
	}
	ws.inputMu.RLock()
	inputs := append([]mosaic.Input(nil), ws.state.inputs...)
	ws.inputMu.RUnlock()
	if !gmosCalibrationEligible(inputs, nil, ws.queueRunning) {
		dialog.ShowInformation("GMOS Calibration", "Load GMOS science images first.", ws.win)
		return
	}
	ws.inputMu.RLock()
	oldInputs := append([]mosaic.Input(nil), ws.state.inputs...)
	var oldReference *mosaic.Input
	if ws.state.referenceInput != nil {
		ref := *ws.state.referenceInput
		oldReference = &ref
	}
	ws.inputMu.RUnlock()
	in := oldInputs[0]
	rawSelected, rawErr := rawGMOSInput(in)
	if rawErr != nil {
		dialog.ShowInformation("Gemini GMOS Calibration", rawErr.Error(), ws.win)
		return
	}
	if !mosaic.IsGeminiHeader(rawSelected.PrimaryHeader) {
		dialog.ShowInformation("GMOS Calibration", "The active inputs are not GMOS data.", ws.win)
		return
	}
	if err := validateGMOSWorkspaceInputs(oldInputs, rawSelected); err != nil {
		lbl := widget.NewLabel(err.Error())
		lbl.Wrapping = fyne.TextWrapWord
		content := container.NewVScroll(lbl)
		content.SetMinSize(fyne.NewSize(600, 200))
		d := dialog.NewCustom("Gemini GMOS Calibration", "OK", content, ws.win)
		d.Show()
		return
	}
	ctx, generation, started := ws.beginGMOSCalibration()
	if !started {
		return
	}
	pt := newProgressTrackerWithContext("Gemini GMOS Calibration", "Discovering calibration frames...", ws.win, ctx, ws.cancelGMOSCalibration)
	go func() {
		m, err := mosaic.DiscoverGMOSCalibration(filepath.Dir(rawSelected.Path))
		if err == nil && ctx.Err() == nil {
			science := mosaic.GMOSCalibrationFrame{Path: rawSelected.Path, Kind: mosaic.GMOSScience, Filter: mosaic.FilterStringForInput(rawSelected), Date: rawSelected.DateObs, Key: mosaic.GMOSCompatibilityKeyForPath(rawSelected.Path)}
			sel := mosaic.SelectGMOSCalibrations(science, m)
			if validateErr := mosaic.ValidateGMOSSelection(sel); validateErr != nil {
				err = fmt.Errorf("%s: %w", sel.Warning, validateErr)
			} else {
				pt.progress("Applying calibration and recombining exposures", 0, len(gmosSourcePaths(oldInputs)))
				var inputs []mosaic.Input
				var statuses []mosaic.InputStatus
				inputs, statuses, err = reloadGMOSInputs(ctx, oldInputs, sel, func(done, total int) {
					pt.progress("Applying calibration and recombining exposures", done, total)
				})
				if err == nil {
					fyne.Do(func() {
						if !ws.finishGMOSCalibration(generation) {
							pt.hide()
							return
						}
						ws.inputMu.Lock()
						if !sameStringSlice(gmosSourcePaths(oldInputs), gmosSourcePaths(ws.state.inputs)) || gmosReferenceIdentity(ws.state.referenceInput) != gmosReferenceIdentity(oldReference) {
							ws.inputMu.Unlock()
							pt.hide()
							return
						}
						ws.state.inputs, ws.state.statuses = inputs, statuses
						// The reference is a geometry-only baseline. GMOS calibration
						// applies only to science inputs and preserves this value exactly.
						ws.state.referenceInput = oldReference
						ws.state.gmosCalibration = &sel
						ws.state.result = nil
						ws.inputMu.Unlock()
						pt.hide()
						ws.resetPreview()
						ws.rebuildOffsetControls()
						ws.updateStatus()
						ws.updateActionButtons()
					})
					return
				}
			}
		}
		if ctx.Err() != nil {
			err = mosaic.ErrCancelled
		}
		fyne.Do(func() {
			ws.finishGMOSCalibration(generation)
			pt.hide()
			if err == mosaic.ErrCancelled {
				dialog.ShowInformation("Gemini GMOS Calibration", "Calibration was cancelled; the current workspace was preserved.", ws.win)
			} else if err != nil {
				dialog.ShowError(err, ws.win)
			}
		})
	}()
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func gmosReferencePath(input *mosaic.Input) string {
	if input == nil {
		return ""
	}
	if input.SourcePath != "" {
		return input.SourcePath
	}
	return input.Path
}

// gmosReferenceIdentity identifies the exact reference chip selected by the
// user, including its source path and SCI extension. A same-path replacement
// must not be allowed to accept a stale calibration result.
func gmosReferenceIdentity(input *mosaic.Input) string {
	if input == nil {
		return ""
	}
	return artifactMaskTargetKey(*input)
}

func validateGMOSWorkspaceInputs(inputs []mosaic.Input, selected mosaic.Input) error {
	selectedRaw, err := rawGMOSInput(selected)
	if err != nil {
		return err
	}
	wantFilter := mosaic.FilterStringForInput(selectedRaw)
	wantKey := mosaic.GMOSCompatibilityKeyForPath(gmosPath(selectedRaw))
	if wantKey == "" {
		return fmt.Errorf("the selected GMOS frame is not a raw 3-chip uncalibrated image (it may have been previously combined or processed)")
	}
	for _, input := range inputs {
		raw, rawErr := rawGMOSInput(input)
		if rawErr != nil {
			return rawErr
		}
		if !mosaic.IsGeminiHeader(raw.PrimaryHeader) {
			return fmt.Errorf("GMOS calibration requires a workspace containing only GMOS inputs")
		}
		if got := mosaic.FilterStringForInput(raw); got != wantFilter {
			return fmt.Errorf("GMOS calibration cannot apply to mixed filters (%s and %s)", wantFilter, got)
		}
		gotKey := mosaic.GMOSCompatibilityKeyForPath(gmosPath(raw))
		if gotKey == "" {
			return fmt.Errorf("workspace contains a GMOS frame that is not a raw 3-chip image:\n%s\n\nEnsure all files in the workspace are raw, uncombined FITS files from the telescope.", filepath.Base(gmosPath(raw)))
		}
		if gotKey != wantKey {
			return fmt.Errorf("GMOS calibration cannot apply to mixed detector/readout configurations:\nwant: %s\ngot:  %s", wantKey, gotKey)
		}
	}
	return nil
}

// gmosCalibrationEligible reports whether the loaded science set can expose
// GMOS calibration. The reference baseline is accepted only to make the
// geometry-only boundary explicit; it intentionally does not affect the
// result and is never calibrated here.
func gmosCalibrationEligible(inputs []mosaic.Input, _ *mosaic.Input, queueRunning bool) bool {
	if queueRunning || len(inputs) == 0 {
		return false
	}
	for _, input := range inputs {
		if !mosaic.IsGeminiHeader(input.PrimaryHeader) {
			return false
		}
	}
	return true
}

func rawGMOSInput(input mosaic.Input) (mosaic.Input, error) {
	path := input.SourcePath
	if path == "" {
		return input, nil
	}
	meta, err := mosaic.LoadInputsMetadataFromPath(path)
	if err != nil {
		return mosaic.Input{}, fmt.Errorf("load raw GMOS metadata %s: %w", filepath.Base(path), err)
	}
	if len(meta) == 0 {
		return mosaic.Input{}, fmt.Errorf("raw GMOS source %s contains no image chips", filepath.Base(path))
	}
	return meta[0], nil
}

func gmosPath(input mosaic.Input) string {
	if input.SourcePath != "" {
		return input.SourcePath
	}
	return input.Path
}

func preserveGMOSInputState(dst *mosaic.Input, src mosaic.Input) {
	dst.OffsetX, dst.OffsetY = src.OffsetX, src.OffsetY
	dst.ManualTransform, dst.HasManualTransform = src.ManualTransform, src.HasManualTransform
	dst.OffsetLocked, dst.Excluded = src.OffsetLocked, src.Excluded
	dst.NormalizeExposure, dst.ExposureScale = src.NormalizeExposure, src.ExposureScale
}

func gmosSourcePaths(inputs []mosaic.Input) []string {
	seen := make(map[string]bool)
	var paths []string
	for _, in := range inputs {
		path := in.SourcePath
		if path == "" {
			path = in.Path
		}
		if path != "" && !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

func reloadGMOSInputs(ctx context.Context, old []mosaic.Input, selection mosaic.GMOSCalibrationSelection, progress func(int, int)) ([]mosaic.Input, []mosaic.InputStatus, error) {
	if err := mosaic.ValidateGMOSSelection(selection); err != nil {
		return nil, nil, err
	}
	paths := gmosSourcePaths(old)
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no GMOS source exposures are loaded")
	}
	var inputs []mosaic.Input
	var statuses []mosaic.InputStatus
	for i, path := range paths {
		if ctx != nil && ctx.Err() != nil {
			return nil, nil, mosaic.ErrCancelled
		}
		working, _, err := mosaic.EnsureCombinedExposure(path, mosaic.CombineOptions{Ctx: ctx, GMOSCalibration: &selection})
		if err != nil {
			return nil, nil, err
		}
		loaded, err := mosaic.LoadInputsMetadataFromPath(working)
		if err != nil {
			return nil, nil, err
		}
		for j := range loaded {
			loaded[j].SourcePath = path
			for _, previous := range old {
				previousPath := previous.SourcePath
				if previousPath == "" {
					previousPath = previous.Path
				}
				if previousPath == path {
					preserveGMOSInputState(&loaded[j], previous)
					break
				}
			}
			inputs = append(inputs, loaded[j])
			statuses = append(statuses, mosaic.InputStatus{Path: mosaic.InputKey(loaded[j]), Included: true, Status: fmt.Sprintf("combined and calibrated (%d chips)", len(loaded))})
		}
		if progress != nil {
			progress(i+1, len(paths))
		}
	}
	return inputs, statuses, nil
}
