package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// composeSourceLabel names the source an image belongs to, for the star
// diagnostics overlay's per-star detail popup.
func (ws *composeWorkspace) composeSourceLabel(img *models.LoadedImage) string {
	if len(ws.imgs) > 0 && img == ws.imgs[0] {
		return "Channel 1 (Blue)"
	}
	if len(ws.imgs) > 1 && img == ws.imgs[1] {
		return "Channel 2 (Green)"
	}
	if len(ws.imgs) > 2 && img == ws.imgs[2] {
		return "Channel 3 (Red)"
	}
	for _, layer := range ws.overlayLayers {
		if layer != nil && layer.idx < len(ws.imgs) && ws.imgs[layer.idx] == img {
			return layer.name
		}
	}
	return "Source"
}

// recomputeStarDiagnostics maps every treated source's star-treatment fits
// (Usable and not) onto the current composite grid and refreshes the overlay,
// and separately overlays each channel's own star map (no cross-channel
// geometry mapping) on that channel's own viewport. Call after every applied
// render and whenever the toggle changes; it is a no-op past the
// composite-not-rendered check when diagnostics are off.
func (ws *composeWorkspace) recomputeStarDiagnostics() {
	if ws.viewports[3] == nil {
		return
	}
	if !ws.starDiagEnabled {
		ws.starDiagPoints = nil
		ws.viewports[3].setDiagnosticOverlay(nil)
		if ws.starDiagLabel != nil {
			ws.starDiagLabel.SetText("")
		}
		for i := 0; i < 3; i++ {
			ws.starDiagChannelPts[i] = nil
			if ws.viewports[i] != nil {
				ws.viewports[i].setDiagnosticOverlay(nil)
			}
		}
		return
	}
	ws.recomputeChannelStarDiagnostics()
	w, h := ws.viewports[3].origW, ws.viewports[3].origH
	if w <= 0 || h <= 0 || len(ws.imgs) < 2 || ws.imgs[1] == nil {
		ws.starDiagPoints = nil
		ws.viewports[3].setDiagnosticOverlay(nil)
		if ws.starDiagLabel != nil {
			ws.starDiagLabel.SetText("Star diagnostics: composite not rendered yet.")
		}
		return
	}
	grid := *ws.imgs[1]
	var points []processing.StarTreatmentDiagnostic
	var forceEligible []bool
	treated, skipped := 0, 0
	for i, img := range ws.imgs {
		if img == nil {
			continue
		}
		model := ws.starTreatments.modelSnapshot(img)
		if model == nil {
			continue
		}
		dx, dy, rot, ok := ws.composeChannelOffsetFields(i)
		if !ok {
			continue
		}
		pts, err := processing.MapStarTreatmentDiagnostics(model, ws.composeSourceLabel(img),
			processing.DiskChannel{Image: *img, OffsetX: dx, OffsetY: dy, OffsetRot: rot}, grid, w, h)
		if err != nil {
			continue
		}
		for _, p := range pts {
			if p.Usable {
				treated++
			} else {
				skipped++
			}
		}
		points = append(points, pts...)
		for _, p := range pts {
			forceEligible = append(forceEligible, ws.starDiagnosticForceEligible(p))
		}
	}
	ws.starDiagPoints = points
	ws.viewports[3].setDiagnosticOverlayWithForce(points, forceEligible)
	if ws.starDiagLabel != nil {
		ws.starDiagLabel.SetText(fmt.Sprintf("Star diagnostics: %d treated (green), %d skipped in the composite. Hover for + to force or a red circle for skipped-star diagnostics. Click a circle to view and copy diagnostics.", treated, skipped))
	}
}

// recomputeChannelStarDiagnostics overlays each channel's own prepared
// star-treatment fits directly on that channel's own viewport, in that
// channel's native pixel coordinates — no cross-channel geometry mapping, so
// a channel with no gentler-stretch/whitening model of its own shows nothing.
func (ws *composeWorkspace) recomputeChannelStarDiagnostics() {
	for i := 0; i < 3; i++ {
		if ws.viewports[i] == nil {
			continue
		}
		var img *models.LoadedImage
		if i < len(ws.imgs) {
			img = ws.imgs[i]
		}
		model := ws.starTreatments.modelSnapshot(img)
		if model == nil {
			ws.starDiagChannelPts[i] = nil
			ws.viewports[i].setDiagnosticOverlay(nil)
			continue
		}
		w, h := model.SourceSize()
		var points []processing.StarTreatmentDiagnostic
		for _, f := range model.Fits() {
			if f.X < 0 || f.Y < 0 || f.X > float64(w) || f.Y > float64(h) {
				continue
			}
			points = append(points, processing.StarTreatmentDiagnostic{
				Source: ws.composeSourceLabel(img), SourceID: f.SourceID, X: f.X, Y: f.Y,
				Usable: f.Usable, Reason: f.Reason, Saturated: f.Saturated, HaloValidated: f.HaloValidated,
			})
		}
		ws.starDiagChannelPts[i] = points
		forceEligible := make([]bool, len(points))
		for j, p := range points {
			forceEligible[j] = ws.starDiagnosticForceEligible(p)
		}
		ws.viewports[i].setDiagnosticOverlayWithForce(points, forceEligible)
	}
}

// starDiagnosticForceEligible reports whether the Force override can do
// anything for p. Forcing only bypasses the whitening pass's own runtime
// background-sample/positive-excess gate (see StarNeutralizer); it never
// resurrects a fit an earlier stage already rejected (p.Usable == false), so
// a skipped star is never eligible regardless of source or reason.
func (ws *composeWorkspace) starDiagnosticForceEligible(p processing.StarTreatmentDiagnostic) bool {
	ref := ws.whiteningReference()
	return p.Usable && ref != nil && ws.composeSourceLabel(ref) == p.Source && !ws.starWhitening.ForcedStars[p.SourceID]
}

func (ws *composeWorkspace) setStarDiagnosticOverlay(vp *viewport, points []processing.StarTreatmentDiagnostic) {
	if vp == nil || len(points) == 0 {
		if vp != nil {
			vp.setDiagnosticOverlay(nil)
		}
		return
	}
	eligible := make([]bool, len(points))
	for i, p := range points {
		eligible[i] = ws.starDiagnosticForceEligible(p)
	}
	vp.setDiagnosticOverlayWithForce(points, eligible)
}

// starDiagnosticTapped finds the diagnostic point nearest a tap on the
// composite viewport and shows its detail. Called instead of the measure
// tool's tap handler while diagnostics are enabled.
func (ws *composeWorkspace) starDiagnosticTapped(pos fyne.Position) {
	if ws.viewports[3] == nil || ws.viewports[3].overlay == nil {
		return
	}
	if forceIdx, force := ws.viewports[3].overlay.forceMarkerAt(pos); force && forceIdx >= 0 && forceIdx < len(ws.starDiagPoints) {
		ws.forceStarDiagnostic(ws.starDiagPoints[forceIdx])
		return
	}
	idx, ok := ws.viewports[3].overlay.nearestMarker(pos)
	if !ok || idx < 0 || idx >= len(ws.starDiagPoints) {
		return
	}
	p := ws.starDiagPoints[idx]
	ws.showStarDiagnosticDialog(p)
}

// channelStarDiagnosticTapped is the per-channel-viewport counterpart of
// starDiagnosticTapped, showing detail for that channel's own star map only.
func (ws *composeWorkspace) channelStarDiagnosticTapped(idx int, pos fyne.Position) {
	if idx < 0 || idx >= len(ws.viewports) || ws.viewports[idx] == nil || ws.viewports[idx].overlay == nil {
		return
	}
	pts := ws.starDiagChannelPts[idx]
	if forceIdx, force := ws.viewports[idx].overlay.forceMarkerAt(pos); force && forceIdx >= 0 && forceIdx < len(pts) {
		ws.forceStarDiagnostic(pts[forceIdx])
		return
	}
	i, ok := ws.viewports[idx].overlay.nearestMarker(pos)
	if !ok || i < 0 || i >= len(pts) {
		return
	}
	p := pts[i]
	ws.showStarDiagnosticDialog(p)
}

func (ws *composeWorkspace) forceStarDiagnostic(p processing.StarTreatmentDiagnostic) {
	if !ws.starDiagnosticForceEligible(p) {
		return
	}
	if ws.starWhitening.ForcedStars == nil {
		ws.starWhitening.ForcedStars = map[int]bool{}
	}
	ws.starWhitening.ForcedStars[p.SourceID] = true
	ws.starTreatments.sync(ws.imgs, true)
	ws.refresh()
}

// showStarDiagnosticDialog shows one star's treatment detail. When the point
// belongs to the current White Stars reference source, it also offers a
// Force/Clear override for the runtime whitening checks (background sample
// count, positive excess) in StarNeutralizer — for a star that keeps
// skipping whitening for reasons the diagnostic reason string does not
// cover, an override tells the neutralizer to whiten it on a best-effort
// basis anyway. The override is keyed by catalog SourceID and persists with
// the White Stars setting.
func (ws *composeWorkspace) showStarDiagnosticDialog(p processing.StarTreatmentDiagnostic) {
	status := "treated"
	if !p.Usable {
		status = "skipped"
	}
	reason := p.Reason
	if reason == "" {
		reason = "(no reason recorded)"
	}
	msg := fmt.Sprintf("Source: %s\nStar ID: %d\nPosition: %.1f, %.1f\nStatus: %s\nSaturated: %t · Halo validated: %t\n\nReason: %s",
		p.Source, p.SourceID, p.X, p.Y, status, p.Saturated, p.HaloValidated, reason)
	label := widget.NewLabel(msg)
	label.Wrapping = fyne.TextWrapWord
	ws.win.Clipboard().SetContent(msg)
	content := container.NewVBox(label, widget.NewLabel("Diagnostics copied to clipboard."))

	ref := ws.whiteningReference()
	alreadyForced := ws.starWhitening.ForcedStars[p.SourceID]
	canOverride := ref != nil && ws.composeSourceLabel(ref) == p.Source && (p.Usable || alreadyForced)
	var d dialog.Dialog
	if canOverride {
		state := widget.NewLabel("")
		refreshState := func() {
			if ws.starWhitening.ForcedStars[p.SourceID] {
				state.SetText("Override: this star is forced to whiten regardless of the runtime check.")
			} else {
				state.SetText("Override: none.")
			}
		}
		refreshState()
		apply := func() {
			ws.starTreatments.sync(ws.imgs, true)
			ws.refresh()
		}
		forceBtn := widget.NewButton("Force process", func() {
			if ws.starWhitening.ForcedStars == nil {
				ws.starWhitening.ForcedStars = map[int]bool{}
			}
			ws.starWhitening.ForcedStars[p.SourceID] = true
			d.Hide()
			refreshState()
			apply()
		})
		if !p.Usable {
			// A skipped fit was already rejected upstream of whitening; forcing
			// can't resurrect it (see starDiagnosticForceEligible), so only let
			// an existing override be cleared here.
			forceBtn.Disable()
		}
		clearBtn := widget.NewButton("Clear override", func() {
			delete(ws.starWhitening.ForcedStars, p.SourceID)
			refreshState()
			apply()
		})
		content.Add(widget.NewSeparator())
		content.Add(state)
		content.Add(container.NewHBox(forceBtn, clearBtn))
	}
	d = dialog.NewCustom("Star Treatment Diagnostic", "Close", content, ws.win)
	d.Show()
}
