package ui

import (
	"fmt"
	"image"
	"maps"
	"math"
	"strings"

	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/utils"
)

func (ws *composeWorkspace) normalizeScale() {
	if ws.imgs[0] == nil || ws.imgs[1] == nil || ws.imgs[2] == nil {
		dialog.ShowInformation("Missing Channels", "Load all three FITS channels before scaling.", ws.win)
		return
	}
	if ws.largeMode && ws.largeStore != nil {
		linesG := utils.FormatHeadersLines(ws.imgs[1].Primary, ws.imgs[1].HDU.Header)
		targetScale := processing.GetPixelScale(linesG)
		// Capture everything the worker reads on the UI goroutine.
		store := ws.largeStore
		srcImgs := map[int]*models.LoadedImage{0: ws.imgs[0], 2: ws.imgs[2]}
		ws.largeMu.RLock()
		descs := maps.Clone(ws.largeArtifacts)
		ws.largeMu.RUnlock()
		job := startUIJob(&ws.modalJobs, "Normalizing", "Resampling arrays to match Channel 2 scale...", ws.win)
		go func() {
			type result struct {
				idx, width, height int
				d                  composeArtifactDescriptor
				preview            *image.RGBA
				err                error
			}
			results := make([]result, 0, 2)
			for _, idx := range []int{0, 2} {
				if job.ctx.Err() != nil {
					break
				}
				d, ok := descs[idx]
				if !ok {
					results = append(results, result{idx: idx, err: fmt.Errorf("missing disk artifact for channel %d", idx+1)})
					continue
				}
				img := *srcImgs[idx]
				lines := utils.FormatHeadersLines(img.Primary, img.HDU.Header)
				sourceScale := processing.GetPixelScale(lines)
				if targetScale == 0 || math.Abs(sourceScale-targetScale)/targetScale < 0.01 {
					results = append(results, result{idx: idx, width: d.Width, height: d.Height, d: d})
					continue
				}
				ratio := sourceScale / targetScale
				newW, newH := int(float64(d.Width)*ratio), int(float64(d.Height)*ratio)
				nd, err := store.ResizeArtifact(d, newW, newH)
				var preview *image.RGBA
				if err == nil {
					preview, _, _, err = composeLargeStretchedPreview(nd.Path, &img)
				}
				results = append(results, result{idx: idx, width: newW, height: newH, d: nd, preview: preview, err: err})
			}
			job.finish(func() {
				if ws.largeStore != store {
					return
				}
				count := 0
				for _, r := range results {
					if r.err != nil {
						dialog.ShowError(r.err, ws.win)
						continue
					}
					if r.width == 0 || ws.imgs[r.idx] != srcImgs[r.idx] {
						continue
					}
					ws.largeMu.Lock()
					ws.largeArtifacts[r.idx] = r.d
					if r.preview != nil {
						ws.largePreviews[r.idx] = r.preview
					}
					ws.largeMu.Unlock()
					ws.imgs[r.idx].HDU.Data.Width, ws.imgs[r.idx].HDU.Data.Height = r.width, r.height
					ws.imgs[r.idx].HDU.Data.Pixels = nil
					clearComposeOrigPixels(&ws.origPixels, r.idx)
					count++
				}
				ws.refresh()
				dialog.ShowInformation("Complete", fmt.Sprintf("Rescaled %d channel(s) to match Channel 2 pixel scale.", count), ws.win)
			}, nil)
		}()
		return
	}

	// Calculate the absolute physical scale of the reference channel
	linesG := utils.FormatHeadersLines(ws.imgs[1].Primary, ws.imgs[1].HDU.Header)
	targetScale := processing.GetPixelScale(linesG)

	// The worker reads detached copies and returns resized data; the images are
	// only updated on the UI goroutine, and only if the slot was not replaced.
	srcImgs := [3]*models.LoadedImage{ws.imgs[0], ws.imgs[1], ws.imgs[2]}
	srcCopies := [3]models.LoadedImage{*ws.imgs[0], *ws.imgs[1], *ws.imgs[2]}
	job := startUIJob(&ws.modalJobs, "Normalizing", "Resampling arrays to match Channel 2 scale...", ws.win)

	go func() {
		type resized struct {
			idx  int
			data fitsio.ImageData
		}
		results := make([]resized, 0, 2)
		for i := 0; i < 3; i++ {
			if i == 1 {
				continue // Channel 2 is the reference
			}
			if job.ctx.Err() != nil {
				break
			}
			img := srcCopies[i]

			lines := utils.FormatHeadersLines(img.Primary, img.HDU.Header)
			sourceScale := processing.GetPixelScale(lines)

			// Skip if scales match within a 1% margin of error to prevent destructive sub-pixel resampling
			if math.Abs(sourceScale-targetScale)/targetScale < 0.01 {
				continue
			}

			ratio := sourceScale / targetScale
			newW := int(float64(img.HDU.Data.Width) * ratio)
			newH := int(float64(img.HDU.Data.Height) * ratio)

			pixels := processing.ResizeChannel(img.HDU.Data.Pixels, img.HDU.Data.Width, img.HDU.Data.Height, newW, newH)
			results = append(results, resized{idx: i, data: fitsio.ImageData{Width: newW, Height: newH, Pixels: pixels}})
		}

		job.finish(func() {
			resizedCount := 0
			for _, r := range results {
				if ws.imgs[r.idx] != srcImgs[r.idx] {
					continue
				}
				ws.imgs[r.idx].HDU.Data.Pixels = r.data.Pixels
				ws.imgs[r.idx].HDU.Data.Width = r.data.Width
				ws.imgs[r.idx].HDU.Data.Height = r.data.Height
				clearComposeOrigPixels(&ws.origPixels, r.idx)
				resizedCount++
			}
			ws.refresh()
			dialog.ShowInformation("Complete", fmt.Sprintf("Rescaled %d channel(s) to match Channel 2 pixel scale.", resizedCount), ws.win)
		}, nil)
	}()
}

func (ws *composeWorkspace) copySettings() {
	if ws.imgs[0] == nil {
		dialog.ShowInformation("Missing", "Load Channel 1 first", ws.win)
		return
	}
	missing := make([]string, 0, 2)
	for _, idx := range []int{1, 2} {
		if ws.imgs[idx] == nil {
			missing = append(missing, fmt.Sprintf("Channel %d", idx+1))
		}
	}
	if len(missing) == 2 {
		dialog.ShowInformation("Missing", "Load Channel 2 and Channel 3 to copy settings", ws.win)
		return
	}
	if len(missing) == 1 {
		dialog.ShowInformation("Missing", fmt.Sprintf("Load %s to copy settings", missing[0]), ws.win)
	}
	src := ws.imgs[0]
	ws.withSuspendedRefresh(func() {
		for _, idx := range []int{1, 2} {
			if ws.imgs[idx] == nil {
				continue
			}
			dst := ws.imgs[idx]
			dst.Mode = src.Mode
			dst.Black = src.Black
			dst.White = src.White
			dst.Background = src.Background
			dst.Peak = src.Peak
			dst.ScaledPeak = src.ScaledPeak
			dst.ShowClip = src.ShowClip
			dst.AsinhScale = src.AsinhScale
			dst.MTFMidtone = src.MTFMidtone
			dst.GHSStretch = src.GHSStretch
			dst.GHSLocal = src.GHSLocal
			dst.GHSSymmetry = src.GHSSymmetry

			ws.controlSets[idx].ModeSelect.SetSelected(modeToLabel(src.Mode))
			ws.controlSets[idx].BackgroundEntry.SetValue(src.Background)
			ws.controlSets[idx].PeakEntry.SetValue(src.Peak)
			ws.controlSets[idx].ScaledPeakEntry.SetValue(src.ScaledPeak)
			setStretchParamEntries(ws.controlSets[idx], src)
			ws.controlSets[idx].ShowClip.SetChecked(src.ShowClip)
			ws.viewports[idx].blackBox.SetValue(src.Black)
			ws.viewports[idx].whiteBox.SetValue(src.White)
		}
	})
	ws.refresh()
}

func (ws *composeWorkspace) matchChannelStretch(refIdx int, matchStarCores bool) {
	if refIdx < 0 || refIdx >= 3 || ws.imgs[refIdx] == nil {
		dialog.ShowInformation("Missing", "Load the reference channel first", ws.win)
		return
	}
	refSnapshot := cloneLoadedImageForStretchMatch(ws.imgs[refIdx])
	targetSnapshots := make([]*models.LoadedImage, 3)
	targetSources := make([]*models.LoadedImage, 3)
	targetCount := 0
	for idx := 0; idx < 3; idx++ {
		if idx == refIdx || ws.imgs[idx] == nil {
			continue
		}
		targetSources[idx] = ws.imgs[idx]
		targetSnapshots[idx] = cloneLoadedImageForStretchMatch(ws.imgs[idx])
		targetCount++
	}
	if targetCount == 0 {
		dialog.ShowInformation("No Targets", "Load at least one other channel to match.", ws.win)
		return
	}
	if ws.largeMode && ws.largeStore != nil {
		ws.largeMu.RLock()
		refD, ok := ws.largeArtifacts[refIdx]
		expectedTargets := make(map[int]composeArtifactDescriptor)
		for idx := range targetSnapshots {
			if d, exists := ws.largeArtifacts[idx]; exists {
				expectedTargets[idx] = d
			}
		}
		ws.largeMu.RUnlock()
		if !ok {
			dialog.ShowError(fmt.Errorf("missing disk artifact for reference channel"), ws.win)
			return
		}
		job := startUIJob(&ws.modalJobs, "Matching Channel Stretch", "Matching channel levels...", ws.win)
		go func(expected composeArtifactDescriptor) {
			refSummary, err := largeArtifactStretchSummary(expected.Path, refSnapshot, matchStarCores)
			type result struct {
				idx     int
				state   models.ChannelState
				preview *image.RGBA
				err     error
			}
			results := make([]result, 0, targetCount)
			if err == nil {
				for idx := range targetSnapshots {
					if targetSnapshots[idx] == nil {
						continue
					}
					if job.ctx.Err() != nil {
						break
					}
					ws.largeMu.RLock()
					d, exists := ws.largeArtifacts[idx]
					ws.largeMu.RUnlock()
					if !exists {
						results = append(results, result{idx: idx, err: fmt.Errorf("missing disk artifact")})
						continue
					}
					anchors := refSummary.coreAnchors
					if anchors == nil {
						// A non-nil empty slice marks this as a target pass even
						// when the reference had too few usable anchors.
						anchors = []processing.Star{}
					}
					targetSummary, e := largeArtifactStretchSummaryWithAnchors(d.Path, targetSnapshots[idx], matchStarCores, anchors, expected.Width, expected.Height)
					st := channelStateFromImage(targetSnapshots[idx])
					if e == nil {
						e = applyLargeStretchMatch(&st, refSnapshot, refSummary, targetSummary)
					}
					var p *image.RGBA
					if e == nil {
						clone := *targetSnapshots[idx]
						applyChannelStateToImage(&clone, st)
						p, _, _, e = composeLargeStretchedPreview(d.Path, &clone)
					}
					results = append(results, result{idx: idx, state: st, preview: p, err: e})
				}
			}
			if err != nil {
				results = append(results, result{err: err})
			}
			job.finish(func() {
				ws.largeMu.RLock()
				currentRef, refCurrent := ws.largeArtifacts[refIdx]
				ws.largeMu.RUnlock()
				if !refCurrent || currentRef.Generation != expected.Generation || currentRef.Path != expected.Path {
					return
				}
				for _, r := range results {
					if r.err != nil {
						dialog.ShowError(r.err, ws.win)
						continue
					}
					ws.largeMu.RLock()
					cur, current := ws.largeArtifacts[r.idx]
					ws.largeMu.RUnlock()
					expected, expectedOK := expectedTargets[r.idx]
					if !expectedOK || !current || cur.Generation != expected.Generation || cur.Path != expected.Path || ws.imgs[r.idx] == nil {
						continue
					}
					applyChannelState(r.idx, r.state, ws.imgs, ws.viewports, ws.controlSets)
					if r.preview != nil {
						ws.largeMu.Lock()
						ws.largePreviews[r.idx] = r.preview
						ws.largeMu.Unlock()
					}
				}
				ws.refresh()
			}, nil)
		}(refD)
		return
	}

	job := startUIJob(&ws.modalJobs, "Matching Channel Stretch", "Matching channel levels...", ws.win)
	go func() {
		type matchResult struct {
			idx   int
			state models.ChannelState
			err   error
		}
		results := make([]matchResult, 0, targetCount)
		for idx, target := range targetSnapshots {
			if target == nil {
				continue
			}
			if job.ctx.Err() != nil {
				break
			}
			err := matchComposeChannelStretch(refSnapshot, target, matchStarCores)
			results = append(results, matchResult{idx: idx, state: channelStateFromImage(target), err: err})
		}
		job.finish(func() {
			applied := 0
			failed := make([]string, 0, len(results))
			ws.withSuspendedRefresh(func() {
				for _, res := range results {
					if res.err != nil {
						failed = append(failed, fmt.Sprintf("Channel %d: %v", res.idx+1, res.err))
						continue
					}
					if ws.imgs[res.idx] == nil || ws.imgs[res.idx] != targetSources[res.idx] {
						failed = append(failed, fmt.Sprintf("Channel %d: no longer loaded", res.idx+1))
						continue
					}
					applyChannelState(res.idx, res.state, ws.imgs, ws.viewports, ws.controlSets)
					applied++
				}
			})
			ws.refresh()
			if len(failed) > 0 {
				dialog.ShowError(fmt.Errorf("%s", strings.Join(failed, "\n")), ws.win)
				return
			}
			if applied == 0 {
				dialog.ShowInformation("No Targets", "No channels were matched.", ws.win)
			}
		}, nil)
	}()
}

func (ws *composeWorkspace) showMatchStretchDialog() {
	options := []string{}
	optionIdx := []int{}
	for i, name := range composeBlinkFilterNames {
		if ws.imgs[i] == nil {
			continue
		}
		options = append(options, name)
		optionIdx = append(optionIdx, i)
	}
	if len(options) == 0 {
		dialog.ShowInformation("Missing", "Load a reference channel first.", ws.win)
		return
	}
	refIdx := optionIdx[0]
	refSelect := NewSafeSelect(options, func(s string) {
		for i, name := range options {
			if name == s {
				refIdx = optionIdx[i]
				return
			}
		}
	})
	refSelect.SetSelected(options[0])
	starCoreCheck := NewToggle(nil)
	starCoreCheck.SetChecked(true)

	content := container.NewVBox(
		widget.NewForm(widget.NewFormItem("Reference", refSelect)),
		container.NewHBox(starCoreCheck, widget.NewLabel("Match star cores")),
		widget.NewLabel("Saturated star cores are ignored automatically."),
	)
	d := dialog.NewCustomConfirm("Match Channel Stretch", "Apply", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		ws.matchChannelStretch(refIdx, starCoreCheck.Checked)
	}, ws.win)
	d.Show()
}

func (ws *composeWorkspace) openLevels() {
	if ws.levelsWin == nil {
		ws.levelsWin = newRGBLevelsWindow(ws.app, ws.levels, ws.refresh)
	}
	ws.levelsWin.setHistogram(ws.latestRGBStats)
	ws.levelsWin.updateEntries()
	ws.levelsWin.win.Show()
	ws.levelsWin.win.RequestFocus()
}

func (ws *composeWorkspace) updateHistScaleLabel() {
	if ws.sharedHistCheck.Checked {
		ws.histScaleStatus.SetText("Histograms: shared filter scale")
		return
	}
	ws.histScaleStatus.SetText("Histograms: per-filter auto scale")
}
