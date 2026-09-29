package ui

import (
	"errors"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// The Manual Offset (X/Y/Rot) fields are the source of truth for each channel's
// placement, applied at RENDER time only — imgs[idx] always holds the original
// loaded pixels and is never warped/baked. renderImages() returns an
// offset-applied view used for the composite (merge) and the per-channel
// previews the blink shows. Results are cached so an unchanged offset isn't
// re-warped on every refresh.
func (ws *composeWorkspace) composeChannelOffsetFields(idx int) (dx, dy, rot float64, ok bool) {
	if idx < 0 || idx >= len(ws.controlSets) || ws.controlSets[idx] == nil {
		return 0, 0, 0, false
	}
	cc := ws.controlSets[idx]
	if cc.XOffsetEntry == nil || cc.YOffsetEntry == nil {
		return 0, 0, 0, false
	}
	dx = cc.XOffsetEntry.Value()
	dy = cc.YOffsetEntry.Value()
	if cc.RotOffsetEntry != nil {
		rot = cc.RotOffsetEntry.Value()
	}
	return dx, dy, rot, true
}

func (ws *composeWorkspace) alignChannels() {
	if ws.imgs[0] == nil || ws.imgs[1] == nil || ws.imgs[2] == nil {
		dialog.ShowInformation("Missing Channels", "Load all three FITS channels before aligning.", ws.win)
		return
	}
	// Snapshot the active extra-layer slots and controls before starting the
	// worker.  The alignment root is still Channel 2, but every loaded layer
	// must be registered to that same grid as the RGB channels.
	extraAlignSlots := make([]int, 0, len(ws.overlayLayers))
	alignControls := make(map[int]*models.ChannelControl, len(ws.overlayLayers))
	for _, layer := range ws.overlayLayers {
		if layer != nil {
			extraAlignSlots = append(extraAlignSlots, layer.idx)
			alignControls[layer.idx] = layer.control
		}
	}
	alignSlots := composeAlignmentSlots(ws.imgs, extraAlignSlots)
	if ws.largeMode && ws.largeStore != nil {
		progressDialog := dialog.NewCustom("Aligning", "Extracting star catalogs...", widget.NewProgressBarInfinite(), ws.win)
		progressDialog.Show()
		go func() {
			ws.largeMu.RLock()
			descs := make(map[int]composeArtifactDescriptor, len(alignSlots))
			channels := make([]composeAlignmentChannel, 0, len(alignSlots))
			for _, idx := range alignSlots {
				d, exists := ws.largeArtifacts[idx]
				if exists {
					descs[idx] = d
				}
			}
			ws.largeMu.RUnlock()
			var err error
			for _, idx := range alignSlots {
				d, exists := descs[idx]
				if !exists {
					err = fmt.Errorf("missing disk artifact for Channel %d", idx+1)
					break
				}
				stars, e := largeArtifactStars(d.Path)
				if e != nil {
					err = e
					break
				}
				channels = append(channels, composeAlignmentChannel{Index: idx + 1, Width: d.Width, Height: d.Height, UsableStars: stars, Footprint: composeAlignmentFootprint{MaxX: float64(d.Width), MaxY: float64(d.Height)}})
			}
			match := func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
				if len(target.UsableStars) < 3 || len(reference.UsableStars) < 3 {
					return composeAlignmentMatch{}, fmt.Errorf("insufficient stars found for alignment")
				}
				sx, sy := float64(reference.Width)/float64(target.Width), float64(reference.Height)/float64(target.Height)
				targetStars := make([]processing.Star, len(target.UsableStars))
				for i, star := range target.UsableStars {
					targetStars[i] = star
					targetStars[i].X *= sx
					targetStars[i].Y *= sy
				}
				// Both catalogs are now expressed in the reference pixel frame;
				// use the same robust histogram/mutual-neighbour fit and global
				// refinement as the normal alignment path.
				forward, stats, e := processing.FitCatalogResidual(targetStars, reference.UsableStars, reference.Width, reference.Height, 30, "general")
				if e != nil {
					return composeAlignmentMatch{}, e
				}
				// FitCatalogResidual maps the resized target catalog into the
				// reference frame. Convert that forward transform back to the
				// target's native frame exactly as the normal pixel path does.
				return composeAlignmentMatchFromFittedAffine(target, reference, forward, stats), nil
			}
			alignment := composeAlignmentResult{}
			if err == nil {
				alignment = coordinateComposeAlignmentWithEligibility(channels, 2, match, composeAlignmentFallbackPairEligible)
			}
			fyne.Do(func() {
				progressDialog.Hide()
				if err != nil {
					dialog.ShowError(err, ws.win)
					return
				}
				// Channel 2 is the alignment root.  Do not commit either
				// fitted result if the reference artifact was replaced while
				// catalogs were being extracted.
				refExpected := descs[1]
				refCurrent, refOK := ws.largeStore.Descriptor(refExpected.Slot)
				if !refOK || !composeLargeAlignmentReferenceCurrent(refCurrent, refExpected) {
					return
				}
				for _, idx := range alignSlots {
					if idx == 1 {
						continue
					}
					res := alignment.Channels[idx+1]
					expected := descs[idx]
					cur, current := ws.largeStore.Descriptor(expected.Slot)
					if !current || cur.Generation != expected.Generation || cur.Path != expected.Path || !res.Applicable {
						continue
					}
					setChannelAlignTransform(ws.imgs[idx], res.Backward)
					// The fitted affine supersedes the user nudge, matching the
					// normal alignment path. Keep controls and metadata in sync.
					control := alignControls[idx]
					if idx < len(ws.controlSets) {
						control = ws.controlSets[idx]
					}
					resetComposeAlignmentOffsets(control)
				}
				ws.refresh()
			})
		}()
		return
	}

	savedStates := ws.captureViewportStates()

	// Reference is Channel 2 (green); align every other loaded channel to it.
	// imgs always holds the ORIGINAL pixels (offsets are applied only at render
	// time), so the computed offset is absolute. Store it in the Manual Offset
	// fields (the source of truth); the refresh below renders it.
	channels := make([]composeAlignmentChannel, 0, len(alignSlots))
	for _, idx := range alignSlots {
		img := ws.imgs[idx]
		channels = append(channels, composeAlignmentChannel{Index: idx + 1, OriginalPixels: img.HDU.Data.Pixels, Width: img.HDU.Data.Width, Height: img.HDU.Data.Height})
	}

	progressDialog := dialog.NewCustom("Aligning", "Please wait...", widget.NewProgressBarInfinite(), ws.win)
	progressDialog.Show()

	go func() {
		// alignOne returns the backward (output→source) transform that registers
		// base to the reference, using the same robust pixel-space star matcher
		// the mosaic builder uses (WCS-independent: channel WCS headers can
		// disagree with the real pixel registration by ~100 px).
		match := func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
			_, fitted, stats, err := processing.AlignChannelByStars(
				target.OriginalPixels, target.Width, target.Height,
				reference.OriginalPixels, reference.Width, reference.Height, 30.0, "general",
			)
			if err != nil {
				return composeAlignmentMatch{}, err
			}
			return composeAlignmentMatchFromFittedAffine(target, reference, fitted, stats), nil
		}

		for i := range channels {
			channels[i].Footprint = composeAlignmentFootprint{MaxX: float64(channels[i].Width), MaxY: float64(channels[i].Height)}
			channels[i].UsableStars = processing.ExtractStars(channels[i].OriginalPixels, channels[i].Width, channels[i].Height, 4.0, 3)
		}
		alignment := coordinateComposeAlignmentWithEligibility(channels, 2, match, composeAlignmentFallbackPairEligible)

		fyne.Do(func() {
			progressDialog.Hide()

			// Write the alignment into the Manual Offset fields; the offset is
			// applied at render time (not baked) by the refresh below.
			setAndApply := func(idx int, back processing.AffineTransform) (dx, dy, rot float64) {
				w := ws.imgs[idx].HDU.Data.Width
				h := ws.imgs[idx].HDU.Data.Height
				// Store the full fitted affine (scale/skew included); the Manual
				// Offset becomes a zeroed user nudge applied on top of it. The
				// returned dx/dy/rot are the equivalent translation/rotation for
				// the summary line only.
				dx, dy, rot = extractManualOffset(back, w, h)
				setChannelAlignTransform(ws.imgs[idx], back)
				control := alignControls[idx]
				if idx < len(ws.controlSets) {
					control = ws.controlSets[idx]
				}
				if control != nil {
					if control.XOffsetEntry != nil {
						control.XOffsetEntry.SetValue(0)
					}
					if control.YOffsetEntry != nil {
						control.YOffsetEntry.SetValue(0)
					}
					if control.RotOffsetEntry != nil {
						control.RotOffsetEntry.SetValue(0)
					}
				}
				return dx, dy, rot
			}

			resultDetail := func(result composeAlignmentChannelResult) string {
				detail := fmt.Sprintf("matched=%d inliers=%d rms=%.2f", result.Stats.MatchedStars, result.Stats.GlobalInliers, result.Stats.RMS)
				if !result.Direct {
					detail += fmt.Sprintf(" via Channel %d", result.ReferenceIndex)
				}
				return detail
			}
			lines := make([]string, 0, len(alignSlots)-1)
			failures := make([]string, 0)
			for _, idx := range alignSlots {
				if idx == 1 {
					continue
				}
				result := alignment.Channels[idx+1]
				name := fmt.Sprintf("Channel %d", idx+1)
				if result.Applicable {
					dx, dy, rot := setAndApply(idx, result.Backward)
					lines = append(lines, fmt.Sprintf("%s:\n  X: %+.2f  Y: %+.2f  Rot: %+.2f°\n  %s", name, dx, dy, rot, resultDetail(result)))
					continue
				}
				err := composeAlignmentResultError(alignment, idx+1, result)
				failures = append(failures, fmt.Sprintf("%s: %v", name, err))
				lines = append(lines, fmt.Sprintf("%s:\n  FAILED: %v", name, err))
			}

			ws.refresh()
			ws.restoreViewportStates(savedStates)

			if len(failures) == len(alignSlots)-1 {
				dialog.ShowError(errors.New(strings.Join(failures, "\n")), ws.win)
				return
			}
			msg := "Alignment Complete.\n\n" + strings.Join(lines, "\n\n")
			dialog.ShowInformation("Alignment Data", msg, ws.win)
		})
	}()
}
