package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/starstretchpreview"
)

// composeStarTreatments keeps the gentler-star-stretch models that Compose
// renders through. Each enabled source owns stretch-independent fits (from
// the original file and its reviewed map) and a model prepared for the
// current stretch and strength. sync runs before every render: a source whose
// settings still match its model renders treated; one whose settings changed
// renders untreated while a background job prepares a replacement, then the
// preview refreshes. Nothing here is persisted; the setting itself lives in
// LoadedImage.StarStretch.
type composeStarTreatments struct {
	mu        sync.Mutex
	win       fyne.Window
	refresh   func()
	largeMode func() bool
	// whiteningRef returns the White Stars reference source, which needs a
	// model even when its own gentler stretch is off; nil when whitening is
	// off.
	whiteningRef func() *models.LoadedImage
	// geometryRef returns the source whose prepared geometry every other
	// treated source borrows (its own background re-measured); nil means each
	// source uses its own map.
	geometryRef func() *models.LoadedImage
	entries     map[*models.LoadedImage]*composeStarTreatmentEntry
}

type composeStarTreatmentEntry struct {
	fits   *starstretchpreview.TreatmentFits
	model  *processing.StarTreatmentModel
	cancel context.CancelFunc
	busy   bool
	status string // last outcome, shown in the dialog
	stars  int
	// derivedFrom is the reference model this model's geometry was borrowed
	// from; nil for a source fitted on its own map.
	derivedFrom *processing.StarTreatmentModel
}

func newComposeStarTreatments(win fyne.Window, largeMode func() bool) *composeStarTreatments {
	return &composeStarTreatments{win: win, largeMode: largeMode, entries: map[*models.LoadedImage]*composeStarTreatmentEntry{}}
}

// modelCurrent reports whether a prepared model still matches the source's
// stretch settings, strength, grid and geometry origin.
func (c *composeStarTreatments) modelCurrent(img *models.LoadedImage, e *composeStarTreatmentEntry, derivedFrom *processing.StarTreatmentModel) bool {
	if e == nil || e.model == nil || !e.model.MatchesStretch(*img) || e.model.Strength() != img.StarStretch.Strength || e.derivedFrom != derivedFrom {
		return false
	}
	if !c.largeMode() {
		if w, h := e.model.SourceSize(); w != img.HDU.Data.Width || h != img.HDU.Data.Height {
			return false
		}
	}
	return true
}

// modelFor returns a source's prepared model when it is current, for uses
// other than that source's own stretch (White Stars geometry).
func (c *composeStarTreatments) modelFor(img *models.LoadedImage) *processing.StarTreatmentModel {
	if img == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[img]; e != nil && c.modelCurrent(img, e, nil) {
		return e.model
	}
	return nil
}

// geometrySource returns the reference model and fits a source must borrow,
// or nil pointers when it fits on its own map (no shared geometry, or it is
// the reference itself). ready is false while the reference is not prepared.
func (c *composeStarTreatments) geometrySource(img *models.LoadedImage) (ref *models.LoadedImage, model *processing.StarTreatmentModel, fits *starstretchpreview.TreatmentFits, ready bool) {
	if c.geometryRef == nil {
		return nil, nil, nil, true
	}
	ref = c.geometryRef()
	if ref == nil || ref == img {
		return nil, nil, nil, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[ref]
	if e == nil || !c.modelCurrent(ref, e, nil) || e.fits == nil {
		return ref, nil, nil, false
	}
	return ref, e.model, e.fits, true
}

// sync reconciles every source with its setting. A source needs a model when
// its gentler stretch is enabled or it is the White Stars reference; only the
// former attaches the model to the source's own rendering. It must run on the
// UI thread before a render snapshot is taken. userInitiated shows errors in
// a dialog; automatic re-preparation only records them.
func (c *composeStarTreatments) sync(imgs []*models.LoadedImage, userInitiated bool) {
	var whiteningRef, geometryRef *models.LoadedImage
	if c.whiteningRef != nil {
		whiteningRef = c.whiteningRef()
	}
	if c.geometryRef != nil {
		geometryRef = c.geometryRef()
	}
	live := map[*models.LoadedImage]bool{}
	for _, img := range imgs {
		if img != nil {
			live[img] = true
		}
	}
	c.mu.Lock()
	for img, e := range c.entries {
		if !live[img] {
			if e.cancel != nil {
				e.cancel()
			}
			delete(c.entries, img)
		}
	}
	c.mu.Unlock()
	for _, img := range imgs {
		if img == nil {
			continue
		}
		if !img.StarStretch.Enabled && img != whiteningRef && img != geometryRef {
			img.StarTreatment = nil
			c.mu.Lock()
			if e := c.entries[img]; e != nil && e.cancel != nil {
				e.cancel()
				e.cancel, e.busy = nil, false
			}
			c.mu.Unlock()
			continue
		}
		c.mu.Lock()
		e := c.entries[img]
		if e == nil {
			e = &composeStarTreatmentEntry{}
			c.entries[img] = e
		}
		c.mu.Unlock()
		_, refModel, refFits, ready := c.geometrySource(img)
		c.mu.Lock()
		current := c.modelCurrent(img, e, refModel)
		busy := e.busy
		c.mu.Unlock()
		if current {
			if img.StarStretch.Enabled {
				img.StarTreatment = e.model
			} else {
				img.StarTreatment = nil
			}
			continue
		}
		img.StarTreatment = nil
		if !ready {
			c.mu.Lock()
			e.status = "waiting for the reference source's star geometry"
			c.mu.Unlock()
			continue
		}
		if !busy {
			c.start(img, e, refModel, refFits, userInitiated)
		}
	}
}

// start prepares (and if needed fits) a source in the background. With a
// reference model the source borrows that geometry and only measures its own
// backgrounds.
func (c *composeStarTreatments) start(img *models.LoadedImage, e *composeStarTreatmentEntry, refModel *processing.StarTreatmentModel, refFits *starstretchpreview.TreatmentFits, userInitiated bool) {
	if strings.TrimSpace(img.Path) == "" {
		e.status = "no original FITS path"
		return
	}
	if img.Rotation90%4 != 0 {
		e.status = "unavailable: the source was rotated in Compose; the star map applies to the original grid"
		return
	}
	meta := *img
	meta.HDU = fitsio.HDU{}
	meta.Primary = fitsio.Header{}
	strength := img.StarStretch.Strength
	// Disk-backed Compose has no source pixels in memory; the original file is
	// reopened by the job and the grid comes from that file.
	var pixels []float32
	w, h := 0, 0
	if !c.largeMode() {
		pixels = img.HDU.Data.Pixels
		w, h = img.HDU.Data.Width, img.HDU.Data.Height
	}
	path := img.Path
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if e.cancel != nil {
		e.cancel()
	}
	e.cancel, e.busy, e.status = cancel, true, "preparing…"
	fits := e.fits
	c.mu.Unlock()
	var pt *progressTracker
	progress := func(string, int, int) {}
	if userInitiated {
		pt = newProgressTrackerWithContextOnUI("Gentler Star Stretch", "Preparing stellar footprints...", c.win, ctx, cancel)
		progress = pt.progress
	}
	go func() {
		var model *processing.StarTreatmentModel
		var err error
		stars := 0
		if refModel != nil {
			model, stars, err = starstretchpreview.DeriveTreatmentModel(ctx, refFits, refModel, path, meta, strength, pixels, progress)
		} else {
			var mapPath string
			mapPath, err = starstretchpreview.ResolveMapPath(path, "")
			if err == nil && !fits.Current(path, mapPath, w, h) {
				fits, err = starstretchpreview.FitTreatment(ctx, path, "", pixels, w, h, progress)
			}
			if err == nil {
				model, stars, err = fits.Model(ctx, meta, strength, pixels, progress)
			}
		}
		if pt != nil {
			pt.hide()
		}
		fyne.Do(func() {
			c.mu.Lock()
			cancelled := ctx.Err() != nil
			if !cancelled {
				e.cancel, e.busy = nil, false
			}
			if cancelled {
				c.mu.Unlock()
				return
			}
			if err != nil {
				e.status = "failed: " + err.Error()
				c.mu.Unlock()
				debuglog.Log(fmt.Sprintf("compose star treatment %s: %v", path, err))
				if userInitiated && !errors.Is(err, context.Canceled) {
					dialog.ShowError(err, c.win)
				}
				return
			}
			if refModel == nil {
				e.fits = fits
			}
			e.stars = stars
			// Settings or the reference may have moved on while preparing; a
			// stale model is never installed. The next render's sync starts
			// another job.
			c.mu.Unlock()
			_, wantRef, _, _ := c.geometrySource(img)
			c.mu.Lock()
			if model.MatchesStretch(*img) && model.Strength() == img.StarStretch.Strength && wantRef == refModel {
				e.model, e.derivedFrom = model, refModel
				if refModel != nil {
					e.status = fmt.Sprintf("%d stars treated with the reference source's geometry", stars)
				} else {
					e.status = fmt.Sprintf("%d of %d fitted stars treated", stars, fits.Usable)
				}
				if img.StarStretch.Enabled {
					img.StarTreatment = model
				}
			} else {
				e.status = "settings changed; preparing again on next render"
			}
			c.mu.Unlock()
			if c.refresh != nil {
				c.refresh()
			}
		})
	}()
}

func (c *composeStarTreatments) statusFor(img *models.LoadedImage) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[img]; e != nil && e.status != "" {
		return e.status
	}
	if img.StarStretch.Enabled {
		return "not prepared yet"
	}
	return "off"
}

// showComposeWhiteStarsDialog edits the White Stars setting: the reference
// source whose map supplies star footprints, the strength, the neutral level
// and which output channels move. Apply stores the setting; the caller's
// refresh prepares the reference if needed and re-renders.
func showComposeWhiteStarsDialog(win fyne.Window, imgs []*models.LoadedImage, roles []string, blinkIDs []string, state *models.StarWhiteningState, c *composeStarTreatments, refresh func()) {
	var ids []string
	var labels []string
	selected := 0
	for i, img := range imgs {
		if img == nil || strings.TrimSpace(img.Path) == "" || i >= len(blinkIDs) || blinkIDs[i] == "" {
			continue
		}
		role := ""
		if i < len(roles) {
			role = roles[i]
		}
		if blinkIDs[i] == state.ReferenceBlinkID || (state.ReferenceBlinkID == "" && i == 2) {
			selected = len(ids)
		}
		ids = append(ids, blinkIDs[i])
		labels = append(labels, fmt.Sprintf("Channel %d — %s — %s", i+1, role, img.Path))
	}
	if len(ids) == 0 {
		dialog.ShowInformation("White Stars", "No loaded Compose source has an original FITS path.", win)
		return
	}
	enabled := widget.NewCheck("Whiten stars in the composite", nil)
	enabled.SetChecked(state.Enabled)
	reference := widget.NewSelect(labels, nil)
	reference.SetSelectedIndex(selected)
	strength := widget.NewEntry()
	s := state.Strength
	if s <= 0 {
		s = .75
	}
	strength.SetText(strconv.FormatFloat(s, 'f', 2, 64))
	level := widget.NewSelect([]string{"White (brightest channel)", "Preserve luminance"}, nil)
	if state.Level == models.StarWhiteningLuminance {
		level.SetSelectedIndex(1)
	} else {
		level.SetSelectedIndex(0)
	}
	red, green, blue := widget.NewCheck("Red", nil), widget.NewCheck("Green", nil), widget.NewCheck("Blue", nil)
	red.SetChecked(state.Red || !state.Enabled)
	green.SetChecked(state.Green || !state.Enabled)
	blue.SetChecked(state.Blue || !state.Enabled)
	status := widget.NewLabel("")
	status.Wrapping = fyne.TextWrapWord
	if refImg := whiteningReferenceImage(imgs, blinkIDs, state.ReferenceBlinkID); refImg != nil {
		status.SetText("Reference star model: " + c.statusFor(refImg))
	}
	help := widget.NewLabel("Star footprints come from the reference source's reviewed star map (Mosaic > Star Map). In each footprint the stellar light above the local background is moved toward a neutral level in the selected output channels, fading with the footprint; the background keeps its color. Artistic only: stars are not all white. Exclude a star by rejecting it in the map review.")
	help.Wrapping = fyne.TextWrapWord
	content := container.NewVBox(
		enabled,
		container.NewGridWithColumns(2, widget.NewLabel("Reference source (star map)"), reference),
		container.NewGridWithColumns(2, widget.NewLabel("Strength (0–1)"), strength),
		container.NewGridWithColumns(2, widget.NewLabel("Neutral level"), level),
		container.NewGridWithColumns(4, widget.NewLabel("Apply to"), red, green, blue),
		status,
		help,
	)
	d := dialog.NewCustomConfirm("White Stars", "Apply", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(strength.Text), 64)
		if err != nil || v < 0 || v > 1 || v != v {
			dialog.ShowError(fmt.Errorf("strength must be a number from 0 to 1"), win)
			return
		}
		if enabled.Checked && !red.Checked && !green.Checked && !blue.Checked {
			dialog.ShowError(fmt.Errorf("select at least one output channel"), win)
			return
		}
		lvl := models.StarWhiteningWhite
		if level.SelectedIndex() == 1 {
			lvl = models.StarWhiteningLuminance
		}
		*state = models.StarWhiteningState{Enabled: enabled.Checked, ReferenceBlinkID: ids[reference.SelectedIndex()], Strength: v, Level: lvl, Red: red.Checked, Green: green.Checked, Blue: blue.Checked}
		refresh()
	}, win)
	d.Show()
}

func whiteningReferenceImage(imgs []*models.LoadedImage, blinkIDs []string, id string) *models.LoadedImage {
	if id == "" {
		return nil
	}
	for i, img := range imgs {
		if img != nil && i < len(blinkIDs) && blinkIDs[i] == id {
			return img
		}
	}
	return nil
}

// showComposeStarTreatmentDialog lets the user enable the gentler star
// stretch per loaded source with a strength. Sources are listed with their
// composite role so the user can pick the red-contributing ones; nothing is
// inferred from filter names.
func showComposeStarTreatmentDialog(win fyne.Window, imgs []*models.LoadedImage, roles []string, blinkIDs []string, geometry *string, c *composeStarTreatments, refresh func()) {
	type row struct {
		img      *models.LoadedImage
		enabled  *widget.Check
		strength *widget.Entry
	}
	var rows []row
	form := container.NewVBox()
	for i, img := range imgs {
		if img == nil || strings.TrimSpace(img.Path) == "" {
			continue
		}
		role := ""
		if i < len(roles) {
			role = roles[i]
		}
		enabled := widget.NewCheck("Gentler star stretch", nil)
		enabled.SetChecked(img.StarStretch.Enabled)
		strength := widget.NewEntry()
		s := img.StarStretch.Strength
		if s <= 0 {
			s = .75
		}
		strength.SetText(strconv.FormatFloat(s, 'f', 2, 64))
		status := widget.NewLabel(c.statusFor(img))
		status.Wrapping = fyne.TextWrapWord
		name := widget.NewLabel(fmt.Sprintf("Channel %d — %s", i+1, role))
		name.TextStyle.Bold = true
		form.Add(name)
		form.Add(widget.NewLabel(img.Path))
		form.Add(container.NewGridWithColumns(3, enabled, widget.NewLabel("Strength (0–1)"), strength))
		form.Add(status)
		form.Add(widget.NewSeparator())
		rows = append(rows, row{img: img, enabled: enabled, strength: strength})
	}
	if len(rows) == 0 {
		dialog.ShowInformation("Gentler Star Stretch", "No loaded Compose source has an original FITS path.", win)
		return
	}
	geometryIDs := []string{""}
	geometryLabels := []string{"Each source's own star map"}
	geometrySelected := 0
	for i, img := range imgs {
		if img == nil || strings.TrimSpace(img.Path) == "" || i >= len(blinkIDs) || blinkIDs[i] == "" {
			continue
		}
		role := ""
		if i < len(roles) {
			role = roles[i]
		}
		if blinkIDs[i] == *geometry {
			geometrySelected = len(geometryIDs)
		}
		geometryIDs = append(geometryIDs, blinkIDs[i])
		geometryLabels = append(geometryLabels, fmt.Sprintf("Channel %d — %s map for every source", i+1, role))
	}
	geometrySelect := widget.NewSelect(geometryLabels, nil)
	geometrySelect.SetSelectedIndex(geometrySelected)
	geometryHelp := widget.NewLabel("With a shared map, every treated source uses that source's star footprints (core, feather, halo, spikes) and only measures its own star backgrounds, so all channels are compressed over exactly the same area. Sources must share the reference grid.")
	geometryHelp.Wrapping = fyne.TextWrapWord
	help := widget.NewLabel("Treated sources compress accepted stars from the reviewed star map (Mosaic > Star Map) before the composite is mixed. Strength 0 reproduces the ordinary stretch. Exclude a star by rejecting it in the map review. Preparation runs in the background and repeats when a source's stretch settings change.")
	help.Wrapping = fyne.TextWrapWord
	content := container.NewVBox(container.NewGridWithColumns(2, widget.NewLabel("Star geometry"), geometrySelect), geometryHelp, widget.NewSeparator(), form, help)
	scroll := container.NewVScroll(content)
	scroll.SetMinSize(fyne.NewSize(560, 380))
	d := dialog.NewCustomConfirm("Gentler Star Stretch", "Apply", "Cancel", scroll, func(ok bool) {
		if !ok {
			return
		}
		for _, r := range rows {
			strength, err := strconv.ParseFloat(strings.TrimSpace(r.strength.Text), 64)
			if err != nil || strength < 0 || strength > 1 || strength != strength {
				dialog.ShowError(fmt.Errorf("strength for %s must be a number from 0 to 1", r.img.Path), win)
				return
			}
			r.img.StarStretch = models.StarStretchState{Enabled: r.enabled.Checked, Strength: strength}
		}
		*geometry = geometryIDs[geometrySelect.SelectedIndex()]
		c.sync(imgs, true)
		refresh()
	}, win)
	d.Show()
}

// whitenComposeRGBA applies White Stars to a composed RGBA buffer when the
// whitening is available, recomputing the RGB histogram afterwards.
func whitenComposeRGBA(ctx context.Context, build func(w, h int) (*processing.StarNeutralizer, error), b []byte, w, h int, s [3]histogram.Stats) ([]byte, [3]histogram.Stats, error) {
	if b == nil {
		return b, s, nil
	}
	n, err := build(w, h)
	if err != nil {
		return nil, s, err
	}
	if n == nil {
		return b, s, nil
	}
	if err := n.ApplyRGBA(ctx, b); err != nil {
		return nil, s, err
	}
	return b, processing.HistogramRGB(b), nil
}
