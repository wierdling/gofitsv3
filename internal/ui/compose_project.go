package ui

import (
	"encoding/json"
	"fmt"
	"image"
	"os"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/wierdling/gofiledialog"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/models"
)

func (ws *composeWorkspace) saveProject() {
	hasChannel := false
	project := models.ComposeProject{
		SharedHistogramScale: ws.sharedHistCheck.Checked,
		DisableComposite:     !ws.buildCompositeCheck.Checked,
		MeasureComposite:     ws.measureEnabled,
		BlinkFilters:         ws.blinkCheck.Checked,
		BlinkExcludedFilter:  ws.blinkExcludedIdx,
		PSF:                  ws.psfSettings,
		LRGB:                 ws.lrgbSettings,
		CompositionMode:      ws.compositionMode,
		MixWeights:           append([]models.ComposeMixWeight(nil), ws.mixWeights...),
	}
	if whitening := snapshotStarWhiteningState(ws.starWhitening); whitening != nil {
		project.StarWhitening = whitening
	}
	project.StarStretchGeometryBlinkID = ws.starGeometryBlinkID
	if ws.blinkChannels != nil {
		selection := append([]int(nil), ws.blinkChannels...)
		project.BlinkChannels = &selection
		keys := make([]string, 0, len(selection))
		for _, index := range selection {
			for _, source := range ws.composeBlinkSources() {
				if source.ProjectIndex == index {
					keys = append(keys, source.Key)
					break
				}
			}
		}
		project.BlinkChannelKeys = &keys
	}
	for i := 0; i < 3; i++ {
		if ws.imgs[i] == nil {
			continue
		}
		hasChannel = true
		dx, dy, rot, _ := ws.composeChannelOffsetFields(i)
		project.Channels[i] = models.ChannelState{
			Path:       ws.imgs[i].Path,
			Mode:       modeToLabel(ws.imgs[i].Mode),
			Black:      ws.imgs[i].Black,
			White:      ws.imgs[i].White,
			Background: ws.imgs[i].Background,
			Peak:       ws.imgs[i].Peak,
			ScaledPeak: ws.imgs[i].ScaledPeak,
			ShowClip:   ws.imgs[i].ShowClip,
			OffsetX:    dx,
			OffsetY:    dy,
			OffsetRot:  rot,
			Rotation90: ws.imgs[i].Rotation90,

			HasAlign: ws.imgs[i].HasAlignTransform,
			AlignA:   ws.imgs[i].AlignA,
			AlignB:   ws.imgs[i].AlignB,
			AlignC:   ws.imgs[i].AlignC,
			AlignD:   ws.imgs[i].AlignD,
			AlignE:   ws.imgs[i].AlignE,
			AlignF:   ws.imgs[i].AlignF,

			AsinhScale:  ws.imgs[i].AsinhScale,
			MTFMidtone:  ws.imgs[i].MTFMidtone,
			GHSStretch:  ws.imgs[i].GHSStretch,
			GHSLocal:    ws.imgs[i].GHSLocal,
			GHSSymmetry: ws.imgs[i].GHSSymmetry,

			StarStretch: starStretchStateForProject(ws.imgs[i].StarStretch),
		}
	}
	for _, l := range ws.overlayLayers {
		if l.win == nil {
			continue
		}
		layer := l.settings
		layer.Open = true
		if l.idx < len(ws.imgs) && ws.imgs[l.idx] != nil {
			hasChannel = true
			layer.Channel = channelStateFromImage(ws.imgs[l.idx])
		}
		project.OverlayLayers = append(project.OverlayLayers, layer)
	}
	if !hasChannel {
		dialog.ShowInformation("Nothing to save", "Load at least one channel before saving", ws.win)
		return
	}
	if err := gofiledialog.ShowSave(func(paths []string, err error) {
		if err != nil {
			dialog.ShowError(err, ws.win)
			return
		}
		if len(paths) == 0 {
			return
		}
		data, err := json.MarshalIndent(project, "", "  ")
		if err != nil {
			dialog.ShowError(err, ws.win)
			return
		}
		if err := os.WriteFile(paths[0], data, 0644); err != nil {
			dialog.ShowError(err, ws.win)
			return
		}
	}, ws.win,
		gofiledialog.WithFileName("project.gfprj"),
		gofiledialog.WithFilters(gofiledialog.Filter{Name: "Compose projects", Extensions: []string{".gfprj"}}),
	); err != nil {
		dialog.ShowError(err, ws.win)
	}
}

// snapshotStarWhiteningState returns the project-safe copy of the White Stars
// settings. Forced star overrides are meaningful even while the feature is
// disabled, so they keep the setting present in a saved project. Copy the map
// because project encoding should not retain mutable workspace state.
func snapshotStarWhiteningState(state models.StarWhiteningState) *models.StarWhiteningState {
	if !state.Enabled && len(state.ForcedStars) == 0 {
		return nil
	}
	snapshot := state
	if len(state.ForcedStars) > 0 {
		snapshot.ForcedStars = make(map[int]bool, len(state.ForcedStars))
		for id, forced := range state.ForcedStars {
			snapshot.ForcedStars[id] = forced
		}
	}
	return &snapshot
}

func (ws *composeWorkspace) loadProject() {
	if err := gofiledialog.ShowOpen(func(paths []string, err error) {
		if err != nil {
			dialog.ShowError(err, ws.win)
			return
		}
		if len(paths) == 0 {
			return
		}
		data, err := os.ReadFile(paths[0])
		if err != nil {
			dialog.ShowError(err, ws.win)
			return
		}
		var project models.ComposeProject
		if err := json.Unmarshal(data, &project); err != nil {
			dialog.ShowError(err, ws.win)
			return
		}
		if err := project.ValidateMixWeights(); err != nil {
			dialog.ShowError(fmt.Errorf("invalid Compose mix weights: %w", err), ws.win)
			return
		}

		if globalSelectComposeTab != nil {
			globalSelectComposeTab()
		}

		progressDialog := dialog.NewCustom("Loading Project", "Reading FITS files and restoring saved stretch settings...", widget.NewProgressBarInfinite(), ws.win)
		progressDialog.Show()

		// Normalize overlay layers: new projects carry OverlayLayers; migrate
		// legacy Orange/Yellow layers from older projects into the same list.
		layerStates := append([]models.OrangeLayerState(nil), project.OverlayLayers...)
		if project.OrangeLayer.Open {
			layerStates = append(layerStates, project.OrangeLayer)
		}
		if project.YellowLayer.Open {
			layerStates = append(layerStates, project.YellowLayer)
		}
		if len(layerStates) > maxOverlayLayers {
			layerStates = layerStates[:maxOverlayLayers]
		}
		// Keep a detached snapshot until every incoming source has staged
		// successfully; large-mode failure restores these runtime references.
		previousImgs := append([]*models.LoadedImage(nil), ws.imgs...)
		previousOrig := append([][]float32(nil), ws.origPixels...)
		previousOverlays := append([]*overlayLayer(nil), ws.overlayLayers...)
		previousArtifacts := make(map[int]composeArtifactDescriptor)
		previousPreviews := make(map[int]*image.RGBA)
		if ws.largeMode {
			ws.largeMu.RLock()
			for idx, d := range ws.largeArtifacts {
				previousArtifacts[idx] = d
			}
			for idx, p := range ws.largePreviews {
				previousPreviews[idx] = p
			}
			ws.largeMu.RUnlock()
		}

		// Tear down existing overlay windows and rebuild imgs/origPixels to hold
		// the 3 base RGB channels plus one slot per incoming layer.
		for _, l := range ws.overlayLayers {
			if l.win != nil {
				l.win.SetCloseIntercept(nil)
				l.win.Close()
			}
		}
		ws.overlayLayers = nil
		ws.imgs = ws.imgs[:3+len(layerStates)]
		ws.origPixels = ws.origPixels[:3+len(layerStates)]
		for i := 3; i < len(ws.imgs); i++ {
			ws.imgs[i] = nil
			ws.origPixels[i] = nil
		}
		for i, st := range layerStates {
			s := st
			s.Open = true
			s = normalizeComposeOverlayState(s, i)
			ws.nextLayerNumber++
			ws.overlayLayers = append(ws.overlayLayers, &overlayLayer{
				idx:      3 + i,
				name:     fmt.Sprintf("Layer %d", ws.nextLayerNumber),
				settings: s,
			})
		}

		go func() {
			type loadResult struct {
				idx      int
				img      *models.LoadedImage
				preview  *image.RGBA
				artifact composeArtifactDescriptor
				state    models.ChannelState
				err      error
			}

			total := len(ws.imgs)
			stateForIdx := func(i int) models.ChannelState {
				if i < 3 {
					return project.Channels[i]
				}
				return layerStates[i-3].Channel
			}
			results := make([]loadResult, 0, total)
			errors := make([]string, 0, total)
			if ws.largeMode {
				// Large-mode project loads are deliberately sequential. Each source is
				// staged through the artifact transaction before its bounded preview is
				// generated; no legacy full-pixel loader is used.
				for i := 0; i < total; i++ {
					state := stateForIdx(i)
					res := loadResult{idx: i, state: state}
					if state.Path != "" {
						slot := fmt.Sprintf("project-%d", i)
						if ws.largeStore == nil {
							res.err = fmt.Errorf("disk-backed Compose store is unavailable")
						} else {
							img, preview, artifact, loadErr := loadLargeComposeImage(state.Path, ws.largeStore, slot)
							res.img, res.preview, res.artifact, res.err = img, preview, artifact, loadErr
							if res.err == nil {
								turns := state.Rotation90 % 4
								for turns > 0 {
									artifact, res.err = ws.largeStore.RotateArtifact90CW(artifact)
									if res.err != nil {
										break
									}
									res.artifact = artifact
									img.HDU.Data.Width, img.HDU.Data.Height = artifact.Width, artifact.Height
									turns--
								}
								if res.err == nil && state.Rotation90%4 != 0 {
									res.preview, _, _, res.err = composeLargeStretchedPreview(artifact.Path, img)
								}
							}
						}
					}
					results = append(results, res)
				}
			} else {
				resultsCh := make(chan loadResult, total)
				var wg sync.WaitGroup
				for i := 0; i < total; i++ {
					state := stateForIdx(i)
					if state.Path == "" {
						results = append(results, loadResult{idx: i, state: state})
						continue
					}
					wg.Add(1)
					go func(idx int, state models.ChannelState) {
						defer wg.Done()
						img, loadErr := loadImageFromPath(state.Path)
						resultsCh <- loadResult{idx: idx, img: img, state: state, err: loadErr}
					}(i, state)
				}
				go func() { wg.Wait(); close(resultsCh) }()
				for res := range resultsCh {
					results = append(results, res)
				}
			}
			for _, res := range results {
				if res.state.Path == "" {
					ws.imgs[res.idx] = nil
					continue
				}
				if res.err != nil {
					label := fmt.Sprintf("Channel %d", res.idx+1)
					if res.idx >= 3 {
						label = fmt.Sprintf("Layer %d", res.idx-2)
					}
					errors = append(errors, fmt.Sprintf("%s: %v", label, res.err))
					continue
				}
				ws.imgs[res.idx] = res.img
				if ws.largeMode {
					ws.largeMu.Lock()
					ws.largeArtifacts[res.idx] = res.artifact
					ws.largePreviews[res.idx] = res.preview
					ws.largeMu.Unlock()
				}
				clearComposeOrigPixels(&ws.origPixels, res.idx)
			}
			if ws.largeMode && len(errors) > 0 {
				// Project replacement is all-or-none in disk mode: discard every
				// staged artifact when any source fails, including artifacts already
				// published into the temporary result maps above.
				for _, res := range results {
					if res.artifact.Slot != "" {
						_, _ = ws.largeStore.RemoveSlotIfCurrent(res.artifact)
					}
					ws.imgs[res.idx] = nil
					ws.largeMu.Lock()
					delete(ws.largeArtifacts, res.idx)
					delete(ws.largePreviews, res.idx)
					ws.largeMu.Unlock()
				}
			}

			fyne.Do(func() {
				if ws.largeMode && len(errors) > 0 {
					// Restore the detached runtime snapshot before reporting failure.
					restoreComposeLargeProjectSnapshot(&ws.imgs, &ws.origPixels, &ws.overlayLayers, &ws.largeArtifacts, &ws.largePreviews, previousImgs, previousOrig, previousOverlays, previousArtifacts, previousPreviews)
					ws.largeMu.Lock()
					ws.largeArtifacts = previousArtifacts
					ws.largePreviews = previousPreviews
					ws.largeRuntime.artifacts = ws.largeArtifacts
					ws.largeRuntime.previews = ws.largePreviews
					ws.largeMu.Unlock()
					for _, l := range ws.overlayLayers {
						if l.win != nil {
							l.win.SetCloseIntercept(nil)
							l.win.Close()
						}
					}
					for _, l := range previousOverlays {
						l.win, l.viewport, l.control = nil, nil, nil
						ws.openOverlayLayerWindow(l)
					}
					ws.blinkChannels = nil
					ws.blinkCheck.SetChecked(false)
					ws.stopBlink()
					dialog.ShowError(fmt.Errorf("%s", strings.Join(errors, "\n")), ws.win)
					progressDialog.Hide()
					return
				} else if ws.largeMode {
					// Commit succeeded; old session slots can now be reclaimed.
					ws.largeMu.RLock()
					currentArtifacts := make(map[int]composeArtifactDescriptor, len(ws.largeArtifacts))
					for idx, d := range ws.largeArtifacts {
						currentArtifacts[idx] = d
					}
					ws.largeMu.RUnlock()
					for idx, old := range previousArtifacts {
						if cur, ok := currentArtifacts[idx]; !ok || cur.Path != old.Path {
							_, _ = ws.largeStore.RemoveSlotIfCurrent(old)
						}
					}
				}
				debuglog.Log("load compose project: applying loaded project state")
				ws.withSuspendedRefresh(func() {
					for i, img := range ws.imgs[:3] {
						if img != nil {
							if ws.largeMode {
								img.Rotation90 = project.Channels[i].Rotation90
							} else {
								restoreComposeChannelRotation(img, project.Channels[i].Rotation90)
							}
							applyChannelState(i, project.Channels[i], ws.imgs, ws.viewports, ws.controlSets)
						}
					}
					ws.sharedHistCheck.SetChecked(project.SharedHistogramScale)
					ws.buildCompositeCheck.SetChecked(!project.DisableComposite)
					ws.psfSettings = project.PSF
					ws.compositionMode = project.CompositionMode
					ws.mixWeights = append([]models.ComposeMixWeight(nil), project.MixWeights...)
					ws.starWhitening = models.StarWhiteningState{Strength: .75, Level: models.StarWhiteningWhite, Red: true, Green: true, Blue: true}
					if project.StarWhitening != nil {
						ws.starWhitening = *project.StarWhitening
					}
					ws.starGeometryBlinkID = project.StarStretchGeometryBlinkID
					ws.lrgbMu.Lock()
					ws.dedicatedL = nil
					ws.lrgbSettings = project.LRGB
					ws.lrgbGeneration++
					ws.lrgbMu.Unlock()
					ws.stopBlink()
					ws.blinkCheck.SetChecked(false)
					ws.blinkExcludedIdx = clampComposeBlinkFilter(project.BlinkExcludedFilter)
					if project.BlinkChannelKeys != nil {
						ws.blinkChannels = resolveComposeBlinkKeys(*project.BlinkChannelKeys, ws.composeBlinkSources())
					} else if project.BlinkChannels != nil {
						selection := make([]int, len(*project.BlinkChannels))
						copy(selection, *project.BlinkChannels)
						ws.blinkChannels = resolveComposeBlinkSelection(ws.composeBlinkSources(), selection, project.BlinkFilters, project.BlinkExcludedFilter)
					} else {
						ws.blinkChannels = resolveComposeBlinkSelection(ws.composeBlinkSources(), nil, project.BlinkFilters, project.BlinkExcludedFilter)
					}
					if ws.largeMode {
						ws.blinkCheck.SetChecked(false)
						ws.blinkChannels = nil
					} else {
						ws.blinkCheck.SetChecked(project.BlinkFilters)
					}
					ws.measureEnabled = project.MeasureComposite
					if ws.measureCheck != nil {
						ws.measureCheck.SetChecked(ws.measureEnabled)
					}
					if !ws.measureEnabled {
						ws.measureStart = nil
						ws.measureEnd = nil
					}
					ws.updateHistScaleLabel()
					ws.updateBlinkStatus()
					ws.updateMeasurement()
				})
				debuglog.Log("load compose project: loaded images applied; refreshing previews asynchronously")
				for idx := range ws.headerWins {
					ws.closeHeaderWindow(idx)
				}
				ws.updateMenus()
				for _, l := range ws.overlayLayers {
					ws.openOverlayLayerWindow(l)
					if l.idx < len(ws.imgs) && ws.imgs[l.idx] != nil && l.control != nil {
						applyChannelState(l.idx, layerStates[l.idx-3].Channel, ws.imgs, ws.layerViews(l), ws.layerControls(l))
						ws.refreshLayerPreview(l)
					}
				}
				if len(errors) > 0 {
					dialog.ShowError(fmt.Errorf("%s", strings.Join(errors, "\n")), ws.win)
				}
				ws.refreshAsync(func() {
					if !ws.largeMode && ws.blinkCheck.Checked {
						ws.startBlink()
					}
					progressDialog.Hide()
					debuglog.Log("load compose project: previews refreshed")
				})
			})
		}()
	}, ws.win,
		gofiledialog.WithFilters(gofiledialog.Filter{Name: "Compose projects", Extensions: []string{".gfprj"}}),
	); err != nil {
		dialog.ShowError(err, ws.win)
	}
}

type vpState struct {
	zoom    float64
	zoomSel string
	offset  fyne.Position
}

func (ws *composeWorkspace) captureViewportStates() []vpState {
	states := make([]vpState, len(ws.viewports))
	for i, vp := range ws.viewports {
		if vp != nil {
			states[i] = vpState{
				zoom:    vp.zoom,
				zoomSel: vp.zoomLabel.Selected,
				offset:  vp.scroll.Offset,
			}
		}
	}
	return states
}

func (ws *composeWorkspace) restoreViewportStates(states []vpState) {
	for i, vp := range ws.viewports {
		if vp != nil && i < len(states) {
			vp.zoom = states[i].zoom
			vp.setZoomLabelValue(states[i].zoomSel)
			vp.applyZoom()
			vp.scroll.Offset = states[i].offset
			vp.scroll.Refresh()
		}
	}
}
