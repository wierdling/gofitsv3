package ui

import (
	"fmt"
	"image"
	"math"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

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
		progressDialog := dialog.NewCustom("Normalizing", "Resampling arrays to match Channel 2 scale...", widget.NewProgressBarInfinite(), ws.win)
		progressDialog.Show()
		go func() {
			type result struct {
				idx, width, height int
				d                  composeArtifactDescriptor
				preview            *image.RGBA
				err                error
			}
			results := make([]result, 0, 2)
			for _, idx := range []int{0, 2} {
				d, ok := ws.largeArtifacts[idx]
				if !ok {
					results = append(results, result{idx: idx, err: fmt.Errorf("missing disk artifact for channel %d", idx+1)})
					continue
				}
				lines := utils.FormatHeadersLines(ws.imgs[idx].Primary, ws.imgs[idx].HDU.Header)
				sourceScale := processing.GetPixelScale(lines)
				if targetScale == 0 || math.Abs(sourceScale-targetScale)/targetScale < 0.01 {
					results = append(results, result{idx: idx, width: d.Width, height: d.Height, d: d})
					continue
				}
				ratio := sourceScale / targetScale
				newW, newH := int(float64(d.Width)*ratio), int(float64(d.Height)*ratio)
				nd, err := ws.largeStore.ResizeArtifact(d, newW, newH)
				var preview *image.RGBA
				if err == nil {
					preview, _, _, err = composeLargeStretchedPreview(nd.Path, ws.imgs[idx])
				}
				results = append(results, result{idx: idx, width: newW, height: newH, d: nd, preview: preview, err: err})
			}
			fyne.Do(func() {
				progressDialog.Hide()
				count := 0
				for _, r := range results {
					if r.err != nil {
						dialog.ShowError(r.err, ws.win)
						continue
					}
					if r.width == 0 {
						continue
					}
					ws.largeArtifacts[r.idx] = r.d
					if r.preview != nil {
						ws.largePreviews[r.idx] = r.preview
					}
					ws.imgs[r.idx].HDU.Data.Width, ws.imgs[r.idx].HDU.Data.Height = r.width, r.height
					ws.imgs[r.idx].HDU.Data.Pixels = nil
					clearComposeOrigPixels(&ws.origPixels, r.idx)
					count++
				}
				ws.refresh()
				dialog.ShowInformation("Complete", fmt.Sprintf("Rescaled %d channel(s) to match Channel 2 pixel scale.", count), ws.win)
			})
		}()
		return
	}

	// Calculate the absolute physical scale of the reference channel
	linesG := utils.FormatHeadersLines(ws.imgs[1].Primary, ws.imgs[1].HDU.Header)
	targetScale := processing.GetPixelScale(linesG)

	progressDialog := dialog.NewCustom("Normalizing", "Resampling arrays to match Channel 2 scale...", widget.NewProgressBarInfinite(), ws.win)
	progressDialog.Show()

	go func() {
		resizedCount := 0
		for i := 0; i < 3; i++ {
			if i == 1 {
				continue // Channel 2 is the reference
			}

			lines := utils.FormatHeadersLines(ws.imgs[i].Primary, ws.imgs[i].HDU.Header)
			sourceScale := processing.GetPixelScale(lines)

			// Skip if scales match within a 1% margin of error to prevent destructive sub-pixel resampling
			if math.Abs(sourceScale-targetScale)/targetScale < 0.01 {
				continue
			}

			ratio := sourceScale / targetScale
			newW := int(float64(ws.imgs[i].HDU.Data.Width) * ratio)
			newH := int(float64(ws.imgs[i].HDU.Data.Height) * ratio)

			resized := processing.ResizeChannel(ws.imgs[i].HDU.Data.Pixels, ws.imgs[i].HDU.Data.Width, ws.imgs[i].HDU.Data.Height, newW, newH)

			ws.imgs[i].HDU.Data.Pixels = resized
			ws.imgs[i].HDU.Data.Width = newW
			ws.imgs[i].HDU.Data.Height = newH
			clearComposeOrigPixels(&ws.origPixels, i)
			resizedCount++
		}

		fyne.Do(func() {
			progressDialog.Hide()
			ws.refresh()
			dialog.ShowInformation("Complete", fmt.Sprintf("Rescaled %d channel(s) to match Channel 2 pixel scale.", resizedCount), ws.win)
		})
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
	targetCount := 0
	for idx := 0; idx < 3; idx++ {
		if idx == refIdx || ws.imgs[idx] == nil {
			continue
		}
		targetSnapshots[idx] = cloneLoadedImageForStretchMatch(ws.imgs[idx])
		targetCount++
	}
	if targetCount == 0 {
		dialog.ShowInformation("No Targets", "Load at least one other channel to match.", ws.win)
		return
	}
	if ws.largeMode && ws.largeStore != nil {
		progressDialog := dialog.NewCustom("Matching Channel Stretch", "Matching channel levels...", widget.NewProgressBarInfinite(), ws.win)
		progressDialog.Show()
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
			progressDialog.Hide()
			dialog.ShowError(fmt.Errorf("missing disk artifact for reference channel"), ws.win)
			return
		}
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
			fyne.Do(func() {
				progressDialog.Hide()
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
					if !expectedOK || !current || cur.Generation != expected.Generation || cur.Path != expected.Path {
						continue
					}
					applyChannelState(r.idx, r.state, ws.imgs, ws.viewports, ws.controlSets)
					if r.preview != nil {
						ws.largePreviews[r.idx] = r.preview
					}
				}
				ws.refresh()
			})
		}(refD)
		return
	}

	progressDialog := dialog.NewCustom("Matching Channel Stretch", "Matching channel levels...", widget.NewProgressBarInfinite(), ws.win)
	progressDialog.Show()
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
			err := matchComposeChannelStretch(refSnapshot, target, matchStarCores)
			results = append(results, matchResult{idx: idx, state: channelStateFromImage(target), err: err})
		}
		fyne.Do(func() {
			progressDialog.Hide()
			applied := 0
			failed := make([]string, 0, len(results))
			ws.withSuspendedRefresh(func() {
				for _, res := range results {
					if res.err != nil {
						failed = append(failed, fmt.Sprintf("Channel %d: %v", res.idx+1, res.err))
						continue
					}
					if ws.imgs[res.idx] == nil {
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
		})
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
