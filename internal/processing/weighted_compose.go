package processing

import (
	"context"
	"fmt"
	"math"
	"sort"

	"gofitsv3/internal/models"
)

// WeightedComposeSource is one already registered and stretched Compose
// source. BlinkID is the stable identity used when a source is an overlay.
type WeightedComposeSource struct {
	BlinkID string
	Pixels  []float32
	Weights models.ComposeMixWeight
}

// WeightedComposeRGB returns three normalized float32 planes. Sources are
// normalized by their finite peak, mixed simultaneously, and gamut-compressed
// by a common scale so the RGB ratios (hue) are preserved at highlights.
func WeightedComposeRGB(ctx context.Context, sources []WeightedComposeSource, width, height int) ([3][]float32, error) {
	var out [3][]float32
	if width <= 0 || height <= 0 {
		return out, fmt.Errorf("weighted composition dimensions must be positive")
	}
	n := width * height
	ordered := append([]WeightedComposeSource(nil), sources...)
	for i := range ordered {
		if ordered[i].BlinkID == "" {
			ordered[i].BlinkID = ordered[i].Weights.BlinkID
		}
	}
	if err := validateWeightedSources(ctx, ordered, n); err != nil {
		return out, err
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].BlinkID < ordered[j].BlinkID })
	out[0], out[1], out[2] = make([]float32, n), make([]float32, n), make([]float32, n)
	peaks := make([]float32, len(ordered))
	for i, source := range ordered {
		var peak float32
		for j, value := range source.Pixels {
			if j&1023 == 0 {
				select {
				case <-ctx.Done():
					return [3][]float32{}, ctx.Err()
				default:
				}
			}
			if finite32(value) && value > peak {
				peak = value
			}
		}
		if peak > 0 {
			peaks[i] = peak
		}
	}
	for pixel := 0; pixel < n; pixel++ {
		if pixel&1023 == 0 {
			select {
			case <-ctx.Done():
				return [3][]float32{}, ctx.Err()
			default:
			}
		}
		var sum [3]float64
		for i, source := range ordered {
			value := source.Pixels[pixel]
			if !finite32(value) || peaks[i] == 0 {
				continue
			}
			v := float64(value / peaks[i])
			weights := [3]float64{source.Weights.Red, source.Weights.Green, source.Weights.Blue}
			for c, weight := range weights {
				if weight > 0 {
					sum[c] += v * weight
				}
			}
		}
		for c := range out {
			out[c][pixel] = float32(sum[c])
		}
		max := maxRGBAt(out, pixel)
		if max > 1 {
			out[0][pixel] /= max
			out[1][pixel] /= max
			out[2][pixel] /= max
		}
	}
	return out, nil
}

func validateWeightedSources(ctx context.Context, sources []WeightedComposeSource, pixels int) error {
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if source.BlinkID == "" {
			if source.Weights.BlinkID == "" {
				return fmt.Errorf("weighted composition source BlinkID is required")
			}
			source.BlinkID = source.Weights.BlinkID
		}
		if source.Weights.BlinkID != "" && source.Weights.BlinkID != source.BlinkID {
			return fmt.Errorf("source BlinkID %q does not match weight BlinkID %q", source.BlinkID, source.Weights.BlinkID)
		}
		if _, exists := seen[source.BlinkID]; exists {
			return fmt.Errorf("duplicate weighted composition source BlinkID %q", source.BlinkID)
		}
		seen[source.BlinkID] = struct{}{}
		if len(source.Pixels) != pixels {
			return fmt.Errorf("source %q has %d pixels, want %d", source.BlinkID, len(source.Pixels), pixels)
		}
		for _, value := range source.Pixels {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if !finite32(value) || value < 0 {
				return fmt.Errorf("source %q contains invalid pixel value", source.BlinkID)
			}
		}
		if err := source.Weights.Validate(); err != nil {
			return fmt.Errorf("source %q: %w", source.BlinkID, err)
		}
	}
	return nil
}

func maxRGBAt(rgb [3][]float32, index int) float32 {
	max := rgb[0][index]
	if rgb[1][index] > max {
		max = rgb[1][index]
	}
	if rgb[2][index] > max {
		max = rgb[2][index]
	}
	return max
}

func finite32(v float32) bool { return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) }
