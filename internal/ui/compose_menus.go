package ui

import ()

func (ws *composeWorkspace) updateMenus() {
	for i := range ws.viewHeaderItems {
		disabled := ws.imgs[i] == nil
		ws.viewHeaderItems[i].Disabled = disabled
		ws.saveHeaderItems[i].Disabled = disabled
	}
	ws.copySettingsItem.Disabled = ws.imgs[0] == nil
	ws.matchStretchItem.Disabled = ws.imgs[0] == nil && ws.imgs[1] == nil && ws.imgs[2] == nil

	allLoaded := ws.imgs[0] != nil && ws.imgs[1] != nil && ws.imgs[2] != nil

	ws.normalizeScaleItem.Disabled = !allLoaded
	ws.alignChannelsItem.Disabled = !allLoaded
	ws.cleanChannelsItem.Disabled = !allLoaded
	ws.resetDataItem.Disabled = !allLoaded
	ws.exportRGBItem.Disabled = !allLoaded
	ws.sendToEditItem.Disabled = !allLoaded || (ws.largeMode && ws.largeStore != nil && func() bool { _, ok := ws.largeStore.Composite(); return !ok }())

	// if allLoaded {
	// 	alignBtn.Enable()
	// 	crossCleanBtn.Enable()
	// } else {
	// 	alignBtn.Disable()
	// 	crossCleanBtn.Disable()
	// }

	anyOverlayLoaded := false
	for _, l := range ws.overlayLayers {
		if l.win != nil && l.idx < len(ws.imgs) && ws.imgs[l.idx] != nil {
			anyOverlayLoaded = true
			break
		}
	}
	anyChannelLoaded := ws.imgs[0] != nil || ws.imgs[1] != nil || ws.imgs[2] != nil || anyOverlayLoaded
	if anyChannelLoaded {
		ws.largeFilesCheck.Disable()
	} else {
		ws.largeFilesCheck.Enable()
	}
	if ws.largeFilesCheck.Checked {
		ws.blinkCheck.SetChecked(false)
		ws.blinkCheck.Disable()
	} else {
		ws.blinkCheck.Enable()
	}
	ws.saveProjectItem.Disabled = ws.imgs[0] == nil && ws.imgs[1] == nil && ws.imgs[2] == nil && !anyOverlayLoaded
	if m := ws.win.MainMenu(); m != nil {
		m.Refresh()
	}
}
