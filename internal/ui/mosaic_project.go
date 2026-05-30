package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

func (ws *mosaicWorkspace) saveMosaicProject() {
	proj := models.MosaicProject{
		DrizzleSettings:      ws.state.drizzleSettings,
		DrizzleSettingsSet:   ws.state.drizzleSettingsSet,
		AlignmentSettings:    ws.state.alignmentSettings,
		AlignmentSettingsSet: ws.state.alignmentSettingsSet,
		SkysubSettings:       ws.state.skysubSettings,
		SkysubSettingsSet:    ws.state.skysubSettingsSet,
		ActiveFilter:         ws.activeFilter,
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
		data, jsonErr := json.MarshalIndent(proj, "", "  ")
		if jsonErr != nil {
			dialog.ShowError(jsonErr, ws.win)
			return
		}
		if writeErr := os.WriteFile(path, data, 0644); writeErr != nil {
			dialog.ShowError(writeErr, ws.win)
			return
		}
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
	fd.Show()
}

func (ws *mosaicWorkspace) loadMosaicProject() {
	fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil || r == nil {
			return
		}
		path := r.URI().Path()
		r.Close()
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
		if proj.ActiveFilter != "" {
			ws.activeFilter = proj.ActiveFilter
			ws.loadLevelPrefsAndMode(ws.activeFilter)
		}

		// Reload input FITS files.
		progressDialog := dialog.NewCustom("Loading Project", "Reading FITS files...", widget.NewProgressBarInfinite(), ws.win)
		progressDialog.Show()
		go func() {
			var newInputs []mosaic.Input
			var newStatuses []mosaic.InputStatus
			var newRef *mosaic.Input
			refLabelText := "Reference: none"

			for _, mis := range proj.Inputs {
				loadedInputs, loadErr := mosaic.LoadInputsFromPath(mis.Path)
				if loadErr != nil {
					newStatuses = append(newStatuses, mosaic.InputStatus{Path: mis.Path, Status: "failed", Error: loadErr.Error()})
					continue
				}
				matched := false
				for _, loaded := range loadedInputs {
					if mis.SCIExt != 0 && loaded.SCIExt != mis.SCIExt {
						continue
					}
					inp := loaded
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
					newInputs = append(newInputs, inp)
					newStatuses = append(newStatuses, mosaic.InputStatus{Path: mosaic.InputKey(inp), Included: true, Status: "loaded"})
					matched = true
					break
				}
				if !matched {
					newStatuses = append(newStatuses, mosaic.InputStatus{Path: mis.Path, Status: "failed", Error: fmt.Sprintf("missing sci,%d in %s", mis.SCIExt, filepath.Base(mis.Path))})
				}
			}

			if proj.ReferencePath != "" {
				refInputs, refErr := mosaic.LoadInputsFromPath(proj.ReferencePath)
				if refErr == nil {
					for _, loaded := range refInputs {
						if proj.ReferenceSCIExt != 0 && loaded.SCIExt != proj.ReferenceSCIExt {
							continue
						}
						loaded.ReferenceOnly = true
						newRef = &loaded
						refLabelText = "Reference: " + mosaic.InputLabel(loaded)
						break
					}
				}
			}

			fyne.Do(func() {
				ws.state.inputs = newInputs
				ws.state.statuses = newStatuses
				ws.state.referenceInput = newRef
				ws.refLabel.SetText(refLabelText)
				progressDialog.Hide()
				ws.resetPreview()
				ws.rebuildOffsetControls()
				ws.updateStatus()
			})
		}()
	}, ws.win)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
	ws.configureLastDir(fd)
	fd.SetView(dialog.ListView)
	fd.Show()
}
