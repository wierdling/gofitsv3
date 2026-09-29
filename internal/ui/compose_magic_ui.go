package ui

import (
	"context"
	"errors"
	"fmt"
	"image"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

func (ws *composeWorkspace) runMagicAll() {
	if composeLargeModeActive != nil && composeLargeModeActive() {
		preset := processing.ParseMagicPreset(ws.composeMagicPreset.Selected)
		type target struct {
			idx      int
			identity *models.LoadedImage
			image    models.LoadedImage
			d        composeArtifactDescriptor
		}
		ws.largeRuntime.mu.RLock()
		targets := make([]target, 0, len(ws.imgs))
		for i, img := range ws.imgs {
			if img == nil {
				continue
			}
			d, ok := ws.largeRuntime.artifacts[i]
			if !ok {
				continue
			}
			targets = append(targets, target{idx: i, identity: img, image: snapshotLargeLoadedImage(img), d: d})
		}
		ws.largeRuntime.mu.RUnlock()
		ctx, cancel := context.WithCancel(context.Background())
		finished := false
		var progressDialog *dialog.CustomDialog
		progressLabel := widget.NewLabel("Applying Magic + Auto MTF to loaded channels…")
		cancelButton := widget.NewButton("Cancel", func() {
			cancel()
			if progressDialog != nil {
				progressDialog.Hide()
			}
		})
		progressDialog = dialog.NewCustomWithoutButtons("Magic", container.NewVBox(
			progressLabel,
			widget.NewProgressBarInfinite(), cancelButton,
		), ws.win)
		progressDialog.SetOnClosed(func() {
			if !finished {
				cancel()
			}
		})
		progressDialog.Show()
		ws.magicAll.Disable()
		go func() {
			ws.largeRuntime.jobMu.Lock()
			defer ws.largeRuntime.jobMu.Unlock()
			type result struct {
				idx      int
				identity *models.LoadedImage
				img      models.LoadedImage
				preview  *image.RGBA
				d        composeArtifactDescriptor
			}
			results := make([]result, 0, len(targets))
			for _, target := range targets {
				if err := composeMagicCanceled(ctx); err != nil {
					fyne.Do(func() {
						finished = true
						progressDialog.Hide()
						ws.magicAll.Enable()
					})
					return
				}
				d := target.d
				lease, err := fitsio.MaterializeFloat32ArtifactLease(d.Path)
				if err != nil {
					fyne.Do(func() {
						finished = true
						progressDialog.Hide()
						ws.magicAll.Enable()
						dialog.ShowError(fmt.Errorf("prepare channel %d for Magic: %w", target.idx+1, err), ws.win)
					})
					return
				}
				clone := target.image
				clone.HDU.Data.Pixels = lease.Pixels
				processing.ApplyMagicLevelsAndMTF(&clone, preset)
				lease.Release()
				if err := composeMagicCanceled(ctx); err != nil {
					fyne.Do(func() {
						finished = true
						progressDialog.Hide()
						ws.magicAll.Enable()
					})
					return
				}
				preview, _, _, err := composeLargeStretchedPreview(d.Path, &clone)
				if err != nil {
					fyne.Do(func() {
						finished = true
						progressDialog.Hide()
						ws.magicAll.Enable()
						dialog.ShowError(fmt.Errorf("build channel %d Magic preview: %w", target.idx+1, err), ws.win)
					})
					return
				}
				results = append(results, result{target.idx, target.identity, clone, preview, d})
			}
			fyne.Do(func() {
				defer cancel()
				if ctx.Err() != nil {
					finished = true
					progressDialog.Hide()
					ws.magicAll.Enable()
					return
				}
				ws.largeRuntime.mu.Lock()
				for _, r := range results {
					cur, ok := ws.largeRuntime.artifacts[r.idx]
					var storeCur composeArtifactDescriptor
					storeOK := ws.largeRuntime.store != nil
					if storeOK {
						storeCur, storeOK = ws.largeRuntime.store.Descriptor(r.d.Slot)
					}
					identityOK := r.idx < len(ws.imgs) && ws.imgs[r.idx] == r.identity
					if !ok || !storeOK || !identityOK || cur.Generation != r.d.Generation || cur.Path != r.d.Path || storeCur.Generation != r.d.Generation || storeCur.Path != r.d.Path {
						ws.largeRuntime.mu.Unlock()
						finished = true
						progressDialog.Hide()
						ws.magicAll.Enable()
						return
					}
				}
				type syncItem struct {
					fn  func(*models.LoadedImage)
					img *models.LoadedImage
				}
				syncFns := make([]syncItem, 0, len(results))
				for _, r := range results {
					r.img.HDU.Data.Pixels = nil
					ws.imgs[r.idx] = &r.img
					ws.largeRuntime.previews[r.idx] = r.preview
					if syncFn := ws.largeRuntime.syncWidgets[r.idx]; syncFn != nil {
						syncFns = append(syncFns, syncItem{fn: syncFn, img: ws.imgs[r.idx]})
					}
				}
				ws.largeRuntime.mu.Unlock()
				for _, item := range syncFns {
					item.fn(item.img)
				}
				for _, r := range results {
					if r.idx < len(ws.controlSets) {
						continue
					}
					for _, layer := range ws.overlayLayers {
						if layer != nil && layer.idx == r.idx && layer.viewport != nil {
							layer.viewport.image.Image = r.preview
							layer.viewport.image.Refresh()
							break
						}
					}
				}
				ws.refresh()
				finished = true
				progressDialog.Hide()
				ws.magicAll.Enable()
			})
		}()
		return
	}
	preset := processing.ParseMagicPreset(ws.composeMagicPreset.Selected)
	// Snapshot loaded channels; processing runs off the UI thread.
	type magicTarget struct {
		idx      int
		img      *models.LoadedImage
		before   magicStretchSnapshot
		prepared models.LoadedImage
	}
	targets := make([]magicTarget, 0, len(ws.imgs))
	for i, img := range ws.imgs {
		if img != nil {
			targets = append(targets, magicTarget{idx: i, img: img, before: snapshotMagicStretch(img), prepared: *img})
		}
	}
	if len(targets) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := false
	var progressDialog *dialog.CustomDialog
	progressLabel := widget.NewLabel("Applying Magic + Auto MTF to loaded channels…")
	cancelButton := widget.NewButton("Cancel", func() {
		cancel()
		if progressDialog != nil {
			progressDialog.Hide()
		}
	})
	progressDialog = dialog.NewCustomWithoutButtons("Magic", container.NewVBox(
		progressLabel,
		widget.NewProgressBarInfinite(), cancelButton,
	), ws.win)
	progressDialog.SetOnClosed(func() {
		if !finished {
			cancel()
		}
	})
	progressDialog.Show()
	ws.magicAll.Disable()
	go func() {
		prepared := make([]composeGlobalMagicTarget, len(targets))
		for i, target := range targets {
			prepared[i] = composeGlobalMagicTarget{index: target.idx, image: target.prepared, independent: target.idx >= len(ws.controlSets)}
		}
		results, prepareErr := prepareComposeGlobalMagic(ctx, prepared, preset)
		if prepareErr != nil {
			if errors.Is(prepareErr, context.Canceled) {
				fyne.Do(func() {
					finished = true
					if progressDialog != nil {
						progressDialog.Hide()
					}
					ws.magicAll.Enable()
				})
				return
			}
			fyne.Do(func() {
				finished = true
				if progressDialog != nil {
					progressDialog.Hide()
				}
				ws.magicAll.Enable()
				dialog.ShowError(fmt.Errorf("prepare independent channel preview: %w", prepareErr), ws.win)
			})
			return
		}
		fyne.Do(func() {
			defer cancel()
			if ctx.Err() != nil {
				finished = true
				if progressDialog != nil {
					progressDialog.Hide()
				}
				ws.magicAll.Enable()
				return
			}
			for _, target := range targets {
				if target.idx >= len(ws.imgs) || ws.imgs[target.idx] != target.img || !magicStretchUnchanged(target.img, target.before) {
					finished = true
					if progressDialog != nil {
						progressDialog.Hide()
					}
					ws.magicAll.Enable()
					return
				}
			}
			ws.withSuspendedRefresh(func() {
				for j, target := range targets {
					result := results[j]
					installMagicStretch(target.img, result.image)
					if target.idx < len(ws.controlSets) {
						applyChannelState(target.idx, channelStateFromImage(target.img), ws.imgs, ws.viewports, ws.controlSets)
						continue
					}
					for _, layer := range ws.overlayLayers {
						if layer != nil && layer.idx == target.idx && layer.control != nil && layer.viewport != nil {
							applyChannelState(target.idx, channelStateFromImage(target.img), ws.imgs, ws.layerViews(layer), ws.layerControls(layer))
							if result.preview != nil {
								ws.applyLayerPreview(layer, result.preview)
							}
							break
						}
					}
				}
			})
			finished = true
			progressLabel.SetText("Refreshing previews…")
			cancelButton.Disable()
			ws.refreshAsync(func() {
				if progressDialog != nil {
					progressDialog.Hide()
				}
				ws.magicAll.Enable()
			})
		})
	}()
}
