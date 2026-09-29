package ui

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

const composeLargeFilesPreferenceKey = "compose.largeFiles"

// globalSendToChannel is registered by newComposeWorkspace and called by the
// preview window to load an image directly into a compose channel with all
// stretch settings already applied.
var globalSendToChannel func(channelIdx int, img *models.LoadedImage)

// globalSelectComposeTab is registered by app.go and called by loadProject in
// workspace_compose.go to switch to the Compose tab when loading a Compose project.
var globalSelectComposeTab func()

// globalComposeLargeCleanup is invoked by the application close handler.
var globalComposeLargeCleanup func()
var composeLargeModeActive func() bool

type composeLRGBContextKey struct{}

type composeLRGBSnapshot struct {
	settings   models.LRGBSettings
	dedicated  *models.LoadedImage
	generation uint64
}

func composeLRGBPublishAllowed(currentGeneration, requestedGeneration uint64, currentPath, requestedPath string) bool {
	return currentGeneration == requestedGeneration && currentPath == requestedPath
}

func newComposeWorkspace(app fyne.App, win fyne.Window) (fyne.CanvasObject, []*fyne.Menu) {
	ws := &composeWorkspace{app: app, win: win}
	activeComposeWorkspace = ws

	ws.imgs = make([]*models.LoadedImage, 3, 3+maxOverlayLayers)
	ws.origPixels = make([][]float32, 3, 3+maxOverlayLayers)
	ws.viewports = []*viewport{newViewport(), newViewport(), newViewport(), newViewport()}
	// Channel histograms: black background, channel-colored bars; compose: white bars.
	ws.viewports[0].histColor = [4]uint8{100, 149, 237, 255} // blue
	ws.viewports[1].histColor = [4]uint8{80, 200, 80, 255}   // green
	ws.viewports[2].histColor = [4]uint8{237, 80, 80, 255}   // red
	ws.viewports[3].histColor = [4]uint8{255, 255, 255, 255} // white (compose)
	ws.headerWins = make([]fyne.Window, 3)
	ws.levels = defaultRGBLevels()
	ws.psfSettings = models.PSFSettings{}
	ws.lrgbSettings = models.LRGBSettings{}
	ws.compositionMode = models.ComposeModeAuto

	// renderImages returns an offset-applied view of imgs (Manual Offsets applied
	// at render time). Forward-declared so refresh/compose can use it; assigned
	// once controlSets exists.
	ws.suspendRefresh = false
	ws.previewSeq = 0
	ws.blinkSeq = 0

	ws.sharedHistCheck = NewToggle(nil)
	ws.sharedHistCheck.SetChecked(false)
	ws.buildCompositeCheck = NewToggle(nil)
	ws.buildCompositeCheck.SetChecked(false)
	ws.blinkCheck = NewToggle(nil)
	ws.blinkCheck.SetChecked(false)
	ws.largeFilesCheck = NewToggle(nil)
	ws.largeFilesCheck.SetChecked(ws.app.Preferences().Bool(composeLargeFilesPreferenceKey))
	ws.largeMode = ws.largeFilesCheck.Checked
	composeLargeModeActive = ws.isLargeModeActive
	ws.starTreatments = newComposeStarTreatments(ws.win, func() bool { return ws.largeMode })
	ws.starWhitening = models.StarWhiteningState{Strength: .75, Level: models.StarWhiteningWhite, Red: true, Green: true, Blue: true}
	ws.starTreatments.whiteningRef = ws.whiteningReference
	ws.starGeometryBlinkID = ""
	ws.starTreatments.geometryRef = func() *models.LoadedImage {
		return whiteningReferenceImage(ws.imgs, ws.composeSourceBlinkIDs(), ws.starGeometryBlinkID)
	}
	ws.largePreviews = make(map[int]*image.RGBA)
	ws.largeArtifacts = make(map[int]composeArtifactDescriptor)
	ws.largeRuntime = &largeChannelRuntime{store: nil, artifacts: ws.largeArtifacts, previews: ws.largePreviews, mu: &ws.largeMu, refresh: nil, syncWidgets: make(map[int]func(*models.LoadedImage))}
	ws.largeLoadGenerations = make(map[string]uint64)
	ws.largeSessionGeneration = uint64(1)
	if ws.largeMode {
		ws.largeStore, _ = newComposeLargeStore("")
		if ws.largeStore == nil {
			ws.largeMode = false
			ws.largeFilesCheck.SetChecked(false)
			ws.app.Preferences().SetBool(composeLargeFilesPreferenceKey, false)
		}
	}
	ws.blinkExcludedIdx = 0
	ws.composeMagicPreset = widget.NewSelect([]string{"Balanced", "Nebula", "Galaxy"}, nil)
	ws.composeMagicPreset.SetSelected("Balanced")

	ws.starTreatments.refresh = ws.refresh
	ws.sharedHistCheck.OnChanged = func(bool) {
		ws.updateHistScaleLabel()
		ws.refresh()
	}
	ws.buildCompositeCheck.OnChanged = func(bool) {
		ws.refresh()
	}

	ws.activePicker = composePicker{channel: -1}
	for i := 0; i < 3; i++ {
		idx := i
		ws.viewports[idx].SetLevelPickers(
			func() { ws.setPicker(idx, "Black") },
			func() { ws.setPicker(idx, "White") },
		)
		ws.viewports[idx].overlay.onPointerMove = func(pos fyne.Position) {
			point, ok := ws.viewports[idx].imagePointAtPosition(pos, false)
			if !ok {
				if ws.activePicker.channel == idx {
					ws.viewports[idx].SetPickerValueText(fmt.Sprintf("Pick %s: --", ws.activePicker.target))
				} else {
					ws.viewports[idx].SetPickerValueText("Value: --")
				}
				return
			}
			ws.updatePickerValue(idx, point)
		}
		ws.viewports[idx].overlay.onPointerOut = func() {
			if ws.activePicker.channel == idx {
				ws.viewports[idx].SetPickerValueText(fmt.Sprintf("Pick %s: --", ws.activePicker.target))
				return
			}
			ws.viewports[idx].SetPickerValueText("Value: --")
		}
		ws.viewports[idx].overlay.onTapped = func(pos fyne.Position) {
			if ws.starDiagEnabled {
				ws.channelStarDiagnosticTapped(idx, pos)
				return
			}
			if ws.activePicker.channel != idx {
				return
			}
			point, ok := ws.viewports[idx].imagePointAtPosition(pos, false)
			if !ok {
				return
			}
			value, ok := composeRegionMedianAt(ws.imgs[idx], point, composePickRadius)
			if !ok {
				return
			}
			if ws.activePicker.target == "Black" {
				ws.imgs[idx].Black = value
				ws.viewports[idx].blackBox.SetValue(value)
			} else {
				ws.imgs[idx].White = value
				ws.viewports[idx].whiteBox.SetValue(value)
			}
			ws.clearPicker()
			ws.refresh()
		}
		ws.viewports[idx].onViewChanged = func() {
			ws.setStarDiagnosticOverlay(ws.viewports[idx], ws.starDiagChannelPts[idx])
		}
		ws.viewports[idx].overlay.onResized = func(fyne.Size) {
			ws.setStarDiagnosticOverlay(ws.viewports[idx], ws.starDiagChannelPts[idx])
		}
	}

	ws.nextLayerNumber = 0

	ws.largeRuntime.store = ws.largeStore
	ws.controlSets = []*models.ChannelControl{
		channelControls("Channel 1 (Blue)", color.RGBA{R: 100, G: 149, B: 237, A: 255}, 0, ws.imgs, &ws.origPixels, ws.viewports, ws.refresh, ws.composeMagicPreset, true, ws.largeRuntime),
		channelControls("Channel 2 (Green)", color.RGBA{R: 80, G: 200, B: 80, A: 255}, 1, ws.imgs, &ws.origPixels, ws.viewports, ws.refresh, ws.composeMagicPreset, true, ws.largeRuntime),
		channelControls("Channel 3 (Red)", color.RGBA{R: 237, G: 80, B: 80, A: 255}, 2, ws.imgs, &ws.origPixels, ws.viewports, ws.refresh, ws.composeMagicPreset, true, ws.largeRuntime),
	}

	ws.renderCache = make([]composeRenderCache, len(ws.imgs))

	ws.viewports[0].SetLoadSave("Blue", "B", color.RGBA{R: 100, G: 149, B: 237, A: 255},
		func() { ws.loadChannel(0) }, func() { ws.saveChannelGray(0) })
	ws.viewports[1].SetLoadSave("Green", "G", color.RGBA{R: 80, G: 200, B: 80, A: 255},
		func() { ws.loadChannel(1) }, func() { ws.saveChannelGray(1) })
	ws.viewports[2].SetLoadSave("Red", "R", color.RGBA{R: 237, G: 80, B: 80, A: 255},
		func() { ws.loadChannel(2) }, func() { ws.saveChannelGray(2) })
	ws.viewports[3].SetCenterAction("Composite", "C", color.RGBA{R: 200, G: 110, B: 30, A: 255},
		"Export to Edit", func() {
			if globalExportToEdit == nil {
				return
			}
			if ws.largeMode {
				if ws.largeStore != nil {
					if d, ok := ws.largeStore.Composite(); ok {
						ws.startLargeEditSnapshot(d, *ws.levels)
						return
					}
				}
				dialog.ShowInformation("Build Composite first", "Build the composite before sending it to Edit.", ws.win)
				return
			}
			img := ws.compositeImageForEdit()
			if img == nil {
				dialog.ShowInformation("Nothing to export", "Compose all three channels first.", ws.win)
				return
			}
			if err := globalExportToEdit(editImageHandoff{memory: img}); err != nil {
				dialog.ShowError(err, ws.win)
			}
		})

	ws.measureLabel = widget.NewLabel("Measure: --")
	ws.measureLabel.TextStyle = fyne.TextStyle{Monospace: true}

	ws.measureCheck = NewToggle(func(v bool) {
		ws.measureEnabled = v
		if !v {
			ws.measureStart = nil
			ws.measureEnd = nil
			ws.updateMeasurement()
		}
	})

	ws.starDiagCheck = NewToggle(func(v bool) {
		ws.starDiagEnabled = v
		ws.recomputeStarDiagnostics()
	})

	ws.viewports[3].overlay.onTapped = func(pos fyne.Position) {
		if ws.starDiagEnabled {
			ws.starDiagnosticTapped(pos)
			return
		}
		if !ws.measureEnabled {
			return
		}
		point, ok := ws.viewports[3].imagePointAtPosition(pos, false)
		if !ok {
			return
		}
		if ws.measureStart == nil || ws.measureEnd != nil {
			ws.measureStart = &imagePoint{X: point.X, Y: point.Y}
			ws.measureEnd = nil
		} else {
			ws.measureEnd = &imagePoint{X: point.X, Y: point.Y}
		}
		ws.updateMeasurement()
	}

	ws.viewports[3].onViewChanged = func() {
		ws.updateMeasurement()
		ws.setStarDiagnosticOverlay(ws.viewports[3], ws.starDiagPoints)
	}
	// SetMinSize alone (from a zoom change) does not synchronously resize this
	// overlay; the actual size change lands later, on Fyne's own layout pass.
	// onResized observes that directly so a pane/window resize (which changes
	// no zoom setting and triggers no new render) still repositions the baked
	// measurement and diagnostic overlay markers.
	ws.viewports[3].overlay.onResized = func(fyne.Size) {
		ws.updateMeasurement()
		ws.setStarDiagnosticOverlay(ws.viewports[3], ws.starDiagPoints)
	}

	ws.starDiagLabel = widget.NewLabel("")
	ws.starDiagLabel.Wrapping = fyne.TextWrapWord

	ws.blinkStatus = widget.NewLabel("")
	ws.blinkStatus.Wrapping = fyne.TextWrapWord
	ws.blinkFrame = 0

	ws.blinkCheck.OnChanged = func(v bool) {
		if v {
			ws.startBlink()
			return
		}
		ws.stopBlink()
		ws.updateBlinkStatus()
		ws.refresh()
	}
	ws.updateBlinkStatus()

	//alignBtn := widget.NewButton("1. Align to Channel 2 (Green)", alignChannels)
	//crossCleanBtn := widget.NewButton("2. Cross-Channel Clean", crossChannelClean)

	menus := ws.buildMenus()
	ws.updateMenus()
	ws.largeFilesCheck.OnChanged = func(enabled bool) {
		hasOverlayImage := false
		for _, layer := range ws.overlayLayers {
			if layer != nil && layer.idx >= 0 && layer.idx < len(ws.imgs) && ws.imgs[layer.idx] != nil {
				hasOverlayImage = true
				break
			}
		}
		if enabled && (ws.imgs[0] != nil || ws.imgs[1] != nil || ws.imgs[2] != nil || hasOverlayImage) {
			ws.largeFilesCheck.SetChecked(false)
			return
		}
		if enabled {
			store, err := newComposeLargeStore("")
			if err != nil {
				ws.largeFilesCheck.SetChecked(false)
				dialog.ShowError(err, ws.win)
				return
			}
			ws.largeStore = store
			ws.largeRuntime.store = store
			ws.largeMode = true
			ws.blinkCheck.SetChecked(false)
			ws.blinkCheck.Disable()
			ws.app.Preferences().SetBool(composeLargeFilesPreferenceKey, true)
		} else {
			ws.largeMode = false
			ws.app.Preferences().SetBool(composeLargeFilesPreferenceKey, false)
			ws.largeMu.Lock()
			ws.largeSessionGeneration++
			store := ws.largeStore
			ws.largeStore = nil
			ws.largeMu.Unlock()
			if store != nil {
				_ = store.Close()
			}
			ws.largeMu.Lock()
			ws.largePreviews = make(map[int]*image.RGBA)
			ws.largeArtifacts = make(map[int]composeArtifactDescriptor)
			ws.largeRuntime.previews = ws.largePreviews
			ws.largeRuntime.artifacts = ws.largeArtifacts
			ws.largeMu.Unlock()
		}
		ws.updateMenus()
	}
	globalComposeLargeCleanup = ws.cleanupLargeMode

	globalSendToChannel = ws.sendToChannel

	return ws.buildLayout(), menus
}

func labelToMode(label string) stretch.Mode {
	switch strings.ToLower(label) {
	case "log":
		return stretch.Log
	case "asinh":
		return stretch.Asinh
	case "sqrt":
		return stretch.Sqrt
	case "histeq":
		return stretch.HistEq
	case "mtf":
		return stretch.MTF
	case "ghs":
		return stretch.GHS
	default:
		return stretch.Linear
	}
}
