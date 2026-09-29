package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"github.com/wierdling/gofiledialog"

	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

// applyInputState copies the saved offsets, manual transform, and lock flag from
// a project entry onto a freshly loaded input.
func applyInputState(inp *mosaic.Input, mis models.MosaicInputState) {
	inp.OffsetX = mis.OffsetX
	inp.OffsetY = mis.OffsetY
	inp.HasManualTransform = mis.HasTransform
	inp.OffsetLocked = mis.Locked
	inp.Excluded = mis.Excluded
	inp.NormalizeExposure = mis.NormalizeExposure
	inp.ExposureScale = mis.ExposureScale
	if mis.HasTransform {
		inp.ManualTransform = processing.AffineTransform{
			A: mis.TransformA, B: mis.TransformB, C: mis.TransformC,
			D: mis.TransformD, E: mis.TransformE, F: mis.TransformF,
		}
	}
}

// lowestSCIExtState returns the group entry with the smallest SCIExt. For a
// legacy multi-chip file this is the sci,1 entry, whose alignment offsets are
// valid for the whole rigid exposure (offsets and affines apply in reference-
// pixel space after WCS mapping, so they carry over to the combined image).
func lowestSCIExtState(group []models.MosaicInputState) models.MosaicInputState {
	best := group[0]
	for _, mis := range group[1:] {
		if mis.SCIExt < best.SCIExt {
			best = mis
		}
	}
	return best
}

// stateForSCIExt returns the group entry matching sciExt, or fallback when none
// matches (used to reattach offsets in the per-chip combine fallback).
func stateForSCIExt(group []models.MosaicInputState, sciExt int, fallback models.MosaicInputState) models.MosaicInputState {
	for _, mis := range group {
		if mis.SCIExt == sciExt {
			return mis
		}
	}
	return fallback
}

func (ws *mosaicWorkspace) saveMosaicProject() {
	if ws.queueRunning {
		return
	}
	proj := models.MosaicProject{
		DrizzleSettings:        ws.state.drizzleSettings,
		DrizzleSettingsSet:     ws.state.drizzleSettingsSet,
		AlignmentSettings:      ws.state.alignmentSettings,
		AlignmentSettingsSet:   ws.state.alignmentSettingsSet,
		SkysubSettings:         ws.state.skysubSettings,
		SkysubSettingsSet:      ws.state.skysubSettingsSet,
		ActiveFilter:           ws.activeFilter,
		ArtifactMasks:          ws.state.artifactMasks,
		ExposureNormMode:       int(ws.state.exposureNormMode),
		GMOSCalibrationEnabled: ws.state.gmosCalibration != nil,
	}
	if ws.state.gmosCalibration != nil {
		s := ws.state.gmosCalibration
		proj.GMOSCalibrationFingerprint = mosaic.GMOSCalibrationFingerprint(*s)
		for _, f := range s.BiasFrames {
			proj.GMOSBiasPaths = append(proj.GMOSBiasPaths, f.Path)
		}
		for _, f := range s.FlatFrames {
			proj.GMOSFlatPaths = append(proj.GMOSFlatPaths, f.Path)
		}
		for _, f := range s.BPMFrames {
			proj.GMOSBPMPaths = append(proj.GMOSBPMPaths, f.Path)
		}
	}
	for _, inp := range ws.state.inputs {
		mis := models.MosaicInputState{
			Path:              inp.Path,
			SourcePath:        inp.SourcePath,
			SCIExt:            inp.SCIExt,
			OffsetX:           inp.OffsetX,
			OffsetY:           inp.OffsetY,
			HasTransform:      inp.HasManualTransform,
			Locked:            inp.OffsetLocked,
			Excluded:          inp.Excluded,
			NormalizeExposure: inp.NormalizeExposure,
			ExposureScale:     inp.ExposureScale,
		}
		// A combined input's Path points at the working/ copy; persist the
		// original source file instead so the project references the user's real
		// data (and survives deleting working/, which is regenerated on load).
		if inp.SourcePath != "" {
			mis.Path = inp.SourcePath
			mis.SCIExt = 0
			mis.Combined = true
		}
		if inp.HasManualTransform {
			t := inp.ManualTransform
			mis.TransformA, mis.TransformB, mis.TransformC = t.A, t.B, t.C
			mis.TransformD, mis.TransformE, mis.TransformF = t.D, t.E, t.F
		}
		proj.Inputs = append(proj.Inputs, mis)
	}
	if ws.state.referenceInput != nil {
		proj.ReferencePath = ws.state.referenceInput.Path
		proj.ReferenceSCIExt = ws.state.referenceInput.SCIExt
		if ws.state.referenceInput.SourcePath != "" {
			proj.ReferencePath = ws.state.referenceInput.SourcePath
			proj.ReferenceSCIExt = 0
		}
	}
	name := ws.lastProjectName
	if name == "" {
		name = "mosaic_project.json"
		if ws.activeFilter != "" {
			name = ws.activeFilter + "_project.json"
		}
	}
	opts := []gofiledialog.Option{
		gofiledialog.WithFileName(name),
		gofiledialog.WithFilters(gofiledialog.Filter{Name: "Mosaic projects", Extensions: []string{".json"}}),
	}
	if lastDir := ws.app.Preferences().String("lastDir"); lastDir != "" {
		opts = append(opts, gofiledialog.WithStartDir(lastDir))
	}
	if err := gofiledialog.ShowSave(func(paths []string, err error) {
		if err != nil {
			dialog.ShowError(err, ws.win)
			return
		}
		if len(paths) == 0 {
			return
		}
		path := paths[0]
		if filepath.Ext(path) == "" {
			path += ".json"
		}
		absPath, absErr := filepath.Abs(path)
		if absErr != nil {
			dialog.ShowError(absErr, ws.win)
			return
		}
		// Serialize a copy with portable paths; keep the live settings absolute.
		serialized := proj
		serialized.SkysubSettings.RowDestripeMaskDir = encodeProjectRelativePath(absPath, serialized.SkysubSettings.RowDestripeMaskDir)
		serialized.SkysubSettings.MIRIArtifactMaskDir = encodeProjectRelativePath(absPath, serialized.SkysubSettings.MIRIArtifactMaskDir)
		data, jsonErr := json.MarshalIndent(serialized, "", "  ")
		if jsonErr != nil {
			dialog.ShowError(jsonErr, ws.win)
			return
		}
		if writeErr := writeProjectJSON(path, data, true); writeErr != nil {
			dialog.ShowError(writeErr, ws.win)
			return
		}
		ws.currentProjectPath = absPath
		ws.lastProjectName = filepath.Base(path)
		ws.app.Preferences().SetString("lastDir", filepath.Dir(path))
		dialog.ShowInformation("Saved", "Mosaic project saved.", ws.win)
	}, ws.win, opts...); err != nil {
		dialog.ShowError(err, ws.win)
	}
}

func (ws *mosaicWorkspace) loadMosaicProject() {
	if ws.queueRunning {
		return
	}
	opts := []gofiledialog.Option{
		gofiledialog.WithFilters(gofiledialog.Filter{Name: "Mosaic projects", Extensions: []string{".json"}}),
	}
	if lastDir := ws.app.Preferences().String("lastDir"); lastDir != "" {
		opts = append(opts, gofiledialog.WithStartDir(lastDir))
	}
	if err := gofiledialog.ShowOpen(func(paths []string, err error) {
		if err != nil {
			dialog.ShowError(err, ws.win)
			return
		}
		if len(paths) == 0 {
			return
		}
		path := paths[0]
		absPath, absErr := filepath.Abs(path)
		if absErr != nil {
			dialog.ShowError(absErr, ws.win)
			return
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			dialog.ShowError(readErr, ws.win)
			return
		}
		var proj models.MosaicProject
		if jsonErr := json.Unmarshal(data, &proj); jsonErr != nil {
			dialog.ShowError(jsonErr, ws.win)
			return
		}
		// Keep the live workspace untouched until every staged input has loaded.

		// Reload input FITS files, combining multi-chip exposures on the way in.
		go func() {
			pt := newProgressTracker("Loading Project", "Reading FITS files...", ws.win)
			pt.progress("Discovering GMOS calibration", 0, 1)
			gmosCalibration, calibrationErr := restoreGMOSCalibrationForProject(proj, absPath)
			if pt.ctx.Err() != nil {
				fyne.Do(pt.hide)
				return
			}
			if calibrationErr != nil {
				fyne.Do(func() {
					pt.hide()
					dialog.ShowError(calibrationErr, ws.win)
				})
				return
			}

			// Group entries by source path, preserving first-seen order. Legacy
			// per-chip projects list a multi-chip file once per SCI extension;
			// those groups collapse into a single combined input on load.
			var order []string
			groups := map[string][]models.MosaicInputState{}
			for _, mis := range proj.Inputs {
				mis.Path = resolveProjectRelativePath(absPath, mis.Path)
				if _, ok := groups[mis.Path]; !ok {
					order = append(order, mis.Path)
				}
				groups[mis.Path] = append(groups[mis.Path], mis)
			}

			var newInputs []mosaic.Input
			var newStatuses []mosaic.InputStatus
			var newRef *mosaic.Input
			var loadFailure error
			refLabelText := "Reference: none"
			migratedExposures := 0
			cancelled := false

			for gi, path := range order {
				group := groups[path]
				rep := lowestSCIExtState(group)
				pt.progress(fmt.Sprintf("Loading: %s", filepath.Base(path)), gi+1, len(order))

				inputs, combined, fallbackErr, err := mosaic.LoadInputsForPipeline(path, mosaic.CombineOptions{
					Ctx:             pt.ctx,
					GMOSCalibration: gmosCalibration,
					Progress: func(stage string, done, total int) {
						pt.progress(fmt.Sprintf("%s: %s", filepath.Base(path), stage), done, total)
					},
				})
				if err != nil {
					if err == mosaic.ErrCancelled {
						cancelled = true
						break
					}
					loadFailure = fmt.Errorf("load %s: %w", filepath.Base(path), err)
					break
				}
				if gmosCalibration != nil && fallbackErr != nil {
					loadFailure = fmt.Errorf("calibrated load %s: %w", filepath.Base(path), fallbackErr)
					break
				}
				if len(inputs) == 0 {
					loadFailure = fmt.Errorf("load %s: no inputs loaded", filepath.Base(path))
					break
				}

				if len(inputs) == 1 {
					// One logical input: a combined exposure, or a single-chip file.
					applyInputState(&inputs[0], rep)
					newInputs = append(newInputs, inputs[0])
					newStatuses = append(newStatuses, mosaic.InputStatus{Path: mosaic.InputKey(inputs[0]), Included: true, Status: "loaded"})
					if combined && len(group) > 1 {
						migratedExposures++
					}
					continue
				}

				// Per-chip fallback (combine unavailable): match each chip to its
				// saved entry by SCI extension, falling back to the representative.
				for j := range inputs {
					applyInputState(&inputs[j], stateForSCIExt(group, inputs[j].SCIExt, rep))
					newInputs = append(newInputs, inputs[j])
					newStatuses = append(newStatuses, mosaic.InputStatus{Path: mosaic.InputKey(inputs[j]), Included: true, Status: "loaded"})
				}
			}

			if !cancelled && loadFailure == nil && proj.ReferencePath != "" {
				refPath := resolveProjectRelativePath(absPath, proj.ReferencePath)
				refInputs, refCombined, refFallbackErr, refErr := mosaic.LoadInputsForPipeline(refPath, mosaic.CombineOptions{Ctx: pt.ctx, GMOSCalibration: gmosCalibration})
				if refErr == mosaic.ErrCancelled {
					cancelled = true
				} else if refErr != nil {
					loadFailure = fmt.Errorf("load reference: %w", refErr)
				} else if gmosCalibration != nil && refFallbackErr != nil {
					loadFailure = fmt.Errorf("calibrated load reference: %w", refFallbackErr)
				} else if len(refInputs) > 0 {
					chosen := refInputs[0]
					if !refCombined && proj.ReferenceSCIExt != 0 {
						for _, r := range refInputs {
							if r.SCIExt == proj.ReferenceSCIExt {
								chosen = r
								break
							}
						}
					}
					chosen.ReferenceOnly = true
					newRef = &chosen
					refLabelText = "Reference: " + mosaic.InputLabel(chosen)
				}
			}

			if cancelled || loadFailure != nil {
				fyne.Do(func() {
					pt.hide()
					if cancelled {
						dialog.ShowInformation("Loading Project", "Project load was cancelled; the current workspace was preserved.", ws.win)
					} else {
						dialog.ShowError(loadFailure, ws.win)
					}
				})
				return
			}

			// Project state is restored first, then valid per-image sidecars may
			// replace it once the project's reference input is available.
			sidecars := loadMosaicAlignmentSidecars(newInputs, newStatuses, newRef)

			fyne.Do(func() {
				// A project replacement invalidates any picker bound to the old
				// reference result before swapping inputs and previews.
				if ws.activePicker != nil {
					ws.exitStarMode()
				}
				ws.inputMu.Lock()
				defer ws.inputMu.Unlock()
				pt.hide()
				ws.state.drizzleSettings = proj.DrizzleSettings
				ws.state.drizzleSettingsSet = proj.DrizzleSettingsSet
				ws.state.alignmentSettings = proj.AlignmentSettings
				ws.state.alignmentSettingsSet = proj.AlignmentSettingsSet
				ws.state.skysubSettings = proj.SkysubSettings
				ws.state.skysubSettings = resolveSkysubSettingsForProject(ws.state.skysubSettings, absPath)
				ws.state.skysubSettingsSet = proj.SkysubSettingsSet
				ws.state.artifactMasks = proj.ArtifactMasks
				ws.state.exposureNormMode = mosaic.NormalizationMode(proj.ExposureNormMode)
				ws.state.gmosCalibration = gmosCalibration
				ws.activeFilter = proj.ActiveFilter
				ws.resetMTFMidtone()
				if proj.ActiveFilter != "" {
					ws.loadLevelPrefsAndMode(ws.activeFilter)
				}
				ws.state.inputs = newInputs
				for _, input := range newInputs {
					ws.advanceInputGenerationLocked(input)
				}
				ws.currentProjectPath = absPath
				ws.lastProjectName = filepath.Base(path)
				ws.app.Preferences().SetString("lastDir", filepath.Dir(path))
				ws.state.statuses = newStatuses
				ws.state.referenceInput = newRef
				ws.refLabel.SetText(refLabelText)
				ws.resetPreview()
				ws.rebuildOffsetControls()
				ws.updateStatus()
				ws.updateActionButtons()
				if migratedExposures > 0 {
					dialog.ShowInformation("Project Migrated",
						fmt.Sprintf("Migrated %d multi-chip exposure(s) from per-chip to combined form. Save the project to keep the new layout.", migratedExposures), ws.win)
				}
				if sidecars.ReferenceChangedNotice != "" {
					dialog.ShowInformation("Saved Alignments", sidecars.ReferenceChangedNotice, ws.win)
				}
			})
		}()
	}, ws.win, opts...); err != nil {
		dialog.ShowError(err, ws.win)
	}
}

func restoreGMOSCalibration(proj models.MosaicProject, inputs []mosaic.Input) *mosaic.GMOSCalibrationSelection {
	if !proj.GMOSCalibrationEnabled || len(inputs) == 0 || len(proj.GMOSBiasPaths) == 0 || len(proj.GMOSFlatPaths) == 0 {
		return nil
	}
	m, err := mosaic.DiscoverGMOSCalibration(filepath.Dir(inputs[0].Path))
	if err != nil {
		return nil
	}
	by := map[string]mosaic.GMOSCalibrationFrame{}
	for _, f := range append(append(m.Bias, m.Twilight...), m.BPM...) {
		by[filepath.Clean(f.Path)] = f
	}
	s := &mosaic.GMOSCalibrationSelection{}
	for _, p := range proj.GMOSBiasPaths {
		f, ok := by[filepath.Clean(resolveProjectRelativePath(filepath.Dir(inputs[0].Path), p))]
		if !ok {
			return nil
		}
		s.BiasFrames = append(s.BiasFrames, f)
	}
	for _, p := range proj.GMOSFlatPaths {
		f, ok := by[filepath.Clean(resolveProjectRelativePath(filepath.Dir(inputs[0].Path), p))]
		if !ok {
			return nil
		}
		s.FlatFrames = append(s.FlatFrames, f)
	}
	for _, p := range proj.GMOSBPMPaths {
		if f, ok := by[filepath.Clean(resolveProjectRelativePath(filepath.Dir(inputs[0].Path), p))]; ok {
			s.BPMFrames = append(s.BPMFrames, f)
		}
	}
	if len(s.BiasFrames) == 0 || len(s.FlatFrames) == 0 || mosaic.GMOSCalibrationFingerprint(*s) != proj.GMOSCalibrationFingerprint {
		return nil
	}
	s.Bias = &s.BiasFrames[0]
	s.Flat = &s.FlatFrames[0]
	if len(s.BPMFrames) > 0 {
		s.BPM = &s.BPMFrames[0]
	}
	return s
}

// restoreGMOSCalibrationForProject resolves and validates persisted calibration
// inputs before any science exposure is combined. Missing or incomplete
// recipes are fatal so a project cannot silently revert to uncalibrated data.
func restoreGMOSCalibrationForProject(proj models.MosaicProject, projectPath string) (*mosaic.GMOSCalibrationSelection, error) {
	if !proj.GMOSCalibrationEnabled {
		return nil, nil
	}
	if len(proj.Inputs) == 0 || len(proj.GMOSBiasPaths) == 0 || len(proj.GMOSFlatPaths) == 0 {
		return nil, fmt.Errorf("project contains an incomplete GMOS calibration selection")
	}
	source := resolveProjectRelativePath(projectPath, proj.Inputs[0].Path)
	var selected mosaic.Input
	var selectedFilter, selectedKey string
	seenSources := map[string]bool{}
	for _, state := range proj.Inputs {
		path := resolveProjectRelativePath(projectPath, state.Path)
		if seenSources[path] {
			continue
		}
		seenSources[path] = true
		meta, loadErr := mosaic.LoadInputsMetadataFromPath(path)
		if loadErr != nil || len(meta) == 0 {
			if loadErr == nil {
				loadErr = fmt.Errorf("no inputs found")
			}
			return nil, fmt.Errorf("validate GMOS project input %s: %w", filepath.Base(path), loadErr)
		}
		if selected.Path == "" {
			selected = meta[0]
			selectedFilter = mosaic.FilterStringForInput(selected)
			selectedKey = mosaic.GMOSCompatibilityKeyForPath(path)
		}
		if !mosaic.IsGeminiHeader(meta[0].PrimaryHeader) {
			return nil, fmt.Errorf("GMOS calibration requires a workspace containing only GMOS inputs")
		}
		if got := mosaic.FilterStringForInput(meta[0]); got != selectedFilter {
			return nil, fmt.Errorf("GMOS calibration cannot apply to mixed filters (%s and %s)", selectedFilter, got)
		}
		if got := mosaic.GMOSCompatibilityKeyForPath(path); got == "" || got != selectedKey {
			return nil, fmt.Errorf("GMOS calibration cannot apply to mixed detector/readout configurations")
		}
	}
	m, err := mosaic.DiscoverGMOSCalibration(filepath.Dir(source))
	if err != nil {
		return nil, fmt.Errorf("discover GMOS calibration: %w", err)
	}
	by := map[string]mosaic.GMOSCalibrationFrame{}
	for _, f := range append(append(m.Bias, m.Twilight...), m.BPM...) {
		by[filepath.Clean(f.Path)] = f
	}
	s := &mosaic.GMOSCalibrationSelection{}
	for _, p := range proj.GMOSBiasPaths {
		f, ok := by[filepath.Clean(resolveProjectRelativePath(projectPath, p))]
		if !ok {
			return nil, fmt.Errorf("GMOS bias calibration is missing: %s", p)
		}
		s.BiasFrames = append(s.BiasFrames, f)
	}
	for _, p := range proj.GMOSFlatPaths {
		f, ok := by[filepath.Clean(resolveProjectRelativePath(projectPath, p))]
		if !ok {
			return nil, fmt.Errorf("GMOS twilight flat calibration is missing: %s", p)
		}
		s.FlatFrames = append(s.FlatFrames, f)
	}
	for _, p := range proj.GMOSBPMPaths {
		if f, ok := by[filepath.Clean(resolveProjectRelativePath(projectPath, p))]; ok {
			s.BPMFrames = append(s.BPMFrames, f)
		}
	}
	s.Bias, s.Flat = &s.BiasFrames[0], &s.FlatFrames[0]
	if len(s.BPMFrames) > 0 {
		s.BPM = &s.BPMFrames[0]
	}
	s.RecipeFingerprint = mosaic.GMOSCalibrationFingerprint(*s)
	if err := validateGMOSRecipeForScience(selectedFilter, selectedKey, *s); err != nil {
		return nil, err
	}
	if proj.GMOSCalibrationFingerprint != "" && s.RecipeFingerprint != proj.GMOSCalibrationFingerprint {
		return nil, fmt.Errorf("GMOS calibration recipe has changed since the project was saved")
	}
	if err := mosaic.ValidateGMOSSelection(*s); err != nil {
		return nil, err
	}
	return s, nil
}

func validateGMOSRecipeForScience(scienceFilter, scienceKey string, selection mosaic.GMOSCalibrationSelection) error {
	if scienceKey == "" || selection.Flat == nil || selection.Flat.Key != scienceKey {
		return fmt.Errorf("saved GMOS calibration recipe does not match the raw science detector/readout configuration")
	}
	if scienceFilter == "" || selection.Flat.Filter != scienceFilter {
		return fmt.Errorf("saved GMOS calibration recipe filter %q does not match raw science filter %q", selection.Flat.Filter, scienceFilter)
	}
	return nil
}
