package ui

import (
	"image/color"
	"testing"

	"gofitsv3/internal/models"
)

func TestNormalizeComposeMixWeightsUsesStableIDsAndDefaults(t *testing.T) {
	sources := []composeWeightSource{
		{ID: models.ComposeChannel1BlinkID, Defaults: models.ComposeMixWeight{Blue: 1}},
		{ID: "overlay-7", Defaults: models.ComposeMixWeight{Red: .5, Green: .2, Blue: .1}},
	}
	got := normalizeComposeMixWeights([]models.ComposeMixWeight{{BlinkID: "overlay-7", Red: 2, Green: 3, Blue: 4}, {BlinkID: "removed", Red: 1}}, sources)
	if len(got) != 2 || got[0].Blue != 1 || got[1].Red != 2 || got[1].Green != 3 || got[1].Blue != 4 {
		t.Fatalf("normalized weights = %#v", got)
	}
}

func TestComposeModeLabelAutoUsesWeightedOnlyForFourSources(t *testing.T) {
	if got := composeModeLabel(models.ComposeModeAuto, 3); got != "Effective mode: Artistic overlays" {
		t.Fatalf("three-source auto label = %q", got)
	}
	if got := composeModeLabel(models.ComposeModeAuto, 4); got != "Effective mode: Weighted multi-channel" {
		t.Fatalf("four-source auto label = %q", got)
	}
	if got := composeModeLabel("", 4); got != "Effective mode: Artistic overlays" {
		t.Fatalf("legacy empty-mode label = %q", got)
	}
}

func TestComposeWeightForColorSeedsCustomColorAndOpacity(t *testing.T) {
	got := composeWeightForColor("custom", color.NRGBA{R: 255, G: 128, B: 0, A: 255}, .5)
	if got.BlinkID != "custom" || got.Red != 0.5 || got.Green != 128.0/255.0*0.5 || got.Blue != 0 {
		t.Fatalf("color weight = %#v", got)
	}
}

func TestComposeMixWeightDisabledRecognizesZeroOpacitySources(t *testing.T) {
	weight := composeWeightForColor("overlay-1", color.NRGBA{R: 255, G: 128, B: 32, A: 255}, 0)
	if !composeMixWeightDisabled(weight) {
		t.Fatalf("zero-opacity weight = %#v, want disabled", weight)
	}
}

func TestNormalizeComposeMixWeightsDoesNotEnableDisabledDefault(t *testing.T) {
	sources := []composeWeightSource{{ID: "overlay-1", Defaults: models.ComposeMixWeight{BlinkID: "overlay-1"}}}
	got := normalizeComposeMixWeights(nil, sources)
	if len(got) != 1 || !composeMixWeightDisabled(got[0]) {
		t.Fatalf("normalized disabled default = %#v", got)
	}
}

func TestUpsertComposeMixWeightReplacesByIdentity(t *testing.T) {
	weights := []models.ComposeMixWeight{{BlinkID: "overlay-1", Red: 1}}
	upsertComposeMixWeight(&weights, models.ComposeMixWeight{BlinkID: "overlay-1", Green: 1})
	upsertComposeMixWeight(&weights, models.ComposeMixWeight{BlinkID: "overlay-2", Blue: 1})
	if len(weights) != 2 || weights[0].Red != 0 || weights[0].Green != 1 || weights[1].BlinkID != "overlay-2" {
		t.Fatalf("upserted weights = %#v", weights)
	}
}

func TestMagicBlackCustomColorDoesNotPersistInvalidWeight(t *testing.T) {
	weights := []models.ComposeMixWeight{{BlinkID: "overlay-1", Red: 1}}
	black := composeWeightForColor("overlay-1", color.NRGBA{A: 255}, 1)
	upsertComposeMixWeight(&weights, black)
	if len(weights) != 0 {
		t.Fatalf("black custom color persisted weights = %#v, want omitted", weights)
	}
	project := models.ComposeProject{CompositionMode: models.ComposeModeAuto, MixWeights: weights}
	if got := project.ResolveComposeMode(4); got != models.ComposeModeWeighted {
		t.Fatalf("four-source Auto mode = %q, want weighted", got)
	}
	if err := project.ValidateMixWeights(); err != nil {
		t.Fatalf("black custom color left invalid project weights: %v", err)
	}
}
