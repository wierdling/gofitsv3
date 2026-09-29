package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

func (ws *composeWorkspace) applyDedicatedL(b []byte, w, h int, settings models.LRGBSettings, dedicated *models.LoadedImage) ([]byte, error) {
	if !settings.Enabled || dedicated == nil || settings.LuminanceWeight <= 0 {
		return b, nil
	}
	ld, _ := processing.ApplyStretchParallel(dedicated)
	return processing.ApplyDedicatedLToRGBA(b, ld.Pixels, ld.Width, ld.Height, w, h, processing.LRGBConfig{
		LuminanceWeight:      settings.LuminanceWeight,
		ChrominanceSmoothing: settings.ChrominanceSmoothing,
		SyntheticWeights:     settings.SyntheticWeights,
	})
}

func (ws *composeWorkspace) loadChannel(idx int) {
	showSingleFITSOpenDialog(ws.app, ws.win, func(path string) {

		progressDialog := dialog.NewCustom(
			fmt.Sprintf("Loading Channel %d", idx+1),
			"Reading FITS data...",
			widget.NewProgressBarInfinite(),
			ws.win,
		)
		progressDialog.Show()

		diskLoad := ws.largeMode
		go func() {
			slot := fmt.Sprintf("channel-%d", idx)
			request, session, store := ws.nextLargeLoadGeneration(slot)
			var img *models.LoadedImage
			var preview *image.RGBA
			var artifact composeArtifactDescriptor
			var loadErr error
			if diskLoad {
				if ws.largeStore == nil {
					loadErr = errors.New("disk-backed Compose store is unavailable")
				} else {
					if store == nil {
						loadErr = errors.New("disk-backed Compose store is unavailable")
					} else {
						img, preview, artifact, loadErr = loadLargeComposeImage(path, store, slot)
					}
				}
			} else {
				img, loadErr = loadImageFromPath(path)
			}

			if loadErr != nil {
				progressDialog.Hide()
				dialog.ShowError(loadErr, ws.win)
				return
			}

			if diskLoad && !ws.largeLoadStillCurrent(slot, request, session, artifact) {
				ws.cleanupLargeArtifactIfCurrent(artifact)
				return
			}
			if !diskLoad {
				replaceComposeChannelImage(ws.imgs, idx, img)
			}
			if diskLoad {
				ws.largeMu.Lock()
				current, currentOK := store.Descriptor(slot)
				if ws.largeStore != store || ws.largeSessionGeneration != session || ws.largeLoadGenerations[slot] != request || !currentOK || current.Generation != artifact.Generation || current.Path != artifact.Path {
					ws.largeMu.Unlock()
					ws.cleanupLargeArtifactIfCurrent(artifact)
					return
				}
				replaceComposeChannelImage(ws.imgs, idx, img)
				ws.largePreviews[idx] = preview
				ws.largeArtifacts[idx] = artifact
				ws.largeMu.Unlock()
			}
			clearComposeOrigPixels(&ws.origPixels, idx)

			fyne.Do(func() {
				if ws.controlSets != nil {
					applyChannelState(idx, channelStateFromImage(img), ws.imgs, ws.viewports, ws.controlSets)
				}
				progressDialog.Hide()
				ws.refresh()
				ws.closeHeaderWindow(idx)
				ws.updateMenus()
			})
		}()

	})
}

// Dedicated L is intentionally an in-memory Compose input for now. Keeping
// it separate from the three color slots avoids treating it as an overlay
// while still giving it the same persisted ChannelState metadata.
func (ws *composeWorkspace) loadDedicatedL() {
	if ws.largeMode {
		dialog.ShowInformation("Dedicated L unavailable", "Dedicated luminance loading is not available in disk-backed Compose yet.", ws.win)
		return
	}
	fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil || r == nil {
			return
		}
		path := r.URI().Path()
		ws.app.Preferences().SetString("lastDir", filepath.Dir(path))
		ws.lrgbMu.Lock()
		ws.lrgbGeneration++
		requestGeneration := ws.lrgbGeneration
		ws.lrgbMu.Unlock()
		progressDialog := dialog.NewCustom("Loading Dedicated L", "Reading FITS data...", widget.NewProgressBarInfinite(), ws.win)
		progressDialog.Show()
		go func() {
			img, loadErr := loadImageFromPath(path)
			if loadErr == nil && img.HDU.Data.Width <= 0 || loadErr == nil && img.HDU.Data.Height <= 0 {
				loadErr = errors.New("dedicated luminance image has invalid dimensions")
			}
			if loadErr != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(loadErr, ws.win) })
				return
			}
			fyne.Do(func() {
				ws.lrgbMu.Lock()
				if ws.lrgbGeneration != requestGeneration {
					ws.lrgbMu.Unlock()
					progressDialog.Hide()
					return
				}
				ws.dedicatedL = img
				ws.lrgbSettings.DedicatedLPath = path
				ws.lrgbSettings.DedicatedLState = channelStateFromImage(img)
				ws.lrgbGeneration++
				ws.lrgbMu.Unlock()
				if ws.controlSets != nil {
					ws.refresh()
				}
				progressDialog.Hide()
				ws.updateMenus()
				ws.refresh()
			})
		}()
	}, ws.win)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
	if last := ws.app.Preferences().String("lastDir"); last != "" {
		if l, err := storage.ListerForURI(storage.NewFileURI(last)); err == nil {
			fd.SetLocation(l)
		}
	}
	fd.SetView(dialog.ListView)
	sizeFileDialog(fd)
	fd.Show()
}

func (ws *composeWorkspace) clearDedicatedL() {
	ws.lrgbMu.Lock()
	ws.dedicatedL = nil
	ws.lrgbSettings.DedicatedLPath = ""
	ws.lrgbSettings.DedicatedLState = models.ChannelState{}
	ws.lrgbGeneration++
	ws.lrgbMu.Unlock()
	ws.refresh()
	ws.updateMenus()
}

func (ws *composeWorkspace) compositeImageForEdit() *image.RGBA {
	// Always render from the save-gated snapshot. The cached composite
	// viewport may have been produced while a temporary Before/After
	// comparison override was active and must never leak into Export to Edit.
	buf, w, h, _, err := ws.composeRGB(context.Background())
	if err != nil || buf == nil {
		return nil
	}
	buf = processing.ApplyRGBLevels(buf, ws.levels)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	copy(img.Pix, buf)
	return img
}

func (ws *composeWorkspace) startLargeEditSnapshot(d composeCompositeDescriptor, levelSnapshot models.RgbLevels) {
	// Capture the session owner before starting any asynchronous work. The
	// Compose session may be cleared or replaced while the copy is running;
	// the job must continue to use the immutable store pointer it started
	// with, rather than dereferencing the mutable outer largeStore variable.
	store := ws.largeStore
	if store == nil {
		dialog.ShowInformation("Nothing to send", "The large-file Compose session is no longer available.", ws.win)
		return
	}
	ws.editSnapshotMu.Lock()
	if ws.editSnapshotCancel != nil {
		ws.editSnapshotMu.Unlock()
		dialog.ShowInformation("Send to Edit", "A composite snapshot is already in progress.", ws.win)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ws.editSnapshotCancel = cancel
	ws.editSnapshotMu.Unlock()
	cancelButton := widget.NewButton("Cancel", cancel)
	progressDialog := dialog.NewCustomWithoutButtons("Sending to Edit", container.NewBorder(nil, cancelButton, nil, nil, container.NewVBox(widget.NewLabel("Copying the published composite…"), widget.NewProgressBarInfinite())), ws.win)
	finished := false
	progressDialog.SetOnClosed(func() {
		if !finished {
			cancel()
		}
	})
	progressDialog.Show()
	go func() {
		ed, err := store.SnapshotCompositeForEdit(ctx, d)
		if err == nil {
			ed.levels = levelSnapshot
		}
		fyne.Do(func() {
			finished = true
			progressDialog.Hide()
			ws.editSnapshotMu.Lock()
			ws.editSnapshotCancel = nil
			ws.editSnapshotMu.Unlock()
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					dialog.ShowInformation("Nothing to send", err.Error(), ws.win)
				}
				return
			}
			if cur, ok := store.Composite(); !ok || cur.Generation != d.Generation {
				ed.cleanup()
				dialog.ShowInformation("Nothing to send", "The composite changed before it could be sent to Edit.", ws.win)
				return
			}
			if globalExportToEdit != nil {
				if installErr := globalExportToEdit(editImageHandoff{disk: ed}); installErr != nil {
					ed.cleanup()
					dialog.ShowError(installErr, ws.win)
				}
			}
		})
	}()
}

func (ws *composeWorkspace) sendToEdit() {
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
		dialog.ShowInformation("Nothing to send", "Compose all three channels first.", ws.win)
		return
	}
	if err := globalExportToEdit(editImageHandoff{memory: img}); err != nil {
		dialog.ShowError(err, ws.win)
	}
}

// Register package-level callback so the preview window can inject an image
// into any channel with its current stretch settings.
func (ws *composeWorkspace) sendToChannel(channelIdx int, img *models.LoadedImage) {
	if channelIdx < 0 || channelIdx >= 3 {
		return
	}
	if composeLargeModeActive != nil && composeLargeModeActive() {
		if img == nil || img.HDU.Data.Width <= 0 || img.HDU.Data.Height <= 0 || len(img.HDU.Data.Pixels) < img.HDU.Data.Width*img.HDU.Data.Height || ws.largeStore == nil {
			debuglog.Log("Compose large mode: Send to Channel requires a materialized incoming image")
			return
		}
		incoming := *img
		pixels := img.HDU.Data.Pixels
		w, h := img.HDU.Data.Width, img.HDU.Data.Height
		slot := fmt.Sprintf("channel-%d", channelIdx)
		request, session, store := ws.nextLargeLoadGeneration(slot)
		stageSlot := fmt.Sprintf("send-channel-%d-%d", channelIdx, time.Now().UnixNano())
		ws.largeMu.RLock()
		oldDescriptor := ws.largeArtifacts[channelIdx]
		ws.largeMu.RUnlock()
		go func() {
			if store == nil {
				fyne.Do(func() { dialog.ShowError(fmt.Errorf("disk-backed Compose store is unavailable"), ws.win) })
				return
			}
			d, err := store.Replace(stageSlot, w, h, func(out *fitsio.Float32Artifact) error {
				row := make([]float32, w)
				for y := 0; y < h; y++ {
					copy(row, pixels[y*w:(y+1)*w])
					if err := out.WriteRow(y, row); err != nil {
						return err
					}
				}
				return nil
			})
			if err == nil {
				incoming.HDU.Data.Pixels = nil
				preview, _, _, pErr := composeLargeStretchedPreview(d.Path, &incoming)
				err = pErr
				if err != nil {
					_, _ = store.RemoveSlotIfCurrent(d)
				} else {
					ws.largeMu.Lock()
					currentSession := ws.largeSessionGeneration == session && ws.largeLoadGenerations[slot] == request && ws.largeStore == store
					if !currentSession {
						ws.largeMu.Unlock()
						_, _ = store.RemoveSlotIfCurrent(d)
						err = errors.New("stale Compose channel generation")
						return
					}
					ws.largeArtifacts[channelIdx] = d
					ws.largePreviews[channelIdx] = preview
					ws.largeMu.Unlock()
					if oldDescriptor.Slot != "" {
						_, _ = store.RemoveSlotIfCurrent(oldDescriptor)
					}
				}
			}
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, ws.win)
					return
				}
				ws.largeMu.Lock()
				current, currentOK := ws.largeArtifacts[channelIdx]
				currentSession := ws.largeStore == store && composeLargeSendCommitAllowed(session, ws.largeSessionGeneration, request, ws.largeLoadGenerations[slot], currentOK, current, d)
				ws.largeMu.Unlock()
				if !currentSession {
					_, _ = store.RemoveSlotIfCurrent(d)
					return
				}
				ws.largeMu.Lock()
				replaceComposeChannelImage(ws.imgs, channelIdx, &incoming)
				ws.largeMu.Unlock()
				clearComposeOrigPixels(&ws.origPixels, channelIdx)
				applyChannelState(channelIdx, channelStateFromImage(&incoming), ws.imgs, ws.viewports, ws.controlSets)
				ws.refresh()
				ws.updateMenus()
			})
		}()
		return
	}
	replaceComposeChannelImage(ws.imgs, channelIdx, img)
	clearComposeOrigPixels(&ws.origPixels, channelIdx)
	applyChannelState(channelIdx, channelStateFromImage(img), ws.imgs, ws.viewports, ws.controlSets)
	ws.refresh()
	ws.updateMenus()
}
