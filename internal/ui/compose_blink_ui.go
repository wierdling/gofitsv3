package ui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/models"
)

// composeSourceBlinkIDs identifies every loaded source by its stable ID:
// channels by their fixed IDs, layers by their persisted BlinkID.
func (ws *composeWorkspace) composeSourceBlinkIDs() []string {
	ids := make([]string, len(ws.imgs))
	copy(ids, []string{models.ComposeChannel1BlinkID, models.ComposeChannel2BlinkID, models.ComposeChannel3BlinkID})
	for _, l := range ws.overlayLayers {
		if l != nil && l.idx < len(ids) {
			ids[l.idx] = l.settings.BlinkID
		}
	}
	return ids
}

// nil means no generalized selection was persisted; this preserves legacy
// project migration and lets future defaults select all loaded sources.
func (ws *composeWorkspace) composeBlinkSources() []composeBlinkSource {
	overlays := make([]composeBlinkOverlaySource, 0, len(ws.overlayLayers))
	for _, layer := range ws.overlayLayers {
		path := ""
		if layer.idx < len(ws.imgs) && ws.imgs[layer.idx] != nil {
			path = ws.imgs[layer.idx].Path
		}
		overlays = append(overlays, composeBlinkOverlaySource{RuntimeIndex: layer.idx, Name: layer.name, Path: path, BlinkID: layer.settings.BlinkID})
	}
	return enumerateComposeBlinkSources(ws.imgs, overlays)
}

func (ws *composeWorkspace) applyBlinkFrame() {
	if !ws.blinkCheck.Checked {
		return
	}
	ws.blinkMu.Lock()
	frames := append([]composeBlinkFrame(nil), ws.blinkPrepared...)
	ws.blinkMu.Unlock()
	if len(frames) < 2 {
		return
	}
	dst := ws.viewports[3]
	if dst == nil {
		ws.updateBlinkStatus()
		return
	}
	frame := frames[ws.blinkFrame%len(frames)].Preview
	if frame.Image == nil || frame.OrigW == 0 || frame.OrigH == 0 {
		return
	}
	dst.image.Image = frame.Image
	dst.origW, dst.origH = frame.OrigW, frame.OrigH
	dst.bins = frame.Bins
	dst.histMax = frame.HistMax
	if dst.StatsLabel != nil {
		dst.StatsLabel.SetText(fmt.Sprintf("Blink: %s", frames[ws.blinkFrame%len(frames)].Name))
	}
	dst.histogram.Refresh()
	if dst.zoomLabel.Selected == "fit" {
		dst.zoom = dst.fitZoom()
	}
	dst.applyZoom()
	dst.image.Refresh()
}

func (ws *composeWorkspace) updateBlinkStatus() {
	sources := ws.composeBlinkSources()
	selection := ws.blinkChannels
	if selection == nil {
		selection = resolveComposeBlinkSelection(sources, nil, false, 0)
	}
	selection = filterComposeBlinkSelection(selection, sources)
	if !ws.blinkCheck.Checked {
		ws.blinkStatus.SetText("Blink: off")
		return
	}
	if len(selection) < 2 {
		ws.blinkStatus.SetText("Blink: choose at least two channels")
		return
	}
	names := make([]string, 0, len(selection))
	for _, index := range selection {
		for _, source := range sources {
			if source.ProjectIndex == index {
				names = append(names, source.Name)
				break
			}
		}
	}
	ws.blinkStatus.SetText(fmt.Sprintf("Blink: %s", strings.Join(names, " <-> ")))
}

func (ws *composeWorkspace) stopBlink() {
	ws.blinkMu.Lock()
	ws.blinkSeq++
	ws.blinkMu.Unlock()
}

func (ws *composeWorkspace) refreshBlinkFrame() {
	if !ws.blinkCheck.Checked {
		return
	}
	ws.blinkFrame = 0
	ws.updateBlinkStatus()
	ws.applyBlinkFrame()
}

func (ws *composeWorkspace) startBlink() {
	ws.stopBlink()
	if !ws.blinkCheck.Checked {
		ws.updateBlinkStatus()
		return
	}
	if ws.blinkChannels == nil {
		ws.blinkChannels = resolveComposeBlinkSelection(ws.composeBlinkSources(), nil, false, 0)
	}
	if len(filterComposeBlinkSelection(ws.blinkChannels, ws.composeBlinkSources())) < 2 {
		ws.stopBlink()
		ws.updateBlinkStatus()
		return
	}
	// Rebuild the per-channel previews so the blink reflects the current Manual
	// Offsets (applied at render time by renderImages).
	ws.refresh()
	ws.blinkMu.Lock()
	ws.blinkSeq++
	seq := ws.blinkSeq
	ws.blinkMu.Unlock()
	ws.blinkFrame = 0
	ws.updateBlinkStatus()
	ws.applyBlinkFrame()
	go func() {
		ticker := time.NewTicker(700 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			ws.blinkMu.Lock()
			currentSeq := ws.blinkSeq
			ws.blinkMu.Unlock()
			if currentSeq != seq {
				return
			}
			fyne.Do(func() {
				ws.blinkMu.Lock()
				currentSeq := ws.blinkSeq
				ws.blinkMu.Unlock()
				if currentSeq != seq || !ws.blinkCheck.Checked {
					return
				}
				ws.blinkFrame++
				ws.applyBlinkFrame()
			})
		}
	}()
}

func (ws *composeWorkspace) chooseBlinkChannels() {
	sources := ws.composeBlinkSources()
	if len(sources) < 2 {
		dialog.ShowInformation("Blink", "Load at least two channels first.", ws.win)
		return
	}
	current := ws.blinkChannels
	if current == nil {
		current = resolveComposeBlinkSelection(sources, nil, false, 0)
	}
	selected := make(map[int]bool, len(current))
	for _, index := range current {
		selected[index] = true
	}
	checks := make([]*widget.Check, len(sources))
	content := container.NewVBox()
	for i, source := range sources {
		check := widget.NewCheck(source.Name, nil)
		check.SetChecked(selected[source.ProjectIndex])
		checks[i] = check
		content.Add(check)
	}
	d := dialog.NewCustomConfirm("Choose Blink Channels", "Apply", "Cancel", container.NewVScroll(content), func(ok bool) {
		if !ok {
			return
		}
		selection := make([]int, 0, len(sources))
		for i, check := range checks {
			if check.Checked {
				selection = append(selection, sources[i].ProjectIndex)
			}
		}
		if len(selection) < 2 {
			dialog.ShowInformation("Blink", "Select at least two channels.", ws.win)
			return
		}
		ws.blinkChannels = selection
		ws.updateBlinkStatus()
		if ws.blinkCheck.Checked {
			ws.startBlink()
		} else {
			ws.refresh()
		}
	}, ws.win)
	d.Show()
}
