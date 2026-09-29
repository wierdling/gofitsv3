package processing

import (
	"context"
	"math"
	"sort"
)

// fitDiffractionRingProfile validates a resolved, unsaturated source whose
// radial light is oscillatory. It deliberately stores the measured profile
// only as diagnostics: rendering still compresses the observed excess and
// never reconstructs missing flux.
func fitDiffractionRingProfile(ctx context.Context, seed StarTreatmentFit, s StarMapSource, sources []StarMapSource, pixels []float32, w, h int, opt StarTreatmentOptions) (StarTreatmentFit, bool) {
	f := seed
	f.RingValidated = false
	f.RingOscillations = 0
	f.RingRadii = nil
	f.RingValues = nil
	f.WingModel = ""
	f.Saturated = false
	fwhm := starSourceFWHM(s)
	f.Sigma = math.Max(fwhm/2.355, .65)
	outer := math.Min(32, math.Max(opt.OuterRadius, math.Max(1.5*s.Radius, 6*f.Sigma)))
	if outer <= 4*f.Sigma || f.X-1.9*outer < 0 || f.Y-1.9*outer < 0 || f.X+1.9*outer >= float64(w) || f.Y+1.9*outer >= float64(h) {
		f.Reason = "ring source has insufficient bounded support"
		return f, false
	}
	bg, bx, by, noise, ok := saturatedWingBackground(ctx, f.X, f.Y, outer, func(id int) bool { return id == f.SourceID }, sources, pixels, w, h, opt.MinSamples)
	if !ok {
		f.Reason = "ring source has insufficient background"
		return f, false
	}
	f.Background, f.BackgroundX, f.BackgroundY, f.Noise = bg, bx, by, noise
	core := math.Max(1.1, .7*f.Sigma)
	binWidth := .5
	binCount := int(math.Floor((outer - core) / binWidth))
	if binCount < 6 {
		f.Reason = "ring source has too little radial span"
		return f, false
	}
	type radialBin struct {
		all      []float64
		sectors  [4][]float64
		observed [4]int
		radius   float64
		median   float64
		valid    bool
	}
	bins := make([]radialBin, binCount)
	for i := range bins {
		bins[i].radius = core + (float64(i)+.5)*binWidth
	}
	for y := max(0, int(math.Floor(f.Y-outer))); y <= min(h-1, int(math.Ceil(f.Y+outer))); y++ {
		if err := ctx.Err(); err != nil {
			return f, false
		}
		for x := max(0, int(math.Floor(f.X-outer))); x <= min(w-1, int(math.Ceil(f.X+outer))); x++ {
			dx, dy := float64(x)-f.X, float64(y)-f.Y
			r := math.Hypot(dx, dy)
			idx := int(math.Floor((r - core) / binWidth))
			if idx < 0 || idx >= len(bins) {
				continue
			}
			v := float64(pixels[y*w+x])
			if !starFinite(v) {
				continue
			}
			e := math.Max(0, v-(bg+bx*dx+by*dy))
			q := 0
			if dx < 0 {
				q++
			}
			if dy < 0 {
				q += 2
			}
			bins[idx].all = append(bins[idx].all, e)
			bins[idx].sectors[q] = append(bins[idx].sectors[q], e)
			bins[idx].observed[q]++
		}
	}
	profile := make([]float64, 0, len(bins))
	radii := make([]float64, 0, len(bins))
	for i := range bins {
		b := &bins[i]
		if len(b.all) < 8 || b.observed[0] < 2 || b.observed[1] < 2 || b.observed[2] < 2 || b.observed[3] < 2 {
			continue
		}
		sectorMedians := make([]float64, 0, 4)
		for _, sector := range b.sectors {
			if len(sector) >= 2 {
				sectorMedians = append(sectorMedians, starMedian(sector))
			}
		}
		if len(sectorMedians) < 3 {
			continue
		}
		sort.Float64s(sectorMedians)
		low, high := sectorMedians[0], sectorMedians[len(sectorMedians)-1]
		if high > 8*math.Max(low, noise) {
			continue
		}
		b.median = starMedian(b.all)
		b.valid = true
		profile = append(profile, b.median)
		radii = append(radii, b.radius)
	}
	if len(profile) < 6 {
		f.Reason = "ring source has insufficient angularly consistent bins"
		return f, false
	}
	// A radial profile cannot silently skip an entire annulus: doing so would
	// let unrelated bright structures on either side of a missing-data gap look
	// like a valid sequence of rings. Leading/trailing weak bins are allowed,
	// but an internal gap of two or more bins rejects the fit.
	first, last := -1, -1
	for i := range bins {
		if bins[i].valid {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	gap := 0
	for i := first; i <= last && first >= 0; i++ {
		if !bins[i].valid {
			gap++
			if gap >= 2 {
				f.Reason = "ring source has a missing internal radial interval"
				return f, false
			}
		} else {
			gap = 0
		}
	}
	peak := 0.0
	for _, v := range profile {
		peak = math.Max(peak, v)
	}
	if peak <= math.Max(6*noise, 1e-9) || profile[len(profile)-1] > .65*peak {
		f.Reason = "ring source has no bounded outward decay"
		return f, false
	}
	// Count prominent extrema rather than reacting to one-pixel noise. A true
	// diffraction profile has a bright ring followed by a dark gap and another
	// bright ring (or the corresponding inner/outer sequence).
	oscillations := 0
	for i := 1; i+1 < len(profile); i++ {
		prominence := math.Max(noise, .10*math.Max(noise, math.Min(profile[i-1], profile[i+1])))
		if (profile[i] > profile[i-1]+prominence && profile[i] > profile[i+1]+prominence) ||
			(profile[i] < profile[i-1]-prominence && profile[i] < profile[i+1]-prominence) {
			oscillations++
		}
	}
	if oscillations < 2 {
		peaks := 0
		for i := 1; i+1 < len(profile); i++ {
			if profile[i] > .05*peak && profile[i] > profile[i-1]+noise && profile[i] > profile[i+1]+noise {
				peaks++
			}
		}
		if peaks >= 2 {
			oscillations = peaks
		} else {
			f.Reason = "ring source is monotonic or lacks resolved oscillations"
			return f, false
		}
	}
	// Measure the empirical fit error against the radial medians. This check is
	// independent of angular validation and prevents a few symmetric artifacts
	// from being accepted as a ring profile.
	loss, energy := 0.0, 0.0
	for _, b := range bins {
		if !b.valid {
			continue
		}
		for _, v := range b.all {
			d := v - b.median
			loss += d * d
			energy += v * v
		}
	}
	residual := math.Sqrt(loss / math.Max(energy, 1e-20))
	if !starFinite(residual) || residual > .45 {
		f.Reason = "ring source empirical profile residual is too large"
		return f, false
	}
	f.Signal = peak
	f.SNR = peak / math.Max(noise, 1e-9)
	f.Residual = residual
	f.OuterRadius = outer
	f.CoreRadius = 0
	f.RingValidated = true
	f.RingOscillations = oscillations
	f.RingRadii = radii
	f.RingValues = profile
	f.WingModel = "EmpiricalRing"
	f.Usable = f.SNR >= opt.MinSNR
	if !f.Usable {
		f.Reason = "ring source has low contrast"
		return f, false
	}
	f.Reason = "validated oscillatory diffraction-ring profile"
	return f, true
}
