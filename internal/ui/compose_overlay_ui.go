package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/export"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// layerViews / layerControls build the sparse (idx+1)-length slices that
// channelControls and applyChannelState expect: the layer's viewport/control
// sits at its own idx and every earlier slot is nil.
func (ws *composeWorkspace) layerViews(l *overlayLayer) []*viewport {
	v := make([]*viewport, l.idx+1)
	v[l.idx] = l.viewport
	return v
}

func (ws *composeWorkspace) layerControls(l *overlayLayer) []*models.ChannelControl {
	c := make([]*models.ChannelControl, l.idx+1)
	c[l.idx] = l.control
	return c
}

func (ws *composeWorkspace) applyLayerPreview(l *overlayLayer, data *composeOverlayPreviewData) {
	l.viewport.image.Image = data.image
	l.viewport.bins = data.bins
	l.viewport.histMax = 0
	l.viewport.origW = data.width
	l.viewport.origH = data.height
	l.viewport.blackBox.SetValue(ws.imgs[l.idx].Black)
	l.viewport.whiteBox.SetValue(ws.imgs[l.idx].White)
	if l.viewport.StatsLabel != nil {
		l.viewport.StatsLabel.SetText(fmt.Sprintf("Sky %.3f  μ %.3f  σ %.3f", data.sky, data.mean, data.std))
	}
	l.viewport.SetFilterText(data.filterText)
	l.viewport.histogram.Refresh()
	if l.viewport.zoomLabel.Selected == "fit" {
		l.viewport.zoom = l.viewport.fitZoom()
	}
	l.viewport.applyZoom()
	l.viewport.image.Refresh()
}

func (ws *composeWorkspace) refreshLayerPreview(l *overlayLayer) {
	if ws.suspendRefresh {
		return
	}
	if l == nil || l.viewport == nil {
		ws.refresh()
		return
	}
	if l.idx >= len(ws.imgs) || ws.imgs[l.idx] == nil {
		l.viewport.image.Image = blankImg()
		l.viewport.bins = [256]int{}
		l.viewport.histMax = 0
		l.viewport.blackBox.SetValue(0)
		l.viewport.whiteBox.SetValue(0)
		if l.viewport.StatsLabel != nil {
			l.viewport.StatsLabel.SetText("Sky --  μ --  σ --")
		}
		l.viewport.SetFilterText("")
		l.viewport.histogram.Refresh()
		l.viewport.image.Refresh()
		ws.refresh()
		return
	}
	if composeLargeModeActive != nil && composeLargeModeActive() {
		ws.largeMu.RLock()
		p := ws.largePreviews[l.idx]
		ws.largeMu.RUnlock()
		if p != nil {
			l.viewport.image.Image = p
			l.viewport.image.Refresh()
		}
		ws.refresh()
		return
	}
	data, err := buildComposeOverlayPreviewData(context.Background(), ws.imgs[l.idx])
	if err != nil {
		return
	}
	ws.applyLayerPreview(l, data)
	ws.refresh()
}

func (ws *composeWorkspace) saveLayerGray(l *overlayLayer) {
	if ws.largeMode {
		if l == nil || l.idx >= len(ws.imgs) || ws.imgs[l.idx] == nil {
			dialog.ShowInformation("Missing", "Load the layer image first", ws.win)
			return
		}
		d, ok := ws.largeArtifacts[l.idx]
		if !ok {
			dialog.ShowInformation("Missing", "The layer artifact is unavailable.", ws.win)
			return
		}
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()
			format := ws.detectExportFormat(path)
			showExportOptionsDialog(format, ws.win, func(opts export.Options) {
				if err := export.FromFloat32Artifact(context.Background(), path, d.Path, d.Width, d.Height, format, opts, ws.imgs[l.idx]); err != nil {
					dialog.ShowError(err, ws.win)
				}
			})
		}, ws.win)
		save.SetFileName("layer_gray.png")
		save.Show()
		return
	}
	if l.idx >= len(ws.imgs) || ws.imgs[l.idx] == nil {
		dialog.ShowInformation("Missing", "Load the layer image first", ws.win)
		return
	}
	save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
		if err != nil || uc == nil {
			return
		}
		path := uc.URI().Path()
		_ = uc.Close()
		format := ws.detectExportFormat(path)
		stretched, _ := processing.ApplyStretchParallel(ws.imgs[l.idx])
		gray := processing.ToGrayRGBA(stretched, make([]byte, len(stretched.Pixels)))
		showExportOptionsDialog(format, ws.win, func(opts export.Options) {
			if err := export.FromImage(path, gray, format, opts); err != nil {
				dialog.ShowError(err, ws.win)
			}
		})
	}, ws.win)
	save.SetFileName("layer_gray.png")
	save.Show()
}

func (ws *composeWorkspace) loadLayer(l *overlayLayer) {
	showSingleFITSOpenDialog(ws.app, ws.win, func(path string) {
		progressDialog := dialog.NewCustom("Loading Layer Image", "Reading FITS data...", widget.NewProgressBarInfinite(), ws.win)
		progressDialog.Show()
		diskLoad := ws.largeMode
		go func() {
			slot := fmt.Sprintf("overlay-%d", l.idx)
			request, session, store := ws.nextLargeLoadGeneration(slot)
			var img *models.LoadedImage
			var preview *image.RGBA
			var artifact composeArtifactDescriptor
			var loadErr error
			if diskLoad {
				if store == nil {
					loadErr = errors.New("disk-backed Compose store is unavailable")
				} else {
					img, preview, artifact, loadErr = loadLargeComposeImage(path, store, slot)
				}
			} else {
				img, loadErr = loadImageFromPath(path)
			}
			if loadErr != nil {
				fyne.Do(func() {
					progressDialog.Hide()
					if ws.loadRequestStillCurrent(slot, request, session) {
						dialog.ShowError(loadErr, ws.win)
					}
				})
				return
			}
			if diskLoad && !ws.largeLoadStillCurrent(slot, request, session, artifact) {
				ws.cleanupLargeArtifactIfCurrent(artifact)
				return
			}
			if !diskLoad && !ws.loadRequestStillCurrent(slot, request, session) {
				fyne.Do(func() { progressDialog.Hide() })
				return
			}
			if diskLoad {
				ws.largeMu.Lock()
				current, currentOK := store.Descriptor(slot)
				if ws.largeStore != store || ws.largeSessionGeneration != session || ws.largeLoadGenerations[slot] != request || !currentOK || current.Generation != artifact.Generation || current.Path != artifact.Path {
					ws.largeMu.Unlock()
					ws.cleanupLargeArtifactIfCurrent(artifact)
					return
				}
				ws.largePreviews[l.idx] = preview
				ws.largeArtifacts[l.idx] = artifact
				ws.imgs[l.idx] = img
				ws.largeMu.Unlock()
			}
			fyne.Do(func() {
				if !diskLoad {
					if !ws.loadRequestStillCurrent(slot, request, session) {
						progressDialog.Hide()
						return
					}
					ws.imgs[l.idx] = img
				}
				clearComposeOrigPixels(&ws.origPixels, l.idx)
				if l.control != nil {
					applyChannelState(l.idx, channelStateFromImage(img), ws.imgs, ws.layerViews(l), ws.layerControls(l))
				}
				progressDialog.Hide()
				ws.refreshLayerPreview(l)
				ws.updateMenus()
			})
		}()
	})
}

func (ws *composeWorkspace) removeLayer(l *overlayLayer) {
	oldSources := ws.composeBlinkSources()
	for i, x := range ws.overlayLayers {
		if x == l {
			ws.overlayLayers = append(ws.overlayLayers[:i], ws.overlayLayers[i+1:]...)
			break
		}
	}
	if l.idx < len(ws.imgs) {
		// Invalidate every pending load, including normal in-memory loads,
		// before removing the slot so a late completion cannot restore it.
		ws.invalidateLargeSlot(l.idx)
		ws.imgs[l.idx] = nil
		if ws.largeMode && ws.largeStore != nil {
			ws.largeMu.Lock()
			store := ws.largeStore
			d := ws.largeArtifacts[l.idx]
			delete(ws.largeArtifacts, l.idx)
			delete(ws.largePreviews, l.idx)
			ws.largeMu.Unlock()
			if d.Slot != "" && store != nil {
				_, _ = store.RemoveSlotIfCurrent(d)
			}
		}
		clearComposeOrigPixels(&ws.origPixels, l.idx)
	}
	l.win = nil
	l.viewport = nil
	l.control = nil
	if ws.blinkChannels != nil {
		ws.blinkChannels = remapComposeBlinkSelection(ws.blinkChannels, oldSources, ws.composeBlinkSources())
	}
}

func (ws *composeWorkspace) openOverlayLayerWindowWithPreview(l *overlayLayer, preparedPreview *composeOverlayPreviewData) {
	if l.win != nil {
		l.win.Show()
		l.win.RequestFocus()
		ws.refresh()
		return
	}
	l.viewport = newViewport()
	l.viewport.histColor = [4]uint8{l.settings.ColorR, l.settings.ColorG, l.settings.ColorB, 255}

	activePicker := ""
	clearPicker := func() {
		activePicker = ""
		l.viewport.overlay.pickerActive = false
		l.viewport.overlay.Refresh()
		l.viewport.SetPickerValueText("Value: --")
	}
	setPicker := func(target string) {
		if activePicker == target {
			clearPicker()
			return
		}
		activePicker = target
		l.viewport.overlay.pickerActive = true
		l.viewport.overlay.Refresh()
		l.viewport.SetPickerValueText(fmt.Sprintf("Pick %s: --", target))
	}
	l.viewport.SetLevelPickers(
		func() { setPicker("Black") },
		func() { setPicker("White") },
	)
	l.viewport.overlay.onPointerMove = func(pos fyne.Position) {
		point, ok := l.viewport.imagePointAtPosition(pos, false)
		if !ok {
			if activePicker != "" {
				l.viewport.SetPickerValueText(fmt.Sprintf("Pick %s: --", activePicker))
			} else {
				l.viewport.SetPickerValueText("Value: --")
			}
			return
		}
		var value float64
		var okv bool
		if activePicker != "" {
			value, okv = composeRegionMedianAt(ws.imgs[l.idx], point, composePickRadius)
		} else {
			value, okv = composePixelValueAt(ws.imgs[l.idx], point)
		}
		if !okv {
			if activePicker != "" {
				l.viewport.SetPickerValueText(fmt.Sprintf("Pick %s: --", activePicker))
			} else {
				l.viewport.SetPickerValueText("Value: --")
			}
			return
		}
		if activePicker != "" {
			l.viewport.SetPickerValueText(fmt.Sprintf("Pick %s: %.6g", activePicker, value))
			return
		}
		l.viewport.SetPickerValueText(fmt.Sprintf("Value: %.6g", value))
	}
	l.viewport.overlay.onPointerOut = func() {
		if activePicker != "" {
			l.viewport.SetPickerValueText(fmt.Sprintf("Pick %s: --", activePicker))
			return
		}
		l.viewport.SetPickerValueText("Value: --")
	}
	l.viewport.overlay.onTapped = func(pos fyne.Position) {
		if activePicker == "" {
			return
		}
		point, ok := l.viewport.imagePointAtPosition(pos, false)
		if !ok {
			return
		}
		value, ok := composeRegionMedianAt(ws.imgs[l.idx], point, composePickRadius)
		if !ok {
			return
		}
		if activePicker == "Black" {
			ws.imgs[l.idx].Black = value
			l.viewport.blackBox.SetValue(value)
		} else {
			ws.imgs[l.idx].White = value
			l.viewport.whiteBox.SetValue(value)
		}
		clearPicker()
		ws.refreshLayerPreview(l)
	}

	l.viewport.SetLoadSave(l.name, "L", color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255},
		func() { ws.loadLayer(l) }, func() { ws.saveLayerGray(l) })
	ws.largeRuntime.store = ws.largeStore
	l.control = channelControls(l.name+" Image", color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255}, l.idx, ws.imgs, &ws.origPixels, ws.layerViews(l), func() { ws.refreshLayerPreview(l) }, ws.composeMagicPreset, false, ws.largeRuntime)

	swatch := canvas.NewRectangle(color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255})
	swatch.SetMinSize(fyne.NewSize(36, 18))
	updateSwatch := func() {
		col := color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255}
		swatch.FillColor = col
		swatch.Refresh()
		l.viewport.histColor = [4]uint8{l.settings.ColorR, l.settings.ColorG, l.settings.ColorB, 255}
		l.viewport.histogram.Refresh()
	}
	colorLabelWidth := float32(0)
	for _, s := range []string{"Red", "Green", "Blue"} {
		if w := widget.NewLabel(s).MinSize().Width; w > colorLabelWidth {
			colorLabelWidth = w
		}
	}
	colorLabel := func(text string) fyne.CanvasObject {
		lb := widget.NewLabel(text)
		return container.New(layout.NewGridWrapLayout(fyne.NewSize(colorLabelWidth, lb.MinSize().Height)), lb)
	}
	colorSlider := func(label string, value uint8, set func(uint8)) fyne.CanvasObject {
		slider := widget.NewSlider(0, 255)
		slider.Step = 1
		slider.Value = float64(value)
		valueEntry := NewNumberEntry(1, 0)
		valueEntry.Min = 0
		valueEntry.Max = 255
		valueEntry.MinWidth = 70
		valueEntry.SetValue(float64(value))
		slider.OnChanged = func(v float64) {
			n := uint8(math.Round(v))
			set(n)
			valueEntry.SetValue(float64(n))
			updateSwatch()
		}
		valueEntry.OnChanged = func(v float64) {
			n := uint8(math.Round(v))
			set(n)
			slider.Value = float64(n)
			slider.Refresh()
			updateSwatch()
		}
		return container.NewBorder(nil, nil, colorLabel(label), valueEntry, slider)
	}
	opacitySlider := widget.NewSlider(0, 100)
	opacitySlider.Step = 1
	opacitySlider.Value = l.settings.Opacity * 100
	opacityValue := NewNumberEntry(1, 0)
	opacityValue.Min = 0
	opacityValue.Max = 100
	opacityValue.MinWidth = 70
	opacityValue.SetValue(opacitySlider.Value)
	opacitySlider.OnChanged = func(v float64) {
		l.settings.Opacity = v / 100
		opacityValue.SetValue(v)
		ws.refresh()
	}
	opacityValue.OnChanged = func(v float64) {
		l.settings.Opacity = v / 100
		opacitySlider.Value = v
		opacitySlider.Refresh()
		ws.refresh()
	}
	protectSlider := widget.NewSlider(0, 100)
	protectSlider.Step = 1
	protectSlider.Value = l.settings.HighlightProtect * 100
	protectValue := NewNumberEntry(1, 0)
	protectValue.Min = 0
	protectValue.Max = 100
	protectValue.MinWidth = 70
	protectValue.SetValue(protectSlider.Value)
	protectSlider.OnChanged = func(v float64) {
		l.settings.HighlightProtect = v / 100
		protectValue.SetValue(v)
		ws.refresh()
	}
	protectValue.OnChanged = func(v float64) {
		l.settings.HighlightProtect = v / 100
		protectSlider.Value = v
		protectSlider.Refresh()
		ws.refresh()
	}

	if l.idx < len(ws.imgs) && ws.imgs[l.idx] != nil {
		applyChannelState(l.idx, channelStateFromImage(ws.imgs[l.idx]), ws.imgs, ws.layerViews(l), ws.layerControls(l))
	}
	colorControls := container.NewVBox(
		canvas.NewText("Overlay", color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255}),
		container.NewHBox(widget.NewLabel("Color"), swatch),
		colorSlider("Red", l.settings.ColorR, func(v uint8) { l.settings.ColorR = v }),
		colorSlider("Green", l.settings.ColorG, func(v uint8) { l.settings.ColorG = v }),
		colorSlider("Blue", l.settings.ColorB, func(v uint8) { l.settings.ColorB = v }),
		container.NewBorder(nil, nil, widget.NewLabel("Opacity"), container.NewHBox(opacityValue, widget.NewLabel("%")), opacitySlider),
		container.NewBorder(nil, nil, widget.NewLabel("Highlight protect"), container.NewHBox(protectValue, widget.NewLabel("%")), protectSlider),
	)
	controls := container.NewVScroll(container.NewVBox(l.control.Content, colorControls))
	controls.SetMinSize(fyne.NewSize(300, 200))
	l.win = ws.app.NewWindow(l.name + " Image")
	shield := newTapShield()
	l.win.SetContent(container.NewStack(container.NewBorder(nil, nil, controls, nil, l.viewport.container), shield))
	l.win.Resize(fyne.NewSize(900, 600))
	l.win.SetCloseIntercept(func() {
		l.win.SetCloseIntercept(nil)
		l.win.Close()
		ws.removeLayer(l)
		ws.refresh()
		ws.updateMenus()
	})
	if preparedPreview != nil {
		ws.applyLayerPreview(l, preparedPreview)
		ws.refresh()
	} else {
		ws.refreshLayerPreview(l)
	}
	l.win.Show()
	// Newly created floating windows have their controls tapped before Fyne's
	// canvas cache is populated by the first paint pass, which crashes any
	// widget.Select tapped that early (CanvasForObject returns nil). The
	// shield eats input for a couple of frames to close that race.
	go func() {
		time.Sleep(200 * time.Millisecond)
		fyne.Do(func() { shield.Hide() })
	}()
}

func (ws *composeWorkspace) openOverlayLayerWindow(l *overlayLayer) {
	ws.openOverlayLayerWindowWithPreview(l, nil)
}

// freeOverlaySlot returns an idx for a new layer, reusing a freed hole when
// available, else growing imgs/origPixels (bounded by maxOverlayLayers).
func (ws *composeWorkspace) freeOverlaySlot() (int, bool) {
	used := make(map[int]bool, len(ws.overlayLayers))
	for _, l := range ws.overlayLayers {
		used[l.idx] = true
	}
	for i := 3; i < len(ws.imgs); i++ {
		if !used[i] && ws.imgs[i] == nil {
			return i, true
		}
	}
	if len(ws.imgs) >= 3+maxOverlayLayers {
		return 0, false
	}
	idx := len(ws.imgs)
	ws.imgs = append(ws.imgs, nil)
	ws.origPixels = append(ws.origPixels, nil)
	return idx, true
}

func (ws *composeWorkspace) createOverlayLayerAt(idx int, settings models.OrangeLayerState) *overlayLayer {
	for len(ws.imgs) <= idx {
		ws.imgs = append(ws.imgs, nil)
		ws.origPixels = append(ws.origPixels, nil)
	}
	ws.nextLayerNumber++
	if settings.BlinkID == "" {
		settings.BlinkID = fmt.Sprintf("overlay-%d", ws.nextLayerNumber)
	}
	l := &overlayLayer{
		idx:      idx,
		name:     fmt.Sprintf("Layer %d", ws.nextLayerNumber),
		settings: settings,
	}
	ws.overlayLayers = append(ws.overlayLayers, l)
	return l
}

func (ws *composeWorkspace) createOverlayLayer(settings models.OrangeLayerState) (*overlayLayer, bool) {
	idx, ok := ws.freeOverlaySlot()
	if !ok {
		return nil, false
	}
	return ws.createOverlayLayerAt(idx, settings), true
}

func (ws *composeWorkspace) addColoredLayer() {
	showSingleFITSOpenDialog(ws.app, ws.win, func(path string) {
		progressDialog := dialog.NewCustom("Loading Layer Image", "Reading FITS data...", widget.NewProgressBarInfinite(), ws.win)
		progressDialog.Show()
		diskLoad := ws.largeMode
		go func() {
			// The staging slot is unique per request and becomes the runtime
			// descriptor slot once the overlay is created below.
			slot := fmt.Sprintf("overlay-stage-%d", time.Now().UnixNano())
			request, session, store := ws.nextLargeLoadGeneration(slot)
			var img *models.LoadedImage
			var preview *image.RGBA
			var artifact composeArtifactDescriptor
			var loadErr error
			if diskLoad {
				// The runtime slot index is assigned only after the load succeeds;
				// use a unique staging slot and publish it under the assigned index.
				if store == nil {
					loadErr = errors.New("disk-backed Compose store is unavailable")
				} else {
					img, preview, artifact, loadErr = loadLargeComposeImage(path, store, slot)
				}
			} else {
				img, loadErr = loadImageFromPath(path)
			}
			fyne.Do(func() {
				progressDialog.Hide()
				if loadErr != nil {
					dialog.ShowError(loadErr, ws.win)
					return
				}
				if diskLoad && !ws.largeLoadStillCurrent(slot, request, session, artifact) {
					ws.cleanupLargeArtifactIfCurrent(artifact)
					return
				}
				l, ok := ws.createOverlayLayer(defaultOverlayLayerSettings(len(ws.overlayLayers)))
				if !ok {
					if diskLoad {
						ws.cleanupLargeArtifactIfCurrent(artifact)
					}
					dialog.ShowInformation("Layer limit", fmt.Sprintf("A maximum of %d colored layers is supported.", maxOverlayLayers), ws.win)
					return
				}
				ws.imgs[l.idx] = img
				if diskLoad {
					ws.largeMu.Lock()
					current, currentOK := store.Descriptor(artifact.Slot)
					if ws.largeStore != store || ws.largeSessionGeneration != session || ws.largeLoadGenerations[artifact.Slot] != request || !currentOK || current.Generation != artifact.Generation || current.Path != artifact.Path {
						ws.largeMu.Unlock()
						ws.cleanupLargeArtifactIfCurrent(artifact)
						return
					}
					ws.largePreviews[l.idx] = preview
					ws.largeArtifacts[l.idx] = artifact
					ws.largeMu.Unlock()
				}
				ws.openOverlayLayerWindow(l)
				ws.updateMenus()
			})
		}()
	})
}

// gatherLegendEntries snapshots the currently loaded base channels and the
// active overlay layers into color/name rows for the Compose color legend.
func (ws *composeWorkspace) gatherLegendEntries() []legendEntry {
	var entries []legendEntry
	base := []struct {
		name string
		col  color.RGBA
	}{
		{"Blue", color.RGBA{R: 100, G: 149, B: 237, A: 255}},
		{"Green", color.RGBA{R: 80, G: 200, B: 80, A: 255}},
		{"Red", color.RGBA{R: 237, G: 80, B: 80, A: 255}},
	}
	for i, b := range base {
		if i < len(ws.imgs) && ws.imgs[i] != nil {
			filter := fitsio.FilterString(ws.imgs[i].Primary)
			name := filter
			if name == "" {
				name = b.name
			}
			entries = append(entries, legendEntry{name: name, filter: filter, path: ws.imgs[i].Path, color: b.col})
		}
	}
	for _, l := range ws.overlayLayers {
		if l == nil || l.win == nil {
			continue
		}
		path, filter := "", ""
		if l.idx < len(ws.imgs) && ws.imgs[l.idx] != nil {
			path = ws.imgs[l.idx].Path
			filter = fitsio.FilterString(ws.imgs[l.idx].Primary)
		}
		name := filter
		if name == "" {
			name = l.name
		}
		entries = append(entries, legendEntry{
			name:   name,
			filter: filter,
			path:   path,
			color:  color.RGBA{R: l.settings.ColorR, G: l.settings.ColorG, B: l.settings.ColorB, A: 255},
		})
	}
	sortLegendEntriesByHue(entries)
	return entries
}

func (ws *composeWorkspace) showColorLegend() {
	showLegendNameDialog(ws.win, ws.gatherLegendEntries(), func(named []legendEntry) {
		showColorLegendWindow(ws.app, ws.win, named)
	})
}
