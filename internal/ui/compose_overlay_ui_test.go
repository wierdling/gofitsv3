package ui

import (
	"testing"

	"gofitsv3/internal/models"
)

func TestComposeFreeOverlaySlot(t *testing.T) {
	ws := &composeWorkspace{
		imgs:       make([]*models.LoadedImage, 3, 3+maxOverlayLayers),
		origPixels: make([][]float32, 3, 3+maxOverlayLayers),
	}

	idx, ok := ws.freeOverlaySlot()
	if !ok || idx != 3 {
		t.Fatalf("first slot = %d,%v, want 3,true", idx, ok)
	}
	if len(ws.imgs) != 4 || len(ws.origPixels) != 4 {
		t.Fatalf("slices not grown in step: imgs=%d origPixels=%d", len(ws.imgs), len(ws.origPixels))
	}

	// Slot 3 is taken by a layer; an empty, unowned slot 4 should be reused.
	ws.overlayLayers = []*overlayLayer{{idx: 3}}
	ws.imgs = append(ws.imgs, nil)
	ws.origPixels = append(ws.origPixels, nil)
	if idx, ok := ws.freeOverlaySlot(); !ok || idx != 4 {
		t.Fatalf("reuse slot = %d,%v, want 4,true", idx, ok)
	}

	// Fill up to the limit.
	for len(ws.imgs) < 3+maxOverlayLayers {
		ws.imgs = append(ws.imgs, &models.LoadedImage{})
		ws.origPixels = append(ws.origPixels, nil)
	}
	ws.imgs[4] = &models.LoadedImage{}
	if _, ok := ws.freeOverlaySlot(); ok {
		t.Fatal("expected no free slot at the overlay limit")
	}
}

func TestComposeLayerViewsAndControls(t *testing.T) {
	ws := &composeWorkspace{}
	l := &overlayLayer{idx: 4, viewport: &viewport{}, control: &models.ChannelControl{}}
	views := ws.layerViews(l)
	if len(views) != 5 || views[4] != l.viewport || views[0] != nil {
		t.Errorf("layerViews = %v, want layer viewport only at index 4", views)
	}
	controls := ws.layerControls(l)
	if len(controls) != 5 || controls[4] != l.control || controls[3] != nil {
		t.Errorf("layerControls = %v, want layer control only at index 4", controls)
	}
}
