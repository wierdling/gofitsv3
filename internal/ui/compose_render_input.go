package ui

import (
	"maps"

	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// composeRenderOffset is one slot's Manual Offset, read from its widgets on
// the UI goroutine.
type composeRenderOffset struct {
	dx, dy, rot float64
	ok          bool
}

// composeRenderOverlay is the render-relevant state of one overlay layer.
// active records whether the layer window is open (only open layers render).
type composeRenderOverlay struct {
	idx      int
	settings models.OrangeLayerState
	active   bool
}

// composeRenderWhitening carries what processing.NewStarNeutralizer needs, so
// the render goroutine never touches ws.starTreatments or the reference
// channel's widgets.
type composeRenderWhitening struct {
	model  *processing.StarTreatmentModel
	ref    processing.DiskChannel
	grid   models.LoadedImage
	state  models.StarWhiteningState
	refIdx int
}

// composeRenderInput is an immutable snapshot of everything composeRGB reads.
// snapshotRenderInput builds it on the UI goroutine; render goroutines read
// only this, never composeWorkspace fields or widgets.
//
// imgs holds per-render shallow copies of the workspace images, so UI handlers
// such as Apply/Auto that write stretch fields in place after the snapshot do
// not change an in-flight render. Pixel slices are shared: they are never
// written in place, only replaced.
type composeRenderInput struct {
	large     bool
	imgs      []*models.LoadedImage
	offsets   []composeRenderOffset
	psf       models.PSFSettings
	mode      models.ComposeMode
	weights   []models.ComposeMixWeight
	overlays  []composeRenderOverlay
	levels    models.RgbLevels
	lrgb      composeLRGBSnapshot
	whitening *composeRenderWhitening

	largeStore     *composeLargeStore
	largeArtifacts map[int]composeArtifactDescriptor
}

// snapshotRenderInput captures the render inputs. It must run on the UI
// goroutine because it reads widget-backed offsets.
func (ws *composeWorkspace) snapshotRenderInput() *composeRenderInput {
	in := &composeRenderInput{
		large:   ws.largeMode,
		imgs:    make([]*models.LoadedImage, len(ws.imgs)),
		offsets: make([]composeRenderOffset, len(ws.imgs)),
		psf:     ws.psfSettings,
		mode:    ws.compositionMode,
		weights: append([]models.ComposeMixWeight(nil), ws.mixWeights...),
	}
	for i, img := range ws.imgs {
		if img != nil {
			c := *img
			in.imgs[i] = &c
		}
		dx, dy, rot, ok := ws.composeChannelOffsetFields(i)
		in.offsets[i] = composeRenderOffset{dx: dx, dy: dy, rot: rot, ok: ok}
	}
	for _, l := range ws.overlayLayers {
		if l != nil {
			in.overlays = append(in.overlays, composeRenderOverlay{idx: l.idx, settings: l.settings, active: l.win != nil})
		}
	}
	if ws.levels != nil {
		in.levels = *ws.levels
	}
	ws.lrgbMu.RLock()
	in.lrgb = composeLRGBSnapshot{settings: ws.lrgbSettings, generation: ws.lrgbGeneration}
	if ws.dedicatedL != nil {
		c := *ws.dedicatedL
		in.lrgb.dedicated = &c
	}
	ws.lrgbMu.RUnlock()
	in.whitening = ws.snapshotWhitening(in)
	if in.large {
		in.largeStore = ws.largeStore
		ws.largeMu.RLock()
		in.largeArtifacts = maps.Clone(ws.largeArtifacts)
		ws.largeMu.RUnlock()
	}
	return in
}

// snapshotWhitening resolves the White Stars inputs against the live images
// (the treatment cache is keyed by workspace pointer), returning nil when
// whitening is off or its model is not prepared yet.
func (ws *composeWorkspace) snapshotWhitening(in *composeRenderInput) *composeRenderWhitening {
	ref := ws.whiteningReference()
	if ref == nil || len(ws.imgs) < 2 || ws.imgs[1] == nil || ws.starTreatments == nil {
		return nil
	}
	model := ws.starTreatments.modelFor(ref)
	if model == nil {
		return nil
	}
	idx := -1
	for i, img := range ws.imgs {
		if img == ref {
			idx = i
		}
	}
	var off composeRenderOffset
	if idx >= 0 && idx < len(in.offsets) {
		off = in.offsets[idx]
	}
	state := ws.starWhitening
	state.ForcedStars = maps.Clone(state.ForcedStars)
	return &composeRenderWhitening{
		model:  model,
		ref:    processing.DiskChannel{Image: *ref, OffsetX: off.dx, OffsetY: off.dy, OffsetRot: off.rot},
		grid:   *ws.imgs[1],
		state:  state,
		refIdx: idx,
	}
}

// buildStarNeutralizer returns the whitening for a composite of size w x h,
// nil when whitening is off or its reference model is not prepared yet.
func (in *composeRenderInput) buildStarNeutralizer(w, h int) (*processing.StarNeutralizer, error) {
	if in == nil || in.whitening == nil {
		return nil, nil
	}
	wh := in.whitening
	return processing.NewStarNeutralizer(wh.model, wh.ref, wh.grid, w, h, wh.state)
}

// offset returns slot idx's Manual Offset from the snapshot.
func (in *composeRenderInput) offset(idx int) composeRenderOffset {
	if idx < 0 || idx >= len(in.offsets) {
		return composeRenderOffset{}
	}
	return in.offsets[idx]
}

// activeOverlayLayers returns detached overlayLayer values (no window or
// widgets) for the overlays whose window was open at snapshot time.
func (in *composeRenderInput) activeOverlayLayers() []*overlayLayer {
	layers := make([]*overlayLayer, 0, len(in.overlays))
	for _, o := range in.overlays {
		if o.active {
			layers = append(layers, &overlayLayer{idx: o.idx, settings: o.settings})
		}
	}
	return layers
}

// hasAllBaseChannels reports whether the snapshot has all three RGB channels.
func (in *composeRenderInput) hasAllBaseChannels() bool {
	return composeHasAllBaseChannels(in.imgs)
}
