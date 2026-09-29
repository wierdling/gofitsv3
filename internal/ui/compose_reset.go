package ui

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/models"
)

func (ws *composeWorkspace) resetData() {
	ws.largeMu.RLock()
	store := ws.largeStore
	ws.largeMu.RUnlock()
	if ws.largeMode && store != nil {
		savedStates := ws.captureViewportStates()
		expected := make([]composeArtifactDescriptor, 0, 3)
		selected := make([]*models.LoadedImage, 0, 3)
		identities := make([]*models.LoadedImage, 0, 3)
		indices := make([]int, 0, 3)
		requests := make(map[int]uint64)
		var session uint64
		ws.largeMu.RLock()
		session = ws.largeSessionGeneration
		store = ws.largeStore
		if store == nil {
			ws.largeMu.RUnlock()
			dialog.ShowInformation("Reset", "The large-file Compose session is no longer available.", ws.win)
			return
		}
		for i := 0; i < 3; i++ {
			if ws.imgs[i] == nil {
				continue
			}
			d, ok := ws.largeArtifacts[i]
			if !ok {
				ws.largeMu.RUnlock()
				dialog.ShowError(fmt.Errorf("missing disk artifact for Channel %d", i+1), ws.win)
				return
			}
			expected = append(expected, d)
			identities = append(identities, ws.imgs[i])
			// Copy the complete small channel state while holding the same lock
			// used by large-mode writers. Reset staging must never read a live
			// LoadedImage while a channel job is committing metadata.
			snapshot := *ws.imgs[i]
			snapshot.HDU = ws.imgs[i].HDU
			snapshot.HDU.Header.Cards = make(map[string]string, len(ws.imgs[i].HDU.Header.Cards))
			for key, value := range ws.imgs[i].HDU.Header.Cards {
				snapshot.HDU.Header.Cards[key] = value
			}
			snapshot.HDU.Data.Pixels = nil
			snapshot.HDU.Data.Int32Pixels = nil
			selected = append(selected, &snapshot)
			indices = append(indices, i)
			slot := fmt.Sprintf("channel-%d", i)
			requests[i] = ws.largeLoadGenerations[slot]
		}
		ws.largeMu.RUnlock()
		if len(selected) == 0 {
			dialog.ShowInformation("Reset", "No loaded channels to reset.", ws.win)
			return
		}
		progressDialog := dialog.NewCustom("Reset", "Restoring channels from disk…", widget.NewProgressBarInfinite(), ws.win)
		progressDialog.Show()
		go func() {
			// Never hold largeMu across FITS I/O. Clear Channels and other UI
			// invalidation paths must be able to advance the generation while
			// this reset is staging its transactional replacements.
			resetStillCurrent := func() bool {
				ws.largeMu.RLock()
				defer ws.largeMu.RUnlock()
				if ws.largeStore != store || ws.largeSessionGeneration != session {
					return false
				}
				for j, idx := range indices {
					slot := fmt.Sprintf("channel-%d", idx)
					if ws.imgs[idx] != identities[j] || ws.largeLoadGenerations[slot] != requests[idx] {
						return false
					}
					current, ok := ws.largeArtifacts[idx]
					if !ok || current.Generation != expected[j].Generation || current.Path != expected[j].Path {
						return false
					}
					stored, ok := store.Descriptor(slot)
					if !ok || stored.Generation != expected[j].Generation || stored.Path != expected[j].Path {
						return false
					}
				}
				return true
			}
			if !resetStillCurrent() {
				fyne.Do(func() {
					progressDialog.Hide()
					dialog.ShowInformation("Reset", "Channels changed before reset started.", ws.win)
				})
				return
			}
			descs, previews, hdus, primaries, err := stageComposeLargeReset(context.Background(), store, selected, expected)
			if err != nil {
				fyne.Do(func() {
					progressDialog.Hide()
					dialog.ShowError(err, ws.win)
				})
				return
			}
			fyne.Do(func() {
				progressDialog.Hide()
				// Revalidate image identity and artifact generations under the
				// short publish lock immediately before mutating live state.
				ws.largeMu.Lock()
				publishCurrent := ws.largeStore == store && ws.largeSessionGeneration == session
				for j, idx := range indices {
					slot := fmt.Sprintf("channel-%d", idx)
					current, ok := ws.largeArtifacts[idx]
					stored, storedOK := store.Descriptor(slot)
					if !publishCurrent || ws.imgs[idx] != identities[j] || ws.largeLoadGenerations[slot] != requests[idx] || !ok || current.Generation != expected[j].Generation || current.Path != expected[j].Path || !storedOK || stored.Generation != descs[j].Generation || stored.Path != descs[j].Path {
						publishCurrent = false
						break
					}
				}
				if !publishCurrent {
					ws.largeMu.Unlock()
					for _, d := range descs {
						_, _ = store.RemoveSlotIfCurrent(d)
					}
					dialog.ShowInformation("Reset", "Channels changed while reset was running.", ws.win)
					return
				}
				for j, idx := range indices {
					ws.imgs[idx].HDU, ws.imgs[idx].Primary = hdus[j], primaries[j]
					ws.imgs[idx].HDU.Data.Width, ws.imgs[idx].HDU.Data.Height = descs[j].Width, descs[j].Height
					ws.imgs[idx].HDU.Data.Pixels = nil
					ws.largeArtifacts[idx], ws.largePreviews[idx] = descs[j], previews[j]
				}
				ws.largeMu.Unlock()
				ws.refresh()
				ws.restoreViewportStates(savedStates)
				dialog.ShowInformation("Reset Complete", "Channels restored from disk.", ws.win)
			})
		}()
		return
	}
	loaded := false
	errors := make([]string, 0, 3)

	savedStates := ws.captureViewportStates()

	ws.withSuspendedRefresh(func() {
		for i := 0; i < 3; i++ {
			if ws.imgs[i] == nil {
				continue
			}
			loaded = true
			state := channelStateFromImage(ws.imgs[i])
			reloaded, err := loadImageFromPath(ws.imgs[i].Path)
			if err != nil {
				errors = append(errors, fmt.Sprintf("Channel %d: %v", i+1, err))
				continue
			}
			ws.imgs[i] = reloaded
			clearComposeOrigPixels(&ws.origPixels, i)
			restoreComposeChannelRotation(reloaded, state.Rotation90)
			applyChannelState(i, state, ws.imgs, ws.viewports, ws.controlSets)
		}
	})

	if !loaded {
		dialog.ShowInformation("Reset", "No loaded channels to reset.", ws.win)
		return
	}

	ws.refresh()
	ws.restoreViewportStates(savedStates)
	if len(errors) > 0 {
		dialog.ShowError(fmt.Errorf("%s", strings.Join(errors, "\n")), ws.win)
		return
	}
	dialog.ShowInformation("Reset Complete", "Channels restored from disk.", ws.win)
}

func (ws *composeWorkspace) clearChannels() {
	dialog.ShowConfirm("Clear Channels", "Free all three channel images from memory?", func(ok bool) {
		if !ok {
			return
		}
		oldSources := ws.composeBlinkSources()
		for i := 0; i < 3; i++ {
			if ws.largeMode && ws.largeStore != nil {
				ws.invalidateLargeSlot(i)
				ws.largeMu.Lock()
				store := ws.largeStore
				d := ws.largeArtifacts[i]
				delete(ws.largeArtifacts, i)
				delete(ws.largePreviews, i)
				ws.imgs[i] = nil
				ws.largeMu.Unlock()
				if d.Slot != "" && store != nil {
					_, _ = store.RemoveSlotIfCurrent(d)
				}
			} else {
				ws.imgs[i] = nil
			}
			clearComposeOrigPixels(&ws.origPixels, i)
			ws.viewports[i].image.Image = blankImg()
		}
		if ws.blinkChannels != nil {
			ws.blinkChannels = remapComposeBlinkSelection(ws.blinkChannels, oldSources, ws.composeBlinkSources())
		}
		ws.viewports[3].image.Image = blankImg()
		ws.refresh()
		ws.updateMenus()
		go func() {
			runtime.GC()
			debug.FreeOSMemory()
		}()
	}, ws.win)
}

func (ws *composeWorkspace) resetCompose() {
	dialog.ShowConfirm("Reset Compose", "Clear all images and reset Build color composite?", func(ok bool) {
		if !ok {
			return
		}
		for _, l := range ws.overlayLayers {
			if l.win != nil {
				l.win.SetCloseIntercept(nil)
				l.win.Close()
			}
		}
		ws.overlayLayers = nil
		ws.blinkChannels = nil
		ws.stopBlink()
		ws.blinkCheck.SetChecked(false)
		for i := range ws.imgs {
			if ws.largeMode && ws.largeStore != nil {
				ws.invalidateLargeSlot(i)
				ws.largeMu.Lock()
				store := ws.largeStore
				d := ws.largeArtifacts[i]
				delete(ws.largeArtifacts, i)
				delete(ws.largePreviews, i)
				ws.imgs[i] = nil
				ws.largeMu.Unlock()
				if d.Slot != "" && store != nil {
					_, _ = store.RemoveSlotIfCurrent(d)
				}
			} else {
				ws.imgs[i] = nil
			}
			clearComposeOrigPixels(&ws.origPixels, i)
		}
		ws.imgs = ws.imgs[:3]
		ws.origPixels = ws.origPixels[:3]
		for i := range ws.viewports {
			ws.viewports[i].image.Image = blankImg()
		}
		ws.buildCompositeCheck.SetChecked(false)
		ws.refresh()
		ws.updateMenus()
		go func() {
			runtime.GC()
			debug.FreeOSMemory()
		}()
	}, ws.win)
}
