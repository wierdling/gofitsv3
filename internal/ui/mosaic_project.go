package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"

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
	proj := models.MosaicProject{
		DrizzleSettings:      ws.state.drizzleSettings,
		DrizzleSettingsSet:   ws.state.drizzleSettingsSet,
		AlignmentSettings:    ws.state.alignmentSettings,
		AlignmentSettingsSet: ws.state.alignmentSettingsSet,
		SkysubSettings:       ws.state.skysubSettings,
		SkysubSettingsSet:    ws.state.skysubSettingsSet,
		ActiveFilter:         ws.activeFilter,
		ArtifactMasks:        ws.state.artifactMasks,
	}
	for _, inp := range ws.state.inputs {
		mis := models.MosaicInputState{
			Path:         inp.Path,
			SCIExt:       inp.SCIExt,
			OffsetX:      inp.OffsetX,
			OffsetY:      inp.OffsetY,
			HasTransform: inp.HasManualTransform,
			Locked:       inp.OffsetLocked,
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
	fd := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
		if err != nil || uc == nil {
			return
		}
		path := uc.URI().Path()
		_ = uc.Close()
		if filepath.Ext(path) == "" {
			path += ".json"
		}
		absPath, absErr := filepath.Abs(path)
		if absErr != nil {
			dialog.ShowError(absErr, ws.win)
			return
		}
		proj.SkysubSettings.RowDestripeMaskDir = encodeProjectRelativePath(absPath, proj.SkysubSettings.RowDestripeMaskDir)
		proj.SkysubSettings.MIRIArtifactMaskDir = encodeProjectRelativePath(absPath, proj.SkysubSettings.MIRIArtifactMaskDir)
		data, jsonErr := json.MarshalIndent(proj, "", "  ")
		if jsonErr != nil {
			dialog.ShowError(jsonErr, ws.win)
			return
		}
		if writeErr := os.WriteFile(path, data, 0644); writeErr != nil {
			dialog.ShowError(writeErr, ws.win)
			return
		}
		ws.currentProjectPath = absPath
		ws.state.skysubSettings.RowDestripeMaskDir = proj.SkysubSettings.RowDestripeMaskDir
		ws.state.skysubSettings.MIRIArtifactMaskDir = proj.SkysubSettings.MIRIArtifactMaskDir
		ws.lastProjectName = filepath.Base(path)
		ws.app.Preferences().SetString("lastDir", filepath.Dir(path))
		dialog.ShowInformation("Saved", "Mosaic project saved.", ws.win)
	}, ws.win)
	name := ws.lastProjectName
	if name == "" {
		name = "mosaic_project.json"
		if ws.activeFilter != "" {
			name = ws.activeFilter + "_project.json"
		}
	}
	fd.SetFileName(name)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
	ws.configureLastDir(fd)
	sizeFileDialog(fd)
	fd.Show()
}

func (ws *mosaicWorkspace) loadMosaicProject() {
	fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil || r == nil {
			return
		}
		path := r.URI().Path()
		r.Close()
		absPath, absErr := filepath.Abs(path)
		if absErr != nil {
			dialog.ShowError(absErr, ws.win)
			return
		}
		ws.currentProjectPath = absPath
		ws.lastProjectName = filepath.Base(path)
		ws.app.Preferences().SetString("lastDir", filepath.Dir(path))
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

		ws.state.drizzleSettings = proj.DrizzleSettings
		ws.state.drizzleSettingsSet = proj.DrizzleSettingsSet
		ws.state.alignmentSettings = proj.AlignmentSettings
		ws.state.alignmentSettingsSet = proj.AlignmentSettingsSet
		ws.state.skysubSettings = proj.SkysubSettings
		ws.state.skysubSettingsSet = proj.SkysubSettingsSet
		ws.state.artifactMasks = proj.ArtifactMasks
		ws.resetMTFMidtone()
		if proj.ActiveFilter != "" {
			ws.activeFilter = proj.ActiveFilter
			ws.loadLevelPrefsAndMode(ws.activeFilter)
		}

		// Reload input FITS files, combining multi-chip exposures on the way in.
		go func() {
			pt := newProgressTracker("Loading Project", "Reading FITS files...", ws.win)

			// Group entries by source path, preserving first-seen order. Legacy
			// per-chip projects list a multi-chip file once per SCI extension;
			// those groups collapse into a single combined input on load.
			var order []string
			groups := map[string][]models.MosaicInputState{}
			for _, mis := range proj.Inputs {
				if _, ok := groups[mis.Path]; !ok {
					order = append(order, mis.Path)
				}
				groups[mis.Path] = append(groups[mis.Path], mis)
			}

			var newInputs []mosaic.Input
			var newStatuses []mosaic.InputStatus
			var newRef *mosaic.Input
			refLabelText := "Reference: none"
			migratedExposures := 0
			cancelled := false

			for gi, path := range order {
				group := groups[path]
				rep := lowestSCIExtState(group)
				pt.progress(fmt.Sprintf("Loading: %s", filepath.Base(path)), gi+1, len(order))

				inputs, combined, _, err := mosaic.LoadInputsForPipeline(path, mosaic.CombineOptions{
					Ctx: pt.ctx,
					Progress: func(stage string, done, total int) {
						pt.progress(fmt.Sprintf("%s: %s", filepath.Base(path), stage), done, total)
					},
				})
				if err != nil {
					if err == mosaic.ErrCancelled {
						cancelled = true
						break
					}
					newStatuses = append(newStatuses, mosaic.InputStatus{Path: path, Status: "failed", Error: err.Error()})
					continue
				}
				if len(inputs) == 0 {
					newStatuses = append(newStatuses, mosaic.InputStatus{Path: path, Status: "failed", Error: fmt.Sprintf("no inputs loaded from %s", filepath.Base(path))})
					continue
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

			if !cancelled && proj.ReferencePath != "" {
				refInputs, refCombined, _, refErr := mosaic.LoadInputsForPipeline(proj.ReferencePath, mosaic.CombineOptions{Ctx: pt.ctx})
				if refErr == mosaic.ErrCancelled {
					cancelled = true
				} else if refErr == nil && len(refInputs) > 0 {
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

			if cancelled {
				fyne.Do(func() {
					pt.hide()
					dialog.ShowInformation("Loading Project", "Project load was cancelled.", ws.win)
				})
				return
			}

			fyne.Do(func() {
				pt.hide()
				ws.state.inputs = newInputs
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
			})
		}()
	}, ws.win)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
	ws.configureLastDir(fd)
	fd.SetView(dialog.ListView)
	sizeFileDialog(fd)
	fd.Show()
}
