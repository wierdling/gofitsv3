package processing

import (
	"context"
	"math"
)

// mosaicSaturatedStars finds bright, resolved cores whose central light is
// inconsistent with the surrounding stellar wings. Saturated here is
// an inference from morphology, not a detector DQ measurement. Broad sources
// still have to pass the existing robust, four-quadrant stellar wing fit.
func mosaicSaturatedStars(ctx context.Context, p []float32, w, h int, opt StarMapOptions) ([]StarMapSource, error) {
	var found []StarMapSource
	for pass, scale := range []int{2, 4} {
		if opt.Progress != nil {
			opt.Progress("Finding mosaic saturated stars", pass, 2)
		}
		cw, ch := w/scale, h/scale
		if cw < 25 || ch < 25 {
			continue
		}
		coarse := make([]float32, cw*ch)
		for y := 0; y < ch; y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for x := 0; x < cw; x++ {
				sum := 0.
				count := 0
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						v := float64(p[(y*scale+dy)*w+x*scale+dx])
						if !starFinite(v) {
							continue
						}
						sum += v
						count++
					}
				}
				if count*4 >= scale*scale*3 {
					coarse[y*cw+x] = float32(sum / float64(count))
				} else {
					coarse[y*cw+x] = float32(math.NaN())
				}
			}
		}
		// A box-smoothed seed ignores individual bright fragments in a damaged core.
		smooth := make([]float64, len(coarse))
		for y := 2; y < ch-2; y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for x := 2; x < cw-2; x++ {
				sum := 0.
				count := 0
				for dy := -2; dy <= 2; dy++ {
					for dx := -2; dx <= 2; dx++ {
						v := float64(coarse[(y+dy)*cw+x+dx])
						if starFinite(v) {
							sum += v
							count++
						}
					}
				}
				if count >= 20 {
					smooth[y*cw+x] = sum / float64(count)
				} else {
					smooth[y*cw+x] = math.NaN()
				}
			}
		}
		mask := make([]bool, len(coarse))
		for y := 12; y < ch-12; y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for x := 12; x < cw-12; x++ {
				i := y*cw + x
				v := smooth[i]
				if !starFinite(v) {
					continue
				}
				maximum := true
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						j := (y+dy)*cw + x + dx
						if j != i && (!starFinite(smooth[j]) || smooth[j] > v || (smooth[j] == v && j < i)) {
							maximum = false
						}
					}
				}
				if !maximum {
					continue
				}
				bg, _, _, noise := starLocalBackground(coarse, cw, ch, float64(x), float64(y))
				// The rescue is restricted to high-contrast, spatially resolved cores.
				if v-bg < math.Max(20, opt.MinSNR)*noise {
					continue
				}
				mask[i] = true
				s := MeasureStarMapSource(coarse, cw, ch, float64(x), float64(y), mask, opt)
				mask[i] = false
				if !s.Accepted() {
					continue
				}
				// A damaged/reconstructed core can be either suppressed or enhanced
				// relative to the wing fit. Require a factor-of-two mismatch in
				// either direction; the independent spike test still applies.
				core := 0.
				n := 0
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						vv := float64(coarse[(int(math.Round(s.Y))+dy)*cw+int(math.Round(s.X))+dx]) - bg
						if starFinite(vv) {
							core = math.Max(core, vv)
							n++
						}
					}
				}
				if n != 9 || core <= 0 || s.Amplitude <= 0 || (s.Amplitude < 2*core && core < 2*s.Amplitude) {
					continue
				}
				s.X = s.X*float64(scale) + float64(scale-1)/2
				s.Y = s.Y*float64(scale) + float64(scale-1)/2
				s.FWHM *= float64(scale)
				s.Radius *= float64(scale)
				if !mosaicDiffractionSpikes(p, w, h, s) {
					continue
				}
				s.Reason = "mosaic-inferred saturation with stellar wings"
				duplicate := false
				for _, a := range found {
					if math.Hypot(a.X-s.X, a.Y-s.Y) < math.Max(4, math.Min(a.Radius, s.Radius)/2) {
						duplicate = true
					}
				}
				if !duplicate {
					found = append(found, s)
				}
			}
		}
	}
	if opt.Progress != nil {
		opt.Progress("Finding mosaic saturated stars", 2, 2)
	}
	return found, nil
}

// Two roughly perpendicular, opposite spike pairs must persist through two
// outer annuli. This deliberately leaves spike-free ambiguous cores uncertain.
func mosaicDiffractionSpikes(p []float32, w, h int, s StarMapSource) bool {
	const bins = 32
	var votes, strong [bins]bool
	for band := 0; band < 2; band++ {
		radius := math.Max(30, s.Radius)
		inner := radius * (.8 + .4*float64(band))
		outer := inner + .4*radius
		if s.X-outer < 0 || s.Y-outer < 0 || s.X+outer >= float64(w) || s.Y+outer >= float64(h) {
			return false
		}
		var sums [bins]float64
		var counts [bins]int
		for y := int(s.Y - outer); y <= int(s.Y+outer); y++ {
			for x := int(s.X - outer); x <= int(s.X+outer); x++ {
				dx, dy := float64(x)-s.X, float64(y)-s.Y
				r := math.Hypot(dx, dy)
				if r < inner || r >= outer {
					continue
				}
				v := float64(p[y*w+x])
				if !starFinite(v) {
					continue
				}
				a := math.Atan2(dy, dx)
				if a < 0 {
					a += 2 * math.Pi
				}
				b := int(a*float64(bins)/(2*math.Pi)) % bins
				sums[b] += v
				counts[b]++
			}
		}
		levels := make([]float64, bins)
		for b := range sums {
			if counts[b] < 3 {
				return false
			}
			levels[b] = sums[b] / float64(counts[b])
		}
		background := starMedian(append([]float64(nil), levels...))
		deviations := make([]float64, bins)
		for b, v := range levels {
			deviations[b] = math.Abs(v - background)
		}
		spread := starMedian(deviations)
		for b, v := range levels {
			// Adjacent bins may share a narrow spike at their boundary.
			high := v > background+math.Max(spread, 1e-10)
			bright := v > background+2*math.Max(spread, 1e-10)
			if band == 0 {
				votes[b] = high
				strong[b] = bright
			} else {
				votes[b] = votes[b] && high
				strong[b] = strong[b] && bright
			}
		}
	}
	near := func(values [bins]bool, b int) bool {
		return values[(b+31)%bins] || values[b%bins] || values[(b+1)%bins]
	}
	for b := 0; b < bins; b++ {
		// Two strong neighboring arms, supported by their opposite arms.
		if strong[b] && near(strong, b+8) && near(votes, b+16) && near(votes, b+24) {
			return true
		}
	}
	return false
}
