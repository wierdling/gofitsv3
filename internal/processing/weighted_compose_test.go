package processing

import (
	"context"
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func TestWeightedComposeRGBOneHotAndMixture(t *testing.T) {
	w := func(id string, r, g, b float64, pixels ...float32) WeightedComposeSource {
		return WeightedComposeSource{BlinkID: id, Pixels: pixels, Weights: models.ComposeMixWeight{BlinkID: id, Red: r, Green: g, Blue: b}}
	}
	got, err := WeightedComposeRGB(context.Background(), []WeightedComposeSource{
		w("a", 1, 0, 0, 1, .5), w("b", 0, 1, 0, 0, 1), w("c", 0, 0, 1, .25, .75), w("d", 1, 1, 1, .5, .5),
	}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got[0][0] != 1 || got[1][0] != .5 || math.Abs(float64(got[2][0]-.6666667)) > 1e-6 || got[0][1] != .75 || got[1][1] != 1 || got[2][1] != 1 {
		t.Fatalf("mixed RGB = %#v, want simultaneous normalized mixture", got)
	}
}

func TestWeightedComposeRGBPermutationInvariantAndPreservesHighlightRatios(t *testing.T) {
	sources := []WeightedComposeSource{
		{BlinkID: "z", Pixels: []float32{4, 8}, Weights: models.ComposeMixWeight{BlinkID: "z", Red: 2, Green: 1, Blue: .5}},
		{BlinkID: "a", Pixels: []float32{4, 8}, Weights: models.ComposeMixWeight{BlinkID: "a", Red: 2, Green: 1, Blue: .5}},
	}
	first, err := WeightedComposeRGB(context.Background(), sources, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WeightedComposeRGB(context.Background(), []WeightedComposeSource{sources[1], sources[0]}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	for c := range first {
		if first[c][0] != second[c][0] {
			t.Fatalf("permutation changed channel %d: %v != %v", c, first[c][0], second[c][0])
		}
	}
	if first[0][0] != 1 || first[1][0] != .5 || first[2][0] != .25 || first[0][1] != 1 || first[1][1] != .5 || first[2][1] != .25 {
		t.Fatalf("highlight mix = %#v, want hue-preserving compressed result", first)
	}
}

func TestWeightedComposeRGBRejectsInvalidInputs(t *testing.T) {
	cases := []WeightedComposeSource{
		{BlinkID: "", Pixels: []float32{1}, Weights: models.ComposeMixWeight{Red: 1}},
		{BlinkID: "a", Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: "a", Red: -1}},
		{BlinkID: "a", Pixels: []float32{float32(math.NaN())}, Weights: models.ComposeMixWeight{BlinkID: "a", Red: 1}},
	}
	for i, source := range cases {
		if i == 0 {
			if _, err := WeightedComposeRGB(context.Background(), []WeightedComposeSource{source}, 1, 1); err == nil {
				t.Fatal("missing source identity succeeded")
			}
			continue
		}
		if _, err := WeightedComposeRGB(context.Background(), []WeightedComposeSource{source}, 1, 1); err == nil {
			t.Fatalf("invalid source %+v succeeded", source)
		}
	}
	if _, err := WeightedComposeRGB(context.Background(), nil, 0, 1); err == nil {
		t.Fatal("invalid dimensions succeeded")
	}
}

func TestWeightedComposeRGBExactFiveSourceMix(t *testing.T) {
	sources := []WeightedComposeSource{
		{BlinkID: "a", Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: "a", Red: .4}},
		{BlinkID: "b", Pixels: []float32{.5}, Weights: models.ComposeMixWeight{BlinkID: "b", Red: 1.6}},
		{BlinkID: "c", Pixels: []float32{.8}, Weights: models.ComposeMixWeight{BlinkID: "c", Green: 1}},
		{BlinkID: "d", Pixels: []float32{.4}, Weights: models.ComposeMixWeight{BlinkID: "d", Blue: 1}},
		{BlinkID: "e", Pixels: []float32{.2}, Weights: models.ComposeMixWeight{BlinkID: "e", Blue: 1}},
	}
	got, err := WeightedComposeRGB(context.Background(), sources, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(got[0][0]-1)) > 1e-6 || math.Abs(float64(got[1][0]-.5)) > 1e-6 || math.Abs(float64(got[2][0]-1)) > 1e-6 {
		t.Fatalf("five-source mix = %#v, want [1 .5 1] after common compression", got)
	}
}

func TestWeightedComposeRGBPartialReorderedStandardIdentities(t *testing.T) {
	sources := []WeightedComposeSource{
		{BlinkID: models.ComposeChannel3BlinkID, Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: models.ComposeChannel3BlinkID, Blue: 1}},
		{BlinkID: models.ComposeChannel1BlinkID, Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: models.ComposeChannel1BlinkID, Red: 1}},
	}
	got, err := WeightedComposeRGB(context.Background(), sources, 1, 1)
	if err != nil || got[0][0] != 1 || got[1][0] != 0 || got[2][0] != 1 {
		t.Fatalf("partial reordered standard mix = %#v, err=%v", got, err)
	}
}

func TestWeightedComposeRGBRejectsCanonicalDuplicateAndMismatch(t *testing.T) {
	duplicate := []WeightedComposeSource{
		{BlinkID: models.ComposeChannel1BlinkID, Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: models.ComposeChannel1BlinkID, Red: 1}},
		{BlinkID: models.ComposeChannel1BlinkID, Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: models.ComposeChannel1BlinkID, Red: 1}},
	}
	if _, err := WeightedComposeRGB(context.Background(), duplicate, 1, 1); err == nil {
		t.Fatal("duplicate canonical identity succeeded")
	}
	mismatch := WeightedComposeSource{BlinkID: models.ComposeChannel1BlinkID, Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: models.ComposeChannel2BlinkID, Red: 1}}
	if _, err := WeightedComposeRGB(context.Background(), []WeightedComposeSource{mismatch}, 1, 1); err == nil {
		t.Fatal("source/weight identity mismatch succeeded")
	}
}

func TestWeightedComposeRGBUsesStableStandardChannelIdentities(t *testing.T) {
	sources := []WeightedComposeSource{
		{Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: models.ComposeChannel1BlinkID, Red: 1}},
		{Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: models.ComposeChannel2BlinkID, Green: 1}},
		{Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: models.ComposeChannel3BlinkID, Blue: 1}},
	}
	got, err := WeightedComposeRGB(context.Background(), sources, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got[0][0] != 1 || got[1][0] != 1 || got[2][0] != 1 {
		t.Fatalf("standard channel identity mix = %#v, want white", got)
	}
}

func TestWeightedComposeRGBAllThreeSourcePermutations(t *testing.T) {
	base := []WeightedComposeSource{
		{BlinkID: "a", Pixels: []float32{.2}, Weights: models.ComposeMixWeight{BlinkID: "a", Red: 1, Green: .2}},
		{BlinkID: "b", Pixels: []float32{.7}, Weights: models.ComposeMixWeight{BlinkID: "b", Green: 1, Blue: .4}},
		{BlinkID: "c", Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: "c", Red: .3, Blue: 1}},
	}
	want, err := WeightedComposeRGB(context.Background(), base, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	perms := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, perm := range perms {
		input := []WeightedComposeSource{base[perm[0]], base[perm[1]], base[perm[2]]}
		got, err := WeightedComposeRGB(context.Background(), input, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		for c := range want {
			if got[c][0] != want[c][0] {
				t.Fatalf("permutation %v changed channel %d: %v != %v", perm, c, got[c][0], want[c][0])
			}
		}
	}
}

func TestComposeWeightedRGBPlanesDistinguishesMissingAndExplicitZeroWeights(t *testing.T) {
	images := make([]*models.LoadedImage, 3)
	for i := range images {
		images[i] = &models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{1}}}, Mode: stretch.Linear, White: 1, Peak: 1, ScaledPeak: 1}
	}
	missing, _, _, err := ComposeWeightedRGBPlanes(context.Background(), images, nil, nil)
	if err != nil || missing[0][0] != 1 || missing[1][0] != 1 || missing[2][0] != 1 {
		t.Fatalf("missing standard weights = %#v, err=%v; want legacy white default", missing, err)
	}
	disabled, _, _, err := ComposeWeightedRGBPlanes(context.Background(), images, nil, []models.ComposeMixWeight{{BlinkID: models.ComposeChannel1BlinkID}})
	if err != nil || disabled[0][0] != 1 || disabled[1][0] != 1 || disabled[2][0] != 0 {
		t.Fatalf("explicit zero standard weight = %#v, err=%v; want blue disabled", disabled, err)
	}
	overlay := &models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{1}}}, Mode: stretch.Linear, White: 1, Peak: 1, ScaledPeak: 1}
	settings := models.OrangeLayerState{BlinkID: "overlay-1", ColorR: 255, Opacity: 1}
	baseWeights := []models.ComposeMixWeight{{BlinkID: models.ComposeChannel3BlinkID}}
	withDefault, _, _, err := ComposeWeightedRGBPlanes(context.Background(), images, []OverlayLayer{{Image: overlay, Settings: settings}}, baseWeights)
	if err != nil || withDefault[0][0] != 1 {
		t.Fatalf("missing overlay weight = %#v, err=%v; want red default", withDefault, err)
	}
	withDisabled, _, _, err := ComposeWeightedRGBPlanes(context.Background(), images, []OverlayLayer{{Image: overlay, Settings: settings}}, append(baseWeights, models.ComposeMixWeight{BlinkID: "overlay-1"}))
	if err != nil || withDisabled[0][0] != 0 {
		t.Fatalf("explicit zero overlay weight = %#v, err=%v; want overlay disabled", withDisabled, err)
	}
}

func TestWeightedComposeRGBCancellationDuringValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WeightedComposeRGB(ctx, []WeightedComposeSource{{BlinkID: "a", Pixels: []float32{1}, Weights: models.ComposeMixWeight{BlinkID: "a", Red: 1}}}, 1, 1)
	if err != context.Canceled {
		t.Fatalf("canceled composition error = %v, want context.Canceled", err)
	}
}
