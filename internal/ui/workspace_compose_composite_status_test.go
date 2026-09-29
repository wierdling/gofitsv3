package ui

import (
	"encoding/binary"
	"math"
	"testing"

	"gofitsv3/internal/models"
)

func TestComposeCompositeDisabledStatusExplainsDisabledToggle(t *testing.T) {
	imgs := []*models.LoadedImage{{}, {}, {}}
	if got, want := composeCompositeDisabledStatus(false, imgs), "Composite disabled — enable Build color composite"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

func TestTransformedComposeOverlaySourceUsesRenderedImageAndSettings(t *testing.T) {
	original := &models.LoadedImage{}
	transformed := &models.LoadedImage{}
	layer := &overlayLayer{idx: 3, settings: models.OrangeLayerState{BlinkID: "overlay-7", Opacity: .4}}
	sources := []*models.LoadedImage{nil, nil, nil, transformed}
	got, ok := transformedComposeOverlaySource(sources, layer)
	if !ok || got.Image != transformed || got.Settings.BlinkID != "overlay-7" || got.Settings.Opacity != .4 {
		t.Fatalf("overlay source = %#v, ok=%v; want transformed source and settings", got, ok)
	}
	if original == got.Image {
		t.Fatal("overlay source unexpectedly used original image")
	}
	if _, ok := transformedComposeOverlaySource(sources, &overlayLayer{idx: 4}); ok {
		t.Fatal("out-of-range overlay source succeeded")
	}
}

func TestArtisticComposeOverlaySourceUsesRenderedImage(t *testing.T) {
	rendered := &models.LoadedImage{}
	transformed := &models.LoadedImage{}
	layer := &overlayLayer{idx: 3, settings: models.OrangeLayerState{BlinkID: "overlay-rendered"}}
	got, ok := artisticComposeOverlaySource([]*models.LoadedImage{nil, nil, nil, rendered}, layer)
	if !ok || got.Image != rendered {
		t.Fatal("artistic overlay source did not use the rendered image")
	}
	weighted, ok := transformedComposeOverlaySource([]*models.LoadedImage{nil, nil, nil, transformed}, layer)
	if !ok || weighted.Image != transformed || weighted.Image == got.Image {
		t.Fatal("weighted and artistic overlay source collections were not separated")
	}
}

func TestComposeOverlaySourcesKeepsArtisticOverlaysOnRenderedGrid(t *testing.T) {
	rendered := &models.LoadedImage{}
	weighted := &models.LoadedImage{}
	layer := &overlayLayer{idx: 3}
	renderedSources := []*models.LoadedImage{nil, nil, nil, rendered}
	weightedSources := []*models.LoadedImage{nil, nil, nil, weighted}

	gotWeighted, gotArtistic := composeOverlaySources(renderedSources, weightedSources, []*overlayLayer{layer}, func(*overlayLayer) bool { return true })
	if len(gotWeighted) != 1 || gotWeighted[0].Image != weighted {
		t.Fatalf("weighted overlays = %#v, want PSF-matched source", gotWeighted)
	}
	if len(gotArtistic) != 1 || gotArtistic[0].Image != rendered {
		t.Fatalf("artistic overlays = %#v, want rendered aligned source", gotArtistic)
	}
}

func TestApplyCrossChannelReplacementRowReplacesOnlyMarkedPixels(t *testing.T) {
	row := []float32{1, 2, 3}
	replacements := make([]byte, 12)
	binary.LittleEndian.PutUint32(replacements[4:], math.Float32bits(9.5))
	if !applyCrossChannelReplacementRow(row, replacements, []byte{0, 1, 0}) {
		t.Fatal("replacement row was not marked dirty")
	}
	if row[0] != 1 || row[1] != 9.5 || row[2] != 3 {
		t.Fatalf("row = %v, want [1 9.5 3]", row)
	}
}

func TestCrossChannelCleanReplayRowsPerBatchCapsAt100MiB(t *testing.T) {
	if got, want := crossChannelCleanReplayRowsPerBatch(1024, 1_000_000), 25_600; got != want {
		t.Fatalf("rows = %d, want %d", got, want)
	}
	if got := crossChannelCleanReplayRowsPerBatch(crossChannelCleanReplayBatchBytes, 10); got != 1 {
		t.Fatalf("wide image rows = %d, want 1", got)
	}
}

func TestComposeCompositeDisabledStatusReportsMissingChannel(t *testing.T) {
	imgs := []*models.LoadedImage{{}, {}, nil}
	if got, want := composeCompositeDisabledStatus(true, imgs), "Composite disabled until all channels are loaded"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

func TestComposeCompositeDisabledStatusDoesNotReportMissingChannelsWhenToggleIsOff(t *testing.T) {
	imgs := []*models.LoadedImage{{}, {}, nil}
	if got, want := composeCompositeDisabledStatus(false, imgs), "Composite disabled — enable Build color composite"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}
