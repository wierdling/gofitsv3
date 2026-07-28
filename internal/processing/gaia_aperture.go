package processing

import (
	"fmt"
	"math"
	"sort"
)

// ApertureFlux is repeatable aperture photometry at a common aligned position.
type ApertureFlux struct {
	Flux, Background, SNR   float64
	Samples, AnnulusSamples int
	Saturated, Blended      bool
}

// MeasureApertureAnnulus estimates a local background from a clipped annulus,
// then integrates the source aperture. Pixels outside the image or non-finite
// samples are ignored; too few valid samples are reported as an error.
func MeasureApertureAnnulus(pixels []float32, width, height int, x, y, apertureRadius, annulusInner, annulusOuter, saturation float64) (ApertureFlux, error) {
	if width <= 0 || height <= 0 || len(pixels) < width*height || apertureRadius <= 0 || annulusOuter <= annulusInner || annulusInner <= apertureRadius {
		return ApertureFlux{}, fmt.Errorf("invalid aperture geometry")
	}
	ann := make([]float64, 0)
	for yy := int(math.Floor(y - annulusOuter)); yy <= int(math.Ceil(y+annulusOuter)); yy++ {
		for xx := int(math.Floor(x - annulusOuter)); xx <= int(math.Ceil(x+annulusOuter)); xx++ {
			if xx < 0 || yy < 0 || xx >= width || yy >= height {
				continue
			}
			d := math.Hypot(float64(xx)-x, float64(yy)-y)
			if d < annulusInner || d > annulusOuter {
				continue
			}
			v := float64(pixels[yy*width+xx])
			if finiteFloat(v) {
				ann = append(ann, v)
			}
		}
	}
	if len(ann) < 3 {
		return ApertureFlux{}, fmt.Errorf("insufficient annulus samples")
	}
	sort.Float64s(ann)
	bg := ann[len(ann)/2]
	var sum, variance float64
	n := 0
	sat := false
	for yy := int(math.Floor(y - apertureRadius)); yy <= int(math.Ceil(y+apertureRadius)); yy++ {
		for xx := int(math.Floor(x - apertureRadius)); xx <= int(math.Ceil(x+apertureRadius)); xx++ {
			if xx < 0 || yy < 0 || xx >= width || yy >= height || math.Hypot(float64(xx)-x, float64(yy)-y) > apertureRadius {
				continue
			}
			v := float64(pixels[yy*width+xx])
			if !finiteFloat(v) {
				continue
			}
			if saturation > 0 && v >= saturation {
				sat = true
			}
			sum += v - bg
			variance += math.Max(v, 0)
			n++
		}
	}
	if n == 0 {
		return ApertureFlux{}, fmt.Errorf("no aperture samples")
	}
	if sum <= 0 {
		return ApertureFlux{Flux: sum, Background: bg, Samples: n, AnnulusSamples: len(ann), Saturated: sat}, nil
	}
	return ApertureFlux{Flux: sum, Background: bg, SNR: sum / math.Sqrt(math.Max(variance, 1)), Samples: n, AnnulusSamples: len(ann), Saturated: sat}, nil
}
