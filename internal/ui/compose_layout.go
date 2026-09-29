package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// buildMenus creates the Compose menu items (kept on ws where updateMenus
// toggles them) and returns the File, Compose and View menus.
func (ws *composeWorkspace) buildMenus() []*fyne.Menu {
	ws.saveProjectItem = fyne.NewMenuItem("Save Compose Project", ws.saveProject)
	loadProjectItem := fyne.NewMenuItem("Load Compose Project", ws.loadProject)
	loadFilterSetItem := fyne.NewMenuItem("Load Filter Set...", ws.loadFilterSet)
	ws.exportRGBItem = fyne.NewMenuItem("Export Compose RGB", ws.exportRGB)

	ws.viewHeaderItems = []*fyne.MenuItem{
		fyne.NewMenuItem("View FITS Header 1", func() { ws.showHeader(0) }),
		fyne.NewMenuItem("View FITS Header 2", func() { ws.showHeader(1) }),
		fyne.NewMenuItem("View FITS Header 3", func() { ws.showHeader(2) }),
	}
	ws.saveHeaderItems = []*fyne.MenuItem{
		fyne.NewMenuItem("Save FITS Header 1...", func() { ws.saveHeader(0) }),
		fyne.NewMenuItem("Save FITS Header 2...", func() { ws.saveHeader(1) }),
		fyne.NewMenuItem("Save FITS Header 3...", func() { ws.saveHeader(2) }),
	}

	ws.copySettingsItem = fyne.NewMenuItem("Copy Channel 1 Settings to 2 & 3", ws.copySettings)
	composeWeightsItem := fyne.NewMenuItem("Color Mixing...", ws.showComposeWeights)
	starStretchPreviewItem := fyne.NewMenuItem("Gentler Star Stretch Preview...", func() { showComposeStarStretchPreviewDialog(ws.win, ws.imgs) })
	whiteStarsItem := fyne.NewMenuItem("White Stars...", func() {
		roles := []string{"Blue", "Green", "Red"}
		for _, l := range ws.overlayLayers {
			for len(roles) <= l.idx {
				roles = append(roles, "")
			}
			roles[l.idx] = "Layer " + l.name
		}
		showComposeWhiteStarsDialog(ws.win, ws.imgs, roles, ws.composeSourceBlinkIDs(), &ws.starWhitening, ws.starTreatments, func() {
			ws.starTreatments.sync(ws.imgs, true)
			ws.refresh()
		})
	})
	starStretchItem := fyne.NewMenuItem("Gentler Star Stretch...", func() {
		roles := []string{"Blue", "Green", "Red"}
		for _, l := range ws.overlayLayers {
			for len(roles) <= l.idx {
				roles = append(roles, "")
			}
			roles[l.idx] = "Layer " + l.name
		}
		showComposeStarTreatmentDialog(ws.win, ws.imgs, roles, ws.composeSourceBlinkIDs(), &ws.starGeometryBlinkID, ws.starTreatments, ws.refresh)
	})
	createStarMapItem := fyne.NewMenuItem("Create Star Map", func() {
		if activeMosaicWorkspace != nil {
			activeMosaicWorkspace.createStarMapDialog()
		}
	})
	pickMissedStarsItem := fyne.NewMenuItem("Pick Missed Stars", func() {
		if activeMosaicWorkspace != nil {
			activeMosaicWorkspace.pickMissedStarsDialog()
		}
	})
	ws.matchStretchItem = fyne.NewMenuItem("Match Channel Stretch...", ws.showMatchStretchDialog)
	addLayerItem := fyne.NewMenuItem("Add Colored Layer...", ws.addColoredLayer)
	ws.normalizeScaleItem = fyne.NewMenuItem("Normalize Scale to Channel 2", ws.normalizeScale)
	ws.sendToEditItem = fyne.NewMenuItem("Send Composite to Edit", ws.sendToEdit)
	ws.alignChannelsItem = fyne.NewMenuItem("Align to Channel 2", ws.alignChannels)
	ws.cleanChannelsItem = fyne.NewMenuItem("Cross-Channel Clean", ws.crossChannelClean)
	ws.resetDataItem = fyne.NewMenuItem("Reset Data (Undo Align & Clean)", ws.resetData)
	psfItem := fyne.NewMenuItem("Match Channel PSF...", func() { showComposePSFDialog(ws.win, ws.imgs, ws.refresh, ws.largeMode, &ws.psfSettings) })
	lrgbItem := fyne.NewMenuItem("LRGB Combination...", func() {
		showComposeLRGBDialog(ws.win, &ws.lrgbSettings, func() {
			ws.lrgbMu.Lock()
			ws.lrgbGeneration++
			ws.lrgbMu.Unlock()
			ws.refresh()
		}, &ws.lrgbMu)
	})
	loadDedicatedLItem := fyne.NewMenuItem("Load Dedicated L...", ws.loadDedicatedL)
	clearDedicatedLItem := fyne.NewMenuItem("Clear Dedicated L", ws.clearDedicatedL)

	// File: project I/O and handing the result off to other tabs / disk.
	fileMenu := fyne.NewMenu("File",
		loadProjectItem,
		loadFilterSetItem,
		ws.saveProjectItem,
		fyne.NewMenuItemSeparator(),
		ws.exportRGBItem,
		ws.sendToEditItem,
	)
	// Compose: everything that acts on the channels themselves (merge of the
	// former Channels and Process menus).
	composeMenu := fyne.NewMenu("Compose",
		ws.alignChannelsItem,
		ws.cleanChannelsItem,
		ws.resetDataItem,
		fyne.NewMenuItemSeparator(),
		ws.copySettingsItem,
		ws.matchStretchItem,
		ws.normalizeScaleItem,
		psfItem,
		lrgbItem,
		composeWeightsItem,
		starStretchItem,
		whiteStarsItem,
		starStretchPreviewItem,
		createStarMapItem,
		pickMissedStarsItem,
		loadDedicatedLItem,
		clearDedicatedLItem,
		fyne.NewMenuItemSeparator(),
		addLayerItem,
	)
	// View: display tuning plus the per-channel FITS header viewers/savers.
	viewMenu := fyne.NewMenu("View",
		fyne.NewMenuItem("RGB Levels...", ws.openLevels),
		fyne.NewMenuItem("Color Legend...", ws.showColorLegend),
		fyne.NewMenuItemSeparator(),
		ws.viewHeaderItems[0],
		ws.viewHeaderItems[1],
		ws.viewHeaderItems[2],
		fyne.NewMenuItemSeparator(),
		ws.saveHeaderItems[0],
		ws.saveHeaderItems[1],
		ws.saveHeaderItems[2],
	)

	return []*fyne.Menu{fileMenu, composeMenu, viewMenu}
}

// buildLayout assembles the controls panel and the 2x2 viewport grid
// (with per-viewport maximize/restore) and returns the root split.
func (ws *composeWorkspace) buildLayout() fyne.CanvasObject {
	channelTabs := NewChannelTabs(
		NewChannelTabItem("Blue", color.RGBA{R: 100, G: 149, B: 237, A: 255}, ws.controlSets[0].Content),
		NewChannelTabItem("Green", color.RGBA{R: 80, G: 200, B: 80, A: 255}, ws.controlSets[1].Content),
		NewChannelTabItem("Red", color.RGBA{R: 237, G: 80, B: 80, A: 255}, ws.controlSets[2].Content),
	)

	clearBtn := widget.NewButton("Clear Channels", ws.clearChannels)
	clearBtn.Importance = widget.DangerImportance

	resetBtn := widget.NewButton("Reset", ws.resetCompose)
	resetBtn.Importance = widget.DangerImportance

	ws.histScaleStatus = widget.NewLabel("")
	ws.histScaleStatus.Wrapping = fyne.TextWrapWord
	ws.updateHistScaleLabel()

	ws.magicAll = widget.NewButton("Magic", ws.runMagicAll)
	ws.magicAll.Importance = widget.HighImportance

	options := container.NewVBox(
		container.NewHBox(widget.NewLabel("Magic preset"), ws.composeMagicPreset),
		container.NewHBox(ws.sharedHistCheck, widget.NewLabel("Shared histogram scale")),
		ws.histScaleStatus,
		container.NewHBox(ws.largeFilesCheck, widget.NewLabel("Disk-backed large files (slow)")),
		container.NewHBox(ws.blinkCheck, widget.NewLabel("Blink filters")),
		widget.NewButton("Choose channels...", ws.chooseBlinkChannels),
		ws.blinkStatus,
		container.NewHBox(ws.measureCheck, widget.NewLabel("Measure composite")),
		container.NewHBox(ws.starDiagCheck, widget.NewLabel("Star treatment diagnostics")),
		clearBtn,
		resetBtn,
	)
	channels := container.NewVBox(
		ws.magicAll,
		container.NewHBox(ws.buildCompositeCheck, widget.NewLabel("Build color composite")),
		widget.NewSeparator(),
		ws.measureLabel,
		ws.starDiagLabel,
		widget.NewSeparator(),
		channelTabs,
	)
	controls := widget.NewAccordion(
		widget.NewAccordionItem("Options", options),
		widget.NewAccordionItem("Channels", channels),
	)
	controls.MultiOpen = true
	controls.Open(1)

	controlsScroll := container.NewVScroll(controls)
	controlsScroll.SetMinSize(fyne.NewSize(260, 200))

	// Build border containers explicitly so we can swap viewport content for maximize/restore.
	bColors := [4]color.RGBA{
		{100, 149, 237, 255},
		{80, 200, 80, 255},
		{237, 80, 80, 255},
		{220, 220, 220, 255},
	}
	borders := make([]*fyne.Container, 4)
	borderRects := make([]*canvas.Rectangle, 4)
	borderedObjects := func(idx int) []fyne.CanvasObject {
		return []fyne.CanvasObject{
			borderRects[idx],
			container.NewPadded(ws.viewports[idx].container),
		}
	}
	for i := range ws.viewports {
		rect := canvas.NewRectangle(color.Transparent)
		rect.StrokeColor = bColors[i]
		rect.StrokeWidth = 1
		rect.CornerRadius = 6
		borderRects[i] = rect
		borders[i] = container.NewMax(borderedObjects(i)...)
	}

	grid := container.NewGridWithColumns(2, borders[0], borders[1], borders[2], borders[3])

	var split *container.Split
	maximizedIdx := -1

	var restore func()
	var maximize func(idx int)

	restore = func() {
		if maximizedIdx < 0 {
			return
		}
		i := maximizedIdx
		borders[i].Objects = borderedObjects(i)
		borders[i].Refresh()
		maximizedIdx = -1
		split.Trailing = grid
		split.Refresh()
	}

	maximize = func(idx int) {
		if maximizedIdx >= 0 {
			i := maximizedIdx
			borders[i].Objects = borderedObjects(i)
			borders[i].Refresh()
		}
		maximizedIdx = idx
		if idx >= 0 && idx < 3 {
			channelTabs.SetActive(idx)
		}
		borders[idx].Objects = []fyne.CanvasObject{borderRects[idx]}
		borders[idx].Refresh()

		restoreBar := container.NewHBox(
			newCompactBtn("Restore", func() { restore() }),
		)
		split.Trailing = container.NewBorder(restoreBar, nil, nil, nil, ws.viewports[idx].container)
		split.Refresh()
	}

	for i := range ws.viewports {
		i := i
		ws.viewports[i].actionRow.Objects = append(ws.viewports[i].actionRow.Objects,
			newCompactBtn("Max", func() { maximize(i) }),
			hpad(20),
		)
		ws.viewports[i].actionRow.Refresh()
	}

	split = container.NewHSplit(controlsScroll, grid)
	split.SetOffset(0.32)
	return split
}
