package processing

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

func TestGenerateWavelengthMixWeightsEvenlySpaced(t *testing.T) {
	result, err := GenerateWavelengthMixWeights([]WavelengthMixSource{
		{BlinkID: "blue", WavelengthNm: 450, BandClass: fitsio.FilterBandWide},
		{BlinkID: "green", WavelengthNm: 550, BandClass: fitsio.FilterBandWide},
		{BlinkID: "red", WavelengthNm: 650, BandClass: fitsio.FilterBandWide},
	}, WavelengthMixOptions{CrossMixPercent: 8, NarrowbandAccentPercent: 25})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][3]float64{"blue": {0, .08, .92}, "green": {.08, .84, .08}, "red": {.92, .08, 0}} {
		got := weightByID(result.Weights, id)
		if !closeFloat(got.Red, want[0]) || !closeFloat(got.Green, want[1]) || !closeFloat(got.Blue, want[2]) {
			t.Errorf("weight %q = %#v, want %#v", got.BlinkID, got, want)
		}
	}
}

func TestGenerateWavelengthMixWeightsUnequalSpacingAndNeutralColumns(t *testing.T) {
	result, err := GenerateWavelengthMixWeights([]WavelengthMixSource{
		{BlinkID: "f445", WavelengthNm: 445, BandClass: fitsio.FilterBandWide},
		{BlinkID: "f550", WavelengthNm: 550, BandClass: fitsio.FilterBandWide},
		{BlinkID: "f600", WavelengthNm: 600, BandClass: fitsio.FilterBandWide},
	}, WavelengthMixOptions{CrossMixPercent: 8, NarrowbandAccentPercent: 25})
	if err != nil {
		t.Fatal(err)
	}
	if result.Weights[1].Red <= result.Weights[1].Blue {
		t.Fatalf("middle filter should be closer to red: %#v", result.Weights[1])
	}
	var columns [3]float64
	for _, weight := range result.Weights {
		columns[0] += weight.Red
		columns[1] += weight.Green
		columns[2] += weight.Blue
	}
	for i, sum := range columns {
		if !closeFloat(sum, 1) {
			t.Errorf("column %d sum = %v, want 1", i, sum)
		}
	}
}

func TestGenerateWavelengthMixWeightsNarrowAccentIsAggregate(t *testing.T) {
	sources := []WavelengthMixSource{
		{BlinkID: "w1", WavelengthNm: 450, BandClass: fitsio.FilterBandWide},
		{BlinkID: "w2", WavelengthNm: 650, BandClass: fitsio.FilterBandWide},
		{BlinkID: "n1", WavelengthNm: 600, BandClass: fitsio.FilterBandNarrow},
		{BlinkID: "n2", WavelengthNm: 610, BandClass: fitsio.FilterBandNarrow},
	}
	result, err := GenerateWavelengthMixWeights(sources, WavelengthMixOptions{CrossMixPercent: 8, NarrowbandAccentPercent: 25})
	if err != nil {
		t.Fatal(err)
	}
	var sums [3]float64
	for _, weight := range result.Weights {
		if weight.BlinkID == "n1" || weight.BlinkID == "n2" {
			sums[0] += weight.Red
			sums[1] += weight.Green
			sums[2] += weight.Blue
		}
	}
	if math.Max(sums[0], math.Max(sums[1], sums[2])) > .25+1e-12 {
		t.Fatalf("narrow accent exceeded cap: %v", sums)
	}
}

func TestGenerateWavelengthMixWeightsPermutationInvariant(t *testing.T) {
	a := []WavelengthMixSource{{BlinkID: "b", WavelengthNm: 400, BandClass: fitsio.FilterBandWide}, {BlinkID: "n1", WavelengthNm: 580, BandClass: fitsio.FilterBandNarrow}, {BlinkID: "n2", WavelengthNm: 600, BandClass: fitsio.FilterBandNarrow}, {BlinkID: "r", WavelengthNm: 800, BandClass: fitsio.FilterBandWide}}
	b := []WavelengthMixSource{a[3], a[1], a[0], a[2]}
	ra, err := GenerateWavelengthMixWeights(a, WavelengthMixOptions{CrossMixPercent: 12, NarrowbandAccentPercent: 30})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := GenerateWavelengthMixWeights(b, WavelengthMixOptions{CrossMixPercent: 12, NarrowbandAccentPercent: 30})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b", "n1", "n2", "r"} {
		if weightByID(ra.Weights, id) != weightByID(rb.Weights, id) {
			t.Fatalf("permutation changed %q: %#v vs %#v", id, ra.Weights, rb.Weights)
		}
	}
	var narrowMax float64
	for _, id := range []string{"n1", "n2"} {
		weight := weightByID(ra.Weights, id)
		narrowMax = math.Max(narrowMax, math.Max(weight.Red, math.Max(weight.Green, weight.Blue)))
	}
	if narrowMax > .3+1e-12 {
		t.Fatalf("narrow accent exceeded shared cap: %v", narrowMax)
	}
}

func TestGenerateWavelengthMixWeightsPromotesInsufficientBase(t *testing.T) {
	result, err := GenerateWavelengthMixWeights([]WavelengthMixSource{
		{BlinkID: "n1", WavelengthNm: 500, BandClass: fitsio.FilterBandNarrow},
		{BlinkID: "n2", WavelengthNm: 700, BandClass: fitsio.FilterBandNarrow},
	}, WavelengthMixOptions{CrossMixPercent: 8, NarrowbandAccentPercent: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected insufficient-base warning")
	}
	for _, weight := range result.Weights {
		if weight.Red == 0 && weight.Green == 0 && weight.Blue == 0 {
			t.Errorf("promoted source has no contribution: %#v", weight)
		}
	}
}

func TestGenerateWavelengthMixWeightsPromotesWAndN(t *testing.T) {
	result, err := GenerateWavelengthMixWeights([]WavelengthMixSource{
		{BlinkID: "w", WavelengthNm: 500, BandClass: fitsio.FilterBandWide},
		{BlinkID: "n", WavelengthNm: 700, BandClass: fitsio.FilterBandNarrow},
	}, WavelengthMixOptions{CrossMixPercent: 8, NarrowbandAccentPercent: 25})
	if err != nil || len(result.Warnings) == 0 {
		t.Fatalf("W/N should be promoted with warning: result=%#v err=%v", result, err)
	}
}

func TestGenerateWavelengthMixWeightsMediumAndLongpassAreBase(t *testing.T) {
	for name, classes := range map[string][]fitsio.FilterBandClass{
		"W/W/M": {fitsio.FilterBandWide, fitsio.FilterBandWide, fitsio.FilterBandMedium},
		"L/W/W": {fitsio.FilterBandLongpass, fitsio.FilterBandWide, fitsio.FilterBandWide},
	} {
		result, err := GenerateWavelengthMixWeights([]WavelengthMixSource{
			{BlinkID: "a", WavelengthNm: 400, BandClass: classes[0]},
			{BlinkID: "b", WavelengthNm: 500, BandClass: classes[1]},
			{BlinkID: "c", WavelengthNm: 700, BandClass: classes[2]},
		}, WavelengthMixOptions{CrossMixPercent: 8, NarrowbandAccentPercent: 25})
		if err != nil || len(result.Weights) != 3 || len(result.Warnings) != 0 {
			t.Errorf("%s should treat every source as base: result=%#v err=%v", name, result, err)
		}
		for channel, want := range []float64{1, 1, 1} {
			var sum float64
			for _, weight := range result.Weights {
				sum += []float64{weight.Red, weight.Green, weight.Blue}[channel]
			}
			if !closeFloat(sum, want) {
				t.Errorf("%s column %d = %v, want %v", name, channel, sum, want)
			}
		}
	}
}

func TestGenerateWavelengthMixWeightsTwoSourceSparseMix(t *testing.T) {
	result, err := GenerateWavelengthMixWeights([]WavelengthMixSource{
		{BlinkID: "blue", WavelengthNm: 400}, {BlinkID: "red", WavelengthNm: 800},
	}, WavelengthMixOptions{CrossMixPercent: 0, NarrowbandAccentPercent: 25})
	if err != nil || len(result.Weights) != 2 {
		t.Fatalf("two-source mapping failed: %#v err=%v", result, err)
	}
	for _, weight := range result.Weights {
		if math.IsNaN(weight.Red) || math.IsNaN(weight.Green) || math.IsNaN(weight.Blue) {
			t.Fatalf("sparse mapping emitted NaN: %#v", weight)
		}
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected sparse-column warning")
	}
}

func TestGenerateWavelengthMixWeightsAccentHundred(t *testing.T) {
	result, err := GenerateWavelengthMixWeights([]WavelengthMixSource{
		{BlinkID: "w1", WavelengthNm: 400, BandClass: fitsio.FilterBandWide},
		{BlinkID: "w2", WavelengthNm: 800, BandClass: fitsio.FilterBandWide},
		{BlinkID: "n", WavelengthNm: 600, BandClass: fitsio.FilterBandNarrow},
	}, WavelengthMixOptions{NarrowbandAccentPercent: 100})
	if err != nil {
		t.Fatal(err)
	}
	var max float64
	weight := weightByID(result.Weights, "n")
	max = math.Max(max, math.Max(weight.Red, math.Max(weight.Green, weight.Blue)))
	if !closeFloat(max, 1) {
		t.Fatalf("100%% accent maximum = %v, want 1", max)
	}
}

func TestGenerateWavelengthMixWeightsValidation(t *testing.T) {
	valid := []WavelengthMixSource{{BlinkID: "a", WavelengthNm: 400}, {BlinkID: "b", WavelengthNm: 500}}
	for name, options := range map[string]WavelengthMixOptions{
		"cross low":   {CrossMixPercent: -1, NarrowbandAccentPercent: 25},
		"cross high":  {CrossMixPercent: 51, NarrowbandAccentPercent: 25},
		"accent low":  {CrossMixPercent: 8, NarrowbandAccentPercent: -1},
		"accent zero": {CrossMixPercent: 8, NarrowbandAccentPercent: 0},
		"accent high": {CrossMixPercent: 8, NarrowbandAccentPercent: 101},
	} {
		if _, err := GenerateWavelengthMixWeights(valid, options); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	for _, sources := range [][]WavelengthMixSource{
		{{BlinkID: "a", WavelengthNm: 400}},
		{{BlinkID: "a", WavelengthNm: 400}, {BlinkID: "a", WavelengthNm: 500}},
		{{BlinkID: "a", WavelengthNm: 0}, {BlinkID: "b", WavelengthNm: 500}},
		{{BlinkID: "a", WavelengthNm: math.NaN()}, {BlinkID: "b", WavelengthNm: 500}},
		{{BlinkID: "a", WavelengthNm: math.Inf(1)}, {BlinkID: "b", WavelengthNm: 500}},
		{{BlinkID: "a", WavelengthNm: 400}, {BlinkID: "b", WavelengthNm: 400}},
	} {
		if _, err := GenerateWavelengthMixWeights(sources, WavelengthMixOptions{CrossMixPercent: 8, NarrowbandAccentPercent: 25}); err == nil {
			t.Errorf("sources %#v: expected validation error", sources)
		}
	}
}

func weightByID(weights []models.ComposeMixWeight, id string) models.ComposeMixWeight {
	for _, weight := range weights {
		if weight.BlinkID == id {
			return weight
		}
	}
	return models.ComposeMixWeight{BlinkID: id}
}

func closeFloat(a, b float64) bool { return math.Abs(a-b) < 1e-12 }
