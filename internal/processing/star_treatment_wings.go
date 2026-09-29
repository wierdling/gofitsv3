package processing

import (
	"context"
	"math"
	"sort"
)

// fitSaturatedStarWings fits only the observed light outside an inferred
// clipped core. The missing core is never reconstructed; CoreRadius is carried
// into the footprint so that the renderer compresses the samples it actually
// has at full mask strength.
func fitSaturatedStarWings(ctx context.Context, f StarTreatmentFit, s StarMapSource, sources []StarMapSource, pixels []float32, w, h int, opt StarTreatmentOptions) (StarTreatmentFit, bool) {
	f.Saturated = true
	f.WingValidated = false
	f.WingModel = ""
	fwhm := starSourceFWHM(s)
	f.Sigma = math.Max(fwhm/2.355, .65)
	for _, other := range sources {
		if ctx.Err() != nil {
			return f, false
		}
		if other.ID != s.ID && other.Accepted() && starFinite(other.X) && starFinite(other.Y) && starSourcesBlended(s, other) {
			f.Reason = "neighbor overlaps saturated stellar core"
			return f, false
		}
	}
	// The rescue detector scales the catalog radius on coarse grids. Allow a
	// larger, still bounded wing search for those sources, while retaining the
	// normal option as the lower bound for ordinary stars.
	outer := math.Max(1.5*s.Radius, 6*f.Sigma)
	outer = math.Min(32, math.Max(opt.OuterRadius, outer))
	if outer <= 2.5*f.Sigma || f.X-1.9*outer < 0 || f.Y-1.9*outer < 0 || f.X+1.9*outer >= float64(w) || f.Y+1.9*outer >= float64(h) {
		f.Reason = "saturated source has insufficient bounded wings"
		return f, false
	}
	// Exclude a conservative central radius, then expand it for a measured
	// plateau. The wing fit describes support, not recovered core brightness.
	core := math.Max(1.5, 0.9*fwhm)
	if core >= outer*.55 {
		f.Reason = "saturated source has insufficient wing span"
		return f, false
	}
	bg, bx, by, noise, ok := saturatedWingBackground(ctx, f.X, f.Y, outer, func(id int) bool { return id == f.SourceID }, sources, pixels, w, h, opt.MinSamples)
	if !ok {
		f.Reason = "saturated source has insufficient wing background"
		return f, false
	}
	f.Background, f.BackgroundX, f.BackgroundY, f.Noise = bg, bx, by, noise
	if measured := saturatedPlateauRadius(ctx, f.X, f.Y, core, outer, bg, bx, by, noise, pixels, w, h); measured > core {
		core = measured
	}
	if core >= outer*.55 {
		f.Reason = "saturated source has insufficient wing span"
		return f, false
	}
	type sample struct {
		r2, excess, dx, dy float64
		quadrant           int
	}
	var samples []sample
	var quadrants [4]int
	for y := max(0, int(math.Floor(f.Y-outer))); y <= min(h-1, int(math.Ceil(f.Y+outer))); y++ {
		if ctx.Err() != nil {
			return f, false
		}
		for x := max(0, int(math.Floor(f.X-outer))); x <= min(w-1, int(math.Ceil(f.X+outer))); x++ {
			dx, dy := float64(x)-f.X, float64(y)-f.Y
			r := math.Hypot(dx, dy)
			if r < core || r >= outer {
				continue
			}
			v := float64(pixels[y*w+x])
			if !starFinite(v) {
				continue
			}
			e := v - (bg + bx*dx + by*dy)
			if e <= 0 {
				continue
			}
			q := 0
			if dx < 0 {
				q++
			}
			if dy < 0 {
				q += 2
			}
			samples = append(samples, sample{r * r, e, dx, dy, q})
			if e > math.Max(3, opt.MinSNR)*noise {
				quadrants[q]++
			}
		}
	}
	if len(samples) < max(24, opt.MinSamples) || quadrants[0] < 3 || quadrants[1] < 3 || quadrants[2] < 3 || quadrants[3] < 3 {
		f.Reason = "saturated source has insufficient spatially distributed wings"
		return f, false
	}
	// A hard bright disk can fit a flexible Moffat surprisingly well even
	// though it has no stellar wing. Require several radial bins and a clear
	// decline toward the search boundary.
	var radial [][]float64
	for i := 0; i < int(math.Ceil(outer-core)); i++ {
		radial = append(radial, nil)
	}
	for _, a := range samples {
		r := math.Sqrt(a.r2)
		bin := int(math.Floor(r - core))
		if bin >= 0 && bin < len(radial) && a.excess > 3*noise {
			radial[bin] = append(radial[bin], a.excess)
		}
	}
	var profile []float64
	for _, bin := range radial {
		if len(bin) >= 4 {
			profile = append(profile, starMedian(bin))
		}
	}
	if len(profile) < 3 || profile[len(profile)-1] > .65*profile[0] {
		f.Reason = "saturated source has no measurable declining wings"
		return f, false
	}
	f.SNR = profile[0] / noise // observed wings, not extrapolated central amplitude

	type candidate struct {
		amp, width, beta, residual float64
		model                      string
	}
	best := candidate{residual: math.Inf(1)}
	for step := 0; step <= 32; step++ {
		if err := ctx.Err(); err != nil {
			return f, false
		}
		width := f.Sigma * (.55 + float64(step)*.06)
		for _, beta := range []float64{0, 1.5, 2.5, 4} { // zero is Gaussian; others are Moffat
			dot, norm := 0., 0.
			for _, a := range samples {
				p := math.Exp(-.5 * a.r2 / (width * width))
				if beta > 0 {
					p = math.Pow(1+a.r2/(width*width), -beta)
				}
				dot += p * a.excess
				norm += p * p
			}
			amp := dot / math.Max(norm, 1e-20)
			if amp <= 0 {
				continue
			}
			deviations := make([]float64, 0, len(samples))
			values := make([]float64, 0, len(samples))
			dxs := make([]float64, 0, len(samples))
			dys := make([]float64, 0, len(samples))
			for _, a := range samples {
				p := math.Exp(-.5 * a.r2 / (width * width))
				if beta > 0 {
					p = math.Pow(1+a.r2/(width*width), -beta)
				}
				deviations = append(deviations, a.excess-amp*p)
				values = append(values, a.excess)
				dxs = append(dxs, a.dx)
				dys = append(dys, a.dy)
			}
			res := trimmedWingResidual(deviations, values, dxs, dys)
			if res < best.residual {
				model := "Gaussian"
				if beta > 0 {
					model = "Moffat"
				}
				best = candidate{amp: amp, width: width, beta: beta, residual: res, model: model}
			}
		}
	}
	f.Residual = best.residual
	f.CoreRadius = core
	if !starFinite(best.amp) || best.amp <= 3*noise || best.residual > .35 {
		f.Reason = "saturated source wings are not a bounded stellar profile"
		return f, false
	}
	// Reject a one-sided contaminant: each quadrant must contain meaningful
	// wing energy and agree with the fitted radial profile within a broad bound.
	var qsum [4]float64
	for _, a := range samples {
		qsum[a.quadrant] += a.excess
	}
	total := qsum[0] + qsum[1] + qsum[2] + qsum[3]
	minQ, maxQ := qsum[0], qsum[0]
	for _, v := range qsum[1:] {
		minQ, maxQ = math.Min(minQ, v), math.Max(maxQ, v)
	}
	for _, v := range qsum {
		if v < .02*total || minQ <= 0 || maxQ > 10*minQ {
			f.Reason = "saturated source wings are asymmetric or contaminated"
			return f, false
		}
	}
	f.Signal, f.Sigma, f.OuterRadius = best.amp, best.width, outer
	f.CoreRadius = core
	if f.SNR < opt.MinSNR {
		f.Reason = "saturated source wings have low contrast"
		return f, false
	}
	f.Residual = best.residual
	f.WingValidated, f.Usable, f.WingModel = true, true, best.model
	f.Reason = "validated saturated wings; clipped core excluded from fit"
	return f, true
}

// saturatedWingBackground fits a robust plane in the annulus 1.35-1.9 x outer
// around (cx, cy). Sources for which excluded returns true (the star itself
// and any blended companions) are not treated as neighbors to mask out.
func saturatedWingBackground(ctx context.Context, cx, cy, outer float64, excluded func(int) bool, sources []StarMapSource, pixels []float32, w, h, minSamples int) (float64, float64, float64, float64, bool) {
	var a [3][3]float64
	var b [3]float64
	var vals, dxs, dys []float64
	nearby := nearbyStarSources(cx, cy, outer*1.9, excluded, sources)
	for y := max(0, int(math.Floor(cy-outer*2))); y <= min(h-1, int(math.Ceil(cy+outer*2))); y++ {
		if ctx.Err() != nil {
			return 0, 0, 0, 0, false
		}
		for x := max(0, int(math.Floor(cx-outer*2))); x <= min(w-1, int(math.Ceil(cx+outer*2))); x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			r := math.Hypot(dx, dy)
			if r < outer*1.35 || r > outer*1.9 {
				continue
			}
			if ctx.Err() != nil {
				return 0, 0, 0, 0, false
			}
			if nearStarSource(float64(x), float64(y), nearby) {
				continue
			}
			v := float64(pixels[y*w+x])
			if !starFinite(v) {
				continue
			}
			q := [3]float64{1, dx, dy}
			for i := 0; i < 3; i++ {
				b[i] += q[i] * v
				for j := 0; j < 3; j++ {
					a[i][j] += q[i] * q[j]
				}
			}
			vals, dxs, dys = append(vals, v), append(dxs, dx), append(dys, dy)
		}
	}
	if len(vals) < max(20, minSamples) {
		return 0, 0, 0, 0, false
	}
	plane, ok := solve3(a, b)
	if !ok {
		return 0, 0, 0, 0, false
	}
	// Match the ordinary-star background protection against isolated bright
	// knots and unlisted contaminants in the annulus.
	for iteration := 0; iteration < 3; iteration++ {
		if ctx.Err() != nil {
			return 0, 0, 0, 0, false
		}
		residuals := make([]float64, len(vals))
		for i, v := range vals {
			residuals[i] = math.Abs(v - plane[0] - plane[1]*dxs[i] - plane[2]*dys[i])
		}
		limit := math.Max(4*1.4826*starMedian(residuals), 1e-8)
		a, b = [3][3]float64{}, [3]float64{}
		kept := 0
		for i, v := range vals {
			if math.Abs(v-plane[0]-plane[1]*dxs[i]-plane[2]*dys[i]) > limit {
				continue
			}
			q := [3]float64{1, dxs[i], dys[i]}
			for j := range q {
				b[j] += q[j] * v
				for k := range q {
					a[j][k] += q[j] * q[k]
				}
			}
			kept++
		}
		if kept < minSamples {
			return 0, 0, 0, 0, false
		}
		plane, ok = solve3(a, b)
		if !ok {
			return 0, 0, 0, 0, false
		}
	}
	dev := make([]float64, len(vals))
	for i := range vals {
		dev[i] = math.Abs(vals[i] - plane[0] - plane[1]*dxs[i] - plane[2]*dys[i])
	}
	noise := math.Max(1.4826*starMedian(dev), 1e-9)
	return plane[0], plane[1], plane[2], noise, true
}

// trimmedWingResidual is the relative RMS misfit after discarding the most
// deviant samples, provided those samples are azimuthally balanced around the
// star. Diffraction spikes and rings are paired stellar light that no radial
// profile describes; they must not veto a wing fit that the azimuthally
// typical profile supports. A one-sided excess (a neighbor, a streak) is not
// balanced, and then the full residual is returned so it still fails.
func trimmedWingResidual(deviations, values, dxs, dys []float64) float64 {
	n := len(deviations)
	if n == 0 {
		return math.Inf(1)
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return math.Abs(deviations[order[a]]) < math.Abs(deviations[order[b]]) })
	keep := n - n*wingResidualTrimPercent/100
	if keep < 1 {
		keep = 1
	}
	var sumX, sumY, sumAbs float64
	for _, i := range order[keep:] {
		r := math.Hypot(dxs[i], dys[i])
		if r <= 0 {
			continue
		}
		sumX += deviations[i] * dxs[i] / r
		sumY += deviations[i] * dys[i] / r
		sumAbs += math.Abs(deviations[i])
	}
	balanced := sumAbs <= 0 || math.Hypot(sumX, sumY) <= wingResidualBalanceLimit*sumAbs
	if !balanced {
		keep = n
	}
	loss, energy := 0., 0.
	for _, i := range order[:keep] {
		loss += deviations[i] * deviations[i]
		energy += values[i] * values[i]
	}
	return math.Sqrt(loss / math.Max(energy, 1e-20))
}

const (
	wingResidualTrimPercent  = 15
	wingResidualBalanceLimit = .35 // net direction of trimmed deviations, as a fraction of their magnitude
)

func saturatedPlateauRadius(ctx context.Context, cx, cy, start, outer, bg, bx, by, noise float64, pixels []float32, w, h int) float64 {
	var center []float64
	last := 0.
	for r := 0.; r < outer*.45; r++ {
		if ctx.Err() != nil {
			return last
		}
		var vals []float64
		for y := max(0, int(math.Floor(cy-r-1))); y <= min(h-1, int(math.Ceil(cy+r+1))); y++ {
			for x := max(0, int(math.Floor(cx-r-1))); x <= min(w-1, int(math.Ceil(cx+r+1))); x++ {
				dx, dy := float64(x)-cx, float64(y)-cy
				d := math.Hypot(dx, dy)
				if d < r || d >= r+1 {
					continue
				}
				v := float64(pixels[y*w+x]) - (bg + bx*dx + by*dy)
				if starFinite(v) {
					vals = append(vals, v)
				}
			}
		}
		if r == 0 {
			if len(vals) > 0 {
				center = vals
			}
			continue
		}
		if len(vals) < 4 || len(center) == 0 {
			continue
		}
		m := starMedian(vals)
		if len(center) > 0 && m >= starMedian(center)*.95 && m > 5*noise {
			last = r + 1
		} else if r >= start {
			break
		}
	}
	return last
}
