package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

func (ws *composeWorkspace) loadFilterSet() {
	showComposeMagicFolderPicker(ws.app, ws.win, func(preset string, rows []composeMagicRow) error {
		if err := validateComposeMagicCapacity(rows, len(ws.overlayLayers)); err != nil {
			return err
		}

		ctx, cancel := context.WithCancel(context.Background())
		var progressDialog *dialog.CustomDialog
		finished := false
		cancelButton := widget.NewButton("Cancel", func() {
			cancel()
			if progressDialog != nil {
				progressDialog.Hide()
			}
		})
		progressDialog = dialog.NewCustomWithoutButtons(
			"Loading Filter Set",
			container.NewVBox(
				widget.NewLabel("Loading files and applying Magic + Auto MTF..."),
				widget.NewProgressBarInfinite(),
				cancelButton,
			),
			ws.win,
		)
		progressDialog.SetOnClosed(func() {
			if !finished {
				cancel()
			}
		})
		progressDialog.Show()
		spec := composeMagicSpec{Preset: preset, Rows: append([]composeMagicRow(nil), rows...)}
		go func() {
			if ws.largeMode {
				// Stage and process one source at a time. All descriptors remain
				// private until every row succeeds, so cancellation/failure leaves
				// the live project untouched.
				if err := validateComposeMagicPlan(spec.Rows); err != nil {
					fyne.Do(func() { finished = true; progressDialog.Hide(); dialog.ShowError(err, ws.win) })
					return
				}
				magicPresetValue := processing.ParseMagicPreset(spec.Preset)
				type staged struct {
					row      composeMagicRow
					img      *models.LoadedImage
					preview  *image.RGBA
					artifact composeArtifactDescriptor
				}
				ordered := append([]composeMagicRow(nil), spec.Rows...)
				sort.SliceStable(ordered, func(i, j int) bool {
					if ordered[i].File.FilterNumber != ordered[j].File.FilterNumber {
						return ordered[i].File.FilterNumber < ordered[j].File.FilterNumber
					}
					return strings.ToLower(ordered[i].File.Name) < strings.ToLower(ordered[j].File.Name)
				})
				stagedRows := make([]staged, 0, len(ordered))
				cleanup := func() {
					for _, x := range stagedRows {
						ws.cleanupLargeArtifactIfCurrent(x.artifact)
					}
				}
				for i, row := range ordered {
					if ctx.Err() != nil {
						cleanup()
						return
					}
					slot := fmt.Sprintf("filter-stage-%d-%d", time.Now().UnixNano(), i)
					img, preview, artifact, loadErr := loadLargeComposeImage(row.File.Path, ws.largeStore, slot)
					if loadErr != nil {
						cleanup()
						fyne.Do(func() {
							finished = true
							progressDialog.Hide()
							dialog.ShowError(fmt.Errorf("load %s: %w", composeMagicFileLabel(row.File), loadErr), ws.win)
						})
						return
					}
					lease, leaseErr := fitsio.MaterializeFloat32ArtifactLease(artifact.Path)
					if leaseErr != nil {
						ws.cleanupLargeArtifactIfCurrent(artifact)
						cleanup()
						fyne.Do(func() { finished = true; progressDialog.Hide(); dialog.ShowError(leaseErr, ws.win) })
						return
					}
					clone := *img
					clone.HDU.Data.Pixels = lease.Pixels
					magicResult := processing.ApplyMagicLevelsAndMTF(&clone, magicPresetValue)
					if ctx.Err() != nil {
						lease.Release()
						ws.cleanupLargeArtifactIfCurrent(artifact)
						cleanup()
						return
					}
					lease.Release()
					if ctx.Err() != nil {
						ws.cleanupLargeArtifactIfCurrent(artifact)
						cleanup()
						return
					}
					if magicResult.ValidPixels == 0 {
						ws.cleanupLargeArtifactIfCurrent(artifact)
						cleanup()
						fyne.Do(func() {
							finished = true
							progressDialog.Hide()
							dialog.ShowError(fmt.Errorf("process %s: Magic found no valid image samples", composeMagicFileLabel(row.File)), ws.win)
						})
						return
					}
					preview, _, _, loadErr = composeLargeStretchedPreview(artifact.Path, &clone)
					if loadErr != nil {
						ws.cleanupLargeArtifactIfCurrent(artifact)
						cleanup()
						fyne.Do(func() { finished = true; progressDialog.Hide(); dialog.ShowError(loadErr, ws.win) })
						return
					}
					clone.HDU.Data.Pixels = nil
					stagedRows = append(stagedRows, staged{row: row, img: &clone, preview: preview, artifact: artifact})
				}
				if !composeLargeInstallAllowed(ctx) {
					cleanup()
					return
				}
				fyne.Do(func() {
					if !composeLargeInstallAllowed(ctx) {
						cleanup()
						return
					}
					defer func() { finished = true; progressDialog.Hide() }()
					existingSlots := make([]int, len(ws.overlayLayers))
					for i, layer := range ws.overlayLayers {
						existingSlots[i] = layer.idx
					}
					planRows := make([]composeMagicRow, len(stagedRows))
					for i := range stagedRows {
						planRows[i] = stagedRows[i].row
					}
					if installErr := validateComposeMagicCapacity(planRows, len(existingSlots)); installErr != nil {
						cleanup()
						dialog.ShowError(installErr, ws.win)
						return
					}
					base := map[composeMagicAssignment]staged{}
					for _, x := range stagedRows {
						if x.row.Assignment != composeMagicCustom {
							base[x.row.Assignment] = x
						}
					}
					for assignment, index := range map[composeMagicAssignment]int{composeMagicBlue: 0, composeMagicGreen: 1, composeMagicRed: 2} {
						x := base[assignment]
						if old := ws.largeArtifacts[index]; old.Slot != "" {
							_, _ = ws.largeStore.RemoveSlotIfCurrent(old)
						}
						ws.imgs[index] = x.img
						ws.largeArtifacts[index] = x.artifact
						ws.largePreviews[index] = x.preview
						clearComposeOrigPixels(&ws.origPixels, index)
						applyChannelState(index, channelStateFromImage(x.img), ws.imgs, ws.viewports, ws.controlSets)
					}
					for _, x := range stagedRows {
						if x.row.Assignment != composeMagicCustom {
							continue
						}
						settings := defaultOverlayLayerSettings(len(ws.overlayLayers))
						settings.ColorR, settings.ColorG, settings.ColorB = x.row.Color.R, x.row.Color.G, x.row.Color.B
						settings.Open = true
						slot, ok := ws.freeOverlaySlot()
						if !ok {
							cleanup()
							dialog.ShowError(errors.New("no free overlay slot"), ws.win)
							return
						}
						layer := ws.createOverlayLayerAt(slot, settings)
						upsertComposeMixWeight(&ws.mixWeights, composeWeightForColor(settings.BlinkID, color.NRGBA{R: settings.ColorR, G: settings.ColorG, B: settings.ColorB, A: 255}, settings.Opacity))
						ws.imgs[layer.idx] = x.img
						ws.largeArtifacts[layer.idx] = x.artifact
						ws.largePreviews[layer.idx] = x.preview
						clearComposeOrigPixels(&ws.origPixels, layer.idx)
						ws.openOverlayLayerWindowWithPreview(layer, nil)
					}
					ws.composeMagicPreset.SetSelected(spec.Preset)
					ws.buildCompositeCheck.SetChecked(true)
					ws.updateMenus()
					ws.refresh()
				})
				return
			}
			batch, prepareErr := prepareComposeMagicBatch(ctx, spec, loadImageFromPath)
			previews := make(map[*models.LoadedImage]*composeOverlayPreviewData)
			if prepareErr == nil {
				for _, channel := range batch.Channels {
					if channel.Row.Assignment != composeMagicCustom {
						continue
					}
					preview, previewErr := buildComposeOverlayPreviewData(ctx, channel.Image)
					if previewErr != nil {
						prepareErr = previewErr
						break
					}
					previews[channel.Image] = preview
				}
			}
			fyne.Do(func() {
				if prepareErr != nil {
					finished = true
					progressDialog.Hide()
					if !errors.Is(prepareErr, context.Canceled) {
						dialog.ShowError(prepareErr, ws.win)
					}
					return
				}
				if ctx.Err() != nil {
					finished = true
					progressDialog.Hide()
					return
				}

				existingSlots := make([]int, len(ws.overlayLayers))
				for i, layer := range ws.overlayLayers {
					existingSlots[i] = layer.idx
				}
				installPlan, installErr := planComposeMagicInstall(batch, ws.imgs, existingSlots)
				if installErr != nil {
					finished = true
					progressDialog.Hide()
					dialog.ShowError(installErr, ws.win)
					return
				}
				if ctx.Err() != nil { // Last boundary before the atomic install and composite enable.
					finished = true
					progressDialog.Hide()
					return
				}
				ws.withSuspendedRefresh(func() {
					for idx, image := range installPlan.Base {
						replaceComposeChannelImage(ws.imgs, idx, image)
						clearComposeOrigPixels(&ws.origPixels, idx)
						applyChannelState(idx, channelStateFromImage(image), ws.imgs, ws.viewports, ws.controlSets)
						ws.closeHeaderWindow(idx)
					}
					for _, custom := range installPlan.Customs {
						channel := custom.Channel
						settings := defaultOverlayLayerSettings(len(ws.overlayLayers))
						settings.ColorR = channel.Row.Color.R
						settings.ColorG = channel.Row.Color.G
						settings.ColorB = channel.Row.Color.B
						settings.Open = true
						layer := ws.createOverlayLayerAt(custom.Slot, settings)
						upsertComposeMixWeight(&ws.mixWeights, composeWeightForColor(settings.BlinkID, color.NRGBA{R: settings.ColorR, G: settings.ColorG, B: settings.ColorB, A: 255}, settings.Opacity))
						ws.imgs[layer.idx] = channel.Image
						clearComposeOrigPixels(&ws.origPixels, layer.idx)
						ws.openOverlayLayerWindowWithPreview(layer, previews[channel.Image])
						if layer.control != nil && layer.control.MagicPresetSelect != nil {
							layer.control.MagicPresetSelect.SetSelected(batch.Preset)
						}
					}
					ws.renderMu.Lock()
					for i := 0; i < len(ws.renderCache) && i < 3; i++ {
						ws.renderCache[i] = composeRenderCache{}
					}
					ws.renderMu.Unlock()
					ws.composeMagicPreset.SetSelected(batch.Preset)
					ws.buildCompositeCheck.SetChecked(true)
				})
				finished = true
				progressDialog.Hide()
				ws.updateMenus()
				ws.refresh()
			})
		}()
		return nil
	})
}
