package ui

import (
	"image/color"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

func TestComposeWeightSourceFromImageUsesLoadedHeadersAndNilPixels(t *testing.T) {
	img := &models.LoadedImage{
		Primary: fitsio.Header{Cards: map[string]string{"TELESCOP": "'JWST'", "INSTRUME": "'NIRCam'", "FILTER": "'F150W'"}},
		HDU:     fitsio.HDU{Header: fitsio.Header{Cards: map[string]string{"PUPIL": "'F162M'"}}},
	}
	got := composeWeightSourceFromImage("overlay-7", "Narrow", models.ComposeMixWeight{Red: 1}, img)
	if got.ID != "overlay-7" || got.FilterName != "F162M" || got.WavelengthNm != 1620 || got.BandClass != fitsio.FilterBandMedium {
		t.Fatalf("source metadata = %#v", got)
	}
	if got.Label != "Narrow — F162M" || got.Origin != "filter name" {
		t.Fatalf("source presentation = %#v", got)
	}
	if img.HDU.Data.Pixels != nil {
		t.Fatal("metadata source builder retained pixels")
	}
}

func TestComposeWeightSourceFromImageAllowsManualLongpassRecovery(t *testing.T) {
	img := &models.LoadedImage{Primary: fitsio.Header{Cards: map[string]string{"TELESCOP": "'JWST'", "FILTER": "'F100LP'"}}}
	got := composeWeightSourceFromImage("channel-1", "Channel 1", models.ComposeMixWeight{Blue: 1}, img)
	if got.BandClass != fitsio.FilterBandLongpass || got.WavelengthNm != 0 || got.UnresolvedReason == "" {
		t.Fatalf("long-pass metadata = %#v", got)
	}
	if parseWavelengthBandClass("l/lp") != fitsio.FilterBandLongpass || wavelengthBandClassLabel(fitsio.FilterBandUnknown) != "Unknown" {
		t.Fatal("manual band class conversion failed")
	}
}

func TestComposeWavelengthCandidatesUsesTemporaryRGBSnapshot(t *testing.T) {
	sources := []composeWeightSource{{ID: "blue"}, {ID: "green"}, {ID: "excluded"}}
	weights := []models.ComposeMixWeight{{BlinkID: "blue", Blue: 1}, {BlinkID: "green", Green: 1}, {BlinkID: "excluded"}}
	candidates, err := composeWavelengthCandidates(sources, weights, []float64{445, 550, 0}, []fitsio.FilterBandClass{fitsio.FilterBandWide, fitsio.FilterBandWide, fitsio.FilterBandUnknown})
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates = %#v, err = %v", candidates, err)
	}
	weights[0] = models.ComposeMixWeight{BlinkID: "blue"}
	if candidates, err = composeWavelengthCandidates(sources, weights, []float64{0, 550, 600}, []fitsio.FilterBandClass{fitsio.FilterBandUnknown, fitsio.FilterBandWide, fitsio.FilterBandWide}); err != nil || len(candidates) != 1 || candidates[0].BlinkID != "green" {
		t.Fatalf("zeroed exclusion candidates = %#v, err = %v", candidates, err)
	}
	if _, err := composeWavelengthCandidates(sources, weights, []float64{0, 550, 600}, []fitsio.FilterBandClass{fitsio.FilterBandUnknown, fitsio.FilterBandWide, fitsio.FilterBandWide}); err != nil {
		t.Fatalf("excluded invalid metadata should not fail: %v", err)
	}
}

func TestApplyGeneratedWavelengthWeightsPreservesExcludedRows(t *testing.T) {
	current := []models.ComposeMixWeight{{BlinkID: "blue", Blue: 1}, {BlinkID: "excluded"}}
	generated := []models.ComposeMixWeight{{BlinkID: "blue", Red: .2, Blue: .8}}
	got := applyGeneratedWavelengthWeights(current, generated)
	if len(got) != 2 || got[0].Red != .2 || !composeMixWeightDisabled(got[1]) {
		t.Fatalf("applied weights = %#v, want generated active and excluded zero", got)
	}
	if current[0].Red != 0 {
		t.Fatal("weight application mutated the source snapshot")
	}
}

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
	upsertComposeMixWeight(&weights, models.ComposeMixWeight{BlinkID: "overlay-1"})
	if len(weights) != 2 || !composeMixWeightDisabled(weights[0]) {
		t.Fatalf("explicit disabled weight = %#v", weights)
	}
}

func TestMagicBlackCustomColorDoesNotPersistInvalidWeight(t *testing.T) {
	weights := []models.ComposeMixWeight{{BlinkID: "overlay-1", Red: 1}}
	black := composeWeightForColor("overlay-1", color.NRGBA{A: 255}, 1)
	upsertComposeMixWeight(&weights, black)
	if len(weights) != 1 || !composeMixWeightDisabled(weights[0]) {
		t.Fatalf("black custom color weights = %#v, want explicit disabled row", weights)
	}
	project := models.ComposeProject{CompositionMode: models.ComposeModeAuto, MixWeights: weights}
	if got := project.ResolveComposeMode(4); got != models.ComposeModeWeighted {
		t.Fatalf("four-source Auto mode = %q, want weighted", got)
	}
	if err := project.ValidateMixWeights(); err != nil {
		t.Fatalf("black custom color left invalid project weights: %v", err)
	}
}

func TestApplyWidebandComposeMixPresetUsesSymmetricBaseFormulaAndPreservesOverlays(t *testing.T) {
	mode := models.ComposeModeArtistic
	overlay := models.ComposeMixWeight{BlinkID: "overlay-7", Red: .2, Green: .3, Blue: .4}
	got, err := applyWidebandComposeMixPreset(&mode, []models.ComposeMixWeight{
		{BlinkID: models.ComposeChannel1BlinkID, Red: 1},
		{BlinkID: models.ComposeChannel2BlinkID, Blue: 1},
		{BlinkID: models.ComposeChannel3BlinkID, Green: 1},
		overlay,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]models.ComposeMixWeight{
		models.ComposeChannel1BlinkID: {Green: .08, Blue: .92},
		models.ComposeChannel2BlinkID: {Red: .08, Green: .84, Blue: .08},
		models.ComposeChannel3BlinkID: {Red: .92, Green: .08},
	}
	for _, weight := range got {
		if expected, ok := want[weight.BlinkID]; ok {
			if weight.Red != expected.Red || weight.Green != expected.Green || weight.Blue != expected.Blue {
				t.Errorf("%s = %#v, want %#v", weight.BlinkID, weight, expected)
			}
		}
	}
	if got[3] != overlay {
		t.Fatalf("overlay weight = %#v, want %#v", got[3], overlay)
	}
	if mode != models.ComposeModeWeighted {
		t.Fatalf("mode = %q, want explicit weighted mode", mode)
	}
}

func TestApplyWidebandComposeMixPresetValidatesPercentage(t *testing.T) {
	for _, percent := range []float64{-0.01, 50.01} {
		mode := models.ComposeModeArtistic
		original := []models.ComposeMixWeight{{BlinkID: models.ComposeChannel1BlinkID, Blue: 1}}
		if _, err := applyWidebandComposeMixPreset(&mode, original, percent); err == nil {
			t.Errorf("percentage %v succeeded, want validation error", percent)
		}
		if mode != models.ComposeModeArtistic {
			t.Errorf("invalid percentage %v changed mode to %q", percent, mode)
		}
	}
	if _, err := applyWidebandComposeMixPreset(nil, nil, 50); err != nil {
		t.Fatalf("50%% should be accepted: %v", err)
	}
}
