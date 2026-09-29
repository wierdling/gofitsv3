package ui

import (
	"fmt"
	"image/color"

	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

func (ws *composeWorkspace) whiteningReference() *models.LoadedImage {
	if !ws.starWhitening.Enabled {
		return nil
	}
	return whiteningReferenceImage(ws.imgs, ws.composeSourceBlinkIDs(), ws.starWhitening.ReferenceBlinkID)
}

// buildStarNeutralizer returns the whitening for a composite of size w x h,
// nil when whitening is off or its reference model is not prepared yet
// (the render then proceeds without it and refreshes when the model lands).
func (ws *composeWorkspace) buildStarNeutralizer(w, h int) (*processing.StarNeutralizer, error) {
	ref := ws.whiteningReference()
	if ref == nil || len(ws.imgs) < 2 || ws.imgs[1] == nil {
		return nil, nil
	}
	model := ws.starTreatments.modelFor(ref)
	if model == nil {
		return nil, nil
	}
	idx := -1
	for i, img := range ws.imgs {
		if img == ref {
			idx = i
		}
	}
	dx, dy, rot, _ := ws.composeChannelOffsetFields(idx)
	return processing.NewStarNeutralizer(model, processing.DiskChannel{Image: *ref, OffsetX: dx, OffsetY: dy, OffsetRot: rot}, *ws.imgs[1], w, h, ws.starWhitening)
}

func (ws *composeWorkspace) composeWeightSources() []composeWeightSource {
	sources := make([]composeWeightSource, 0, 3+len(ws.overlayLayers))
	if ws.imgs[0] != nil {
		sources = append(sources, composeWeightSourceFromImage(models.ComposeChannel1BlinkID, "Channel 1 (Blue)", models.ComposeMixWeight{Blue: 1}, ws.imgs[0]))
	}
	if ws.imgs[1] != nil {
		sources = append(sources, composeWeightSourceFromImage(models.ComposeChannel2BlinkID, "Channel 2 (Green)", models.ComposeMixWeight{Green: 1}, ws.imgs[1]))
	}
	if ws.imgs[2] != nil {
		sources = append(sources, composeWeightSourceFromImage(models.ComposeChannel3BlinkID, "Channel 3 (Red)", models.ComposeMixWeight{Red: 1}, ws.imgs[2]))
	}
	for _, layer := range ws.overlayLayers {
		if layer == nil || layer.idx >= len(ws.imgs) || ws.imgs[layer.idx] == nil {
			continue
		}
		id := layer.settings.BlinkID
		if id == "" {
			id = fmt.Sprintf("overlay-%d", layer.idx-2)
		}
		defaults := composeWeightForColor(id, color.NRGBA{R: layer.settings.ColorR, G: layer.settings.ColorG, B: layer.settings.ColorB, A: 255}, layer.settings.Opacity)
		sources = append(sources, composeWeightSourceFromImage(id, layer.name, defaults, ws.imgs[layer.idx]))
	}
	return sources
}

func (ws *composeWorkspace) showComposeWeights() {
	sources := ws.composeWeightSources()
	showComposeWeightsDialog(ws.win, &ws.compositionMode, &ws.mixWeights, sources, ws.refresh)
}
