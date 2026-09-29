package processing

import (
	"fmt"
	"math"
	"sort"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

// WavelengthMixSource describes one included source for the wavelength-aware
// colour mapping preset. The BlinkID is retained on the generated weight.
type WavelengthMixSource struct {
	BlinkID      string
	WavelengthNm float64
	BandClass    fitsio.FilterBandClass
}

// WavelengthMixOptions controls the amount of cross-channel colour and the
// aggregate contribution allowed from narrowband sources.
type WavelengthMixOptions struct {
	CrossMixPercent         float64
	NarrowbandAccentPercent float64
}

// WavelengthMixResult contains stable-ID weights and non-fatal guidance for
// callers displaying the generated preset.
type WavelengthMixResult struct {
	Weights  []models.ComposeMixWeight
	Warnings []string
}

// GenerateWavelengthMixWeights derives a deterministic RGB mapping from
// linearly spaced wavelengths. Continuum sources are column-normalized;
// narrowband sources share one aggregate accent budget.
func GenerateWavelengthMixWeights(sources []WavelengthMixSource, options WavelengthMixOptions) (WavelengthMixResult, error) {
	if len(sources) < 2 {
		return WavelengthMixResult{}, fmt.Errorf("wavelength mapping requires at least two included sources")
	}
	if !finiteNumber(options.CrossMixPercent) || options.CrossMixPercent < 0 || options.CrossMixPercent > 50 {
		return WavelengthMixResult{}, fmt.Errorf("cross-mix percentage must be between 0 and 50")
	}
	if !finiteNumber(options.NarrowbandAccentPercent) || options.NarrowbandAccentPercent < 1 || options.NarrowbandAccentPercent > 100 {
		return WavelengthMixResult{}, fmt.Errorf("narrowband accent percentage must be between 1 and 100")
	}

	ordered := append([]WavelengthMixSource(nil), sources...)
	seen := make(map[string]struct{}, len(ordered))
	for _, source := range ordered {
		if source.BlinkID == "" {
			return WavelengthMixResult{}, fmt.Errorf("wavelength mapping source BlinkID is required")
		}
		if _, ok := seen[source.BlinkID]; ok {
			return WavelengthMixResult{}, fmt.Errorf("duplicate wavelength mapping source BlinkID %q", source.BlinkID)
		}
		seen[source.BlinkID] = struct{}{}
		if !finiteNumber(source.WavelengthNm) || source.WavelengthNm <= 0 {
			return WavelengthMixResult{}, fmt.Errorf("source %q has an invalid wavelength", source.BlinkID)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].WavelengthNm == ordered[j].WavelengthNm {
			return ordered[i].BlinkID < ordered[j].BlinkID
		}
		return ordered[i].WavelengthNm < ordered[j].WavelengthNm
	})
	minWavelength, maxWavelength := ordered[0].WavelengthNm, ordered[len(ordered)-1].WavelengthNm
	if minWavelength == maxWavelength {
		return WavelengthMixResult{}, fmt.Errorf("wavelength mapping requires at least two distinct wavelengths")
	}

	base := make([]bool, len(ordered))
	baseCount := 0
	for i, source := range ordered {
		base[i] = source.BandClass != fitsio.FilterBandNarrow
		if base[i] {
			baseCount++
		}
	}
	warnings := []string{}
	if baseCount < 2 {
		warnings = append(warnings, "fewer than two continuum filters were found; narrowband sources were promoted to the base palette")
		for i := range base {
			base[i] = true
		}
		baseCount = len(base)
	}

	strength := options.CrossMixPercent / 100
	raw := make([][3]float64, len(ordered))
	for i, source := range ordered {
		t := (source.WavelengthNm - minWavelength) / (maxWavelength - minWavelength)
		raw[i] = wavelengthAnchors(t, strength)
	}

	// Normalize each RGB column using only continuum/base sources. A column
	// with no support is intentionally left empty and reported to the caller.
	var columnSums [3]float64
	for i := range raw {
		if base[i] {
			for c := range columnSums {
				columnSums[c] += raw[i][c]
			}
		}
	}
	for c, sum := range columnSums {
		if sum == 0 {
			warnings = append(warnings, fmt.Sprintf("generated mapping has no base contribution to %s", []string{"red", "green", "blue"}[c]))
			continue
		}
		for i := range raw {
			if base[i] {
				raw[i][c] /= sum
			}
		}
	}

	var narrowSums [3]float64
	for i := range raw {
		if !base[i] {
			for c := range narrowSums {
				narrowSums[c] += raw[i][c]
			}
		}
	}
	maxNarrow := math.Max(narrowSums[0], math.Max(narrowSums[1], narrowSums[2]))
	if maxNarrow > 0 {
		scale := options.NarrowbandAccentPercent / 100 / maxNarrow
		for i := range raw {
			if !base[i] {
				for c := range raw[i] {
					raw[i][c] *= scale
				}
			}
		}
	} else {
		for i := range raw {
			if !base[i] {
				return WavelengthMixResult{}, fmt.Errorf("source %q has no RGB contribution at the selected cross-mix", ordered[i].BlinkID)
			}
		}
	}

	result := WavelengthMixResult{Weights: make([]models.ComposeMixWeight, len(ordered)), Warnings: warnings}
	for i, source := range ordered {
		weight := models.ComposeMixWeight{BlinkID: source.BlinkID, Red: raw[i][0], Green: raw[i][1], Blue: raw[i][2]}
		if err := weight.Validate(); err != nil {
			return WavelengthMixResult{}, fmt.Errorf("source %q: %w", source.BlinkID, err)
		}
		result.Weights[i] = weight
	}
	return result, nil
}

func wavelengthAnchors(t, crossMix float64) [3]float64 {
	blue := [3]float64{0, crossMix, 1 - crossMix}
	green := [3]float64{crossMix, 1 - 2*crossMix, crossMix}
	red := [3]float64{1 - crossMix, crossMix, 0}
	if t <= 0.5 {
		return interpolateAnchor(blue, green, t*2)
	}
	return interpolateAnchor(green, red, (t-0.5)*2)
}

func interpolateAnchor(a, b [3]float64, t float64) (out [3]float64) {
	for i := range out {
		out[i] = a[i] + (b[i]-a[i])*t
	}
	return out
}

func finiteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
