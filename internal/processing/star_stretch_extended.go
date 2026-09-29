package processing

import (
	"context"
	"fmt"
	"math"
)

// Extended support has its own background: a halo can contaminate the core
// annulus and its noise estimate. These are bounded shape checks, not a PSF.
// quiet is the linear excess below which light is invisible at the current
// display settings; halo bins below it count as closed even above the noise.
// negligible is the linear excess below which the gentler correction itself
// is invisible; a declining halo still above quiet at its safe boundary may
// close there when it is below negligible.
func detectExtendedStarComponents(ctx context.Context, pixels []float32, w, h int, f StarTreatmentFit, sources []StarMapSource, quiet, negligible float64) (StarTreatmentFit, string) {
	out := StarTreatmentFit{}
	extent := math.Min(maxStarTreatmentExtent, math.Min(math.Min(f.X, f.Y), math.Min(float64(w-1)-f.X, float64(h-1)-f.Y))-3)
	if extent < math.Max(40, 8*f.Sigma) {
		return out, "core only: insufficient margin for extended background"
	}
	bg, bx, by, noise, ok := saturatedWingBackground(ctx, f.X, f.Y, extent/1.95, func(id int) bool { return id == f.SourceID || starTreatmentIsCompanion(f, id) }, sources, pixels, w, h, 40)
	if !ok || ctx.Err() != nil {
		return out, "core only: extended background unavailable"
	}
	p := extendedStarProbe{ctx: ctx, pixels: pixels, w: w, h: h, fit: f, bg: bg, bx: bx, by: by, noise: noise, quiet: quiet, negligible: negligible}
	for _, s := range sources {
		if ctx.Err() != nil {
			return out, "core only: cancelled"
		}
		if s.ID != f.SourceID && !starTreatmentIsCompanion(f, s.ID) && s.Accepted() && starFinite(s.X) && starFinite(s.Y) && starFinite(s.Radius) && s.Radius > 0 && math.Hypot(s.X-f.X, s.Y-f.Y) < extent+s.Radius+4 {
			p.neighbors = append(p.neighbors, s)
		}
	}
	// A still-falling annulus is not a measured background. Compare radial
	// halves after subtracting the plane; retain negative calibrated samples.
	var innerBG, outerBG []float64
	for y := max(0, int(f.Y-extent)); y <= min(h-1, int(f.Y+extent)); y++ {
		if ctx.Err() != nil {
			return out, "core only: cancelled"
		}
		for x := max(0, int(f.X-extent)); x <= min(w-1, int(f.X+extent)); x++ {
			r := math.Hypot(float64(x)-f.X, float64(y)-f.Y)
			if r < .72*extent || r > .96*extent || p.nearNeighbor(float64(x), float64(y), 2) {
				continue
			}
			v, ok := p.excess(float64(x), float64(y), float64(pixels[y*w+x]))
			if !ok {
				continue
			}
			if r < .84*extent {
				innerBG = append(innerBG, v)
			} else {
				outerBG = append(outerBG, v)
			}
		}
	}
	if len(innerBG) < 40 || len(outerBG) < 40 || math.Abs(starMedian(innerBG)-starMedian(outerBG)) > math.Max(noise, 1e-7) {
		return out, "core only: outer background has unresolved radial structure"
	}
	haloLimit := extent * .68
	for _, s := range p.neighbors {
		haloLimit = math.Min(haloLimit, math.Hypot(s.X-f.X, s.Y-f.Y)-s.Radius-3)
	}
	inner, outer, haloNote := p.halo(math.Max(4, math.Max(3*f.Sigma, f.CoreRadius+2)), haloLimit)
	if outer > f.OuterRadius+3 {
		// The halo's full-strength region begins no earlier than the core's
		// own outer radius, which the mask geometry requires.
		out.HaloValidated, out.HaloInnerRadius, out.HaloRadius = true, math.Max(inner, f.OuterRadius), outer
	}
	spikes, spikeNote := p.spikes(extent - 3)
	out.Spikes = spikes
	if ctx.Err() != nil {
		return StarTreatmentFit{}, "core only: cancelled"
	}
	if out.HaloValidated || len(out.Spikes) > 0 {
		out.ExtendedBackground, out.ExtendedBackgroundX, out.ExtendedBackgroundY, out.ExtendedNoise = bg, bx, by, noise
	}
	if out.HaloValidated {
		haloNote = fmt.Sprintf("halo measured to %.1f px", out.HaloRadius)
	} else if outer > 0 {
		haloNote = "no halo extension beyond core required"
	}
	return out, haloNote + "; " + spikeNote
}

type extendedStarProbe struct {
	ctx               context.Context
	pixels            []float32
	w, h              int
	fit               StarTreatmentFit
	bg, bx, by, noise float64
	quiet             float64 // display-invisible excess; see starDisplayQuietLevel
	negligible        float64 // excess whose gentler correction is invisible
	neighbors         []StarMapSource
}

func (p extendedStarProbe) background(x, y float64) float64 {
	return p.bg + p.bx*(x-p.fit.X) + p.by*(y-p.fit.Y)
}

// excess removes the extended plane and any blended companion's model from a
// raw sample; it is false for nonfinite samples and companion clipped cores.
func (p extendedStarProbe) excess(x, y, v float64) (float64, bool) {
	if !starFinite(v) {
		return 0, false
	}
	companion, ok := starTreatmentCompanionExcess(x, y, p.fit)
	if !ok {
		return 0, false
	}
	return v - p.background(x, y) - companion, true
}
func (p extendedStarProbe) nearNeighbor(x, y, margin float64) bool {
	for _, s := range p.neighbors {
		if math.Hypot(x-s.X, y-s.Y) <= s.Radius+margin {
			return true
		}
	}
	return false
}

// Missing science and neighbors are never interpolated as zero.
func (p extendedStarProbe) sample(dx, dy float64) (float64, bool) {
	x, y := p.fit.X+dx, p.fit.Y+dy
	ix, iy := int(math.Floor(x)), int(math.Floor(y))
	if ix < 0 || iy < 0 || ix+1 >= p.w || iy+1 >= p.h || p.nearNeighbor(x, y, 2) {
		return 0, false
	}
	a, b := x-float64(ix), y-float64(iy)
	v := [4]float64{float64(p.pixels[iy*p.w+ix]), float64(p.pixels[iy*p.w+ix+1]), float64(p.pixels[(iy+1)*p.w+ix]), float64(p.pixels[(iy+1)*p.w+ix+1])}
	for _, n := range v {
		if !starFinite(n) {
			return 0, false
		}
	}
	return p.excess(x, y, (1-b)*((1-a)*v[0]+a*v[1])+b*((1-a)*v[2]+a*v[3]))
}

func (p extendedStarProbe) halo(start, limit float64) (float64, float64, string) {
	if limit <= start+12 {
		return 0, 0, "halo incomplete: neighbor or boundary"
	}
	type sectorBin [8][]float64
	bins := make([]sectorBin, int(math.Ceil(limit/2))+1)
	for y := max(0, int(p.fit.Y-limit)); y <= min(p.h-1, int(p.fit.Y+limit)); y++ {
		if p.ctx.Err() != nil {
			return 0, 0, "halo incomplete: cancelled"
		}
		for x := max(0, int(p.fit.X-limit)); x <= min(p.w-1, int(p.fit.X+limit)); x++ {
			dx, dy := float64(x)-p.fit.X, float64(y)-p.fit.Y
			r := math.Hypot(dx, dy)
			if r < start || r >= limit || p.nearNeighbor(float64(x), float64(y), 2) {
				continue
			}
			v, ok := p.excess(float64(x), float64(y), float64(p.pixels[y*p.w+x]))
			if !ok {
				continue
			}
			q := int((math.Atan2(dy, dx)+math.Pi)*4/math.Pi) % 8
			b := int(r / 2)
			bins[b][q] = append(bins[b][q], v)
		}
	}
	first, last, lastValue := 0., 0., 0.
	finalMedian := 0.
	quiet, strongBins := 0, 0
	closed := false
	for i := int(math.Ceil(start / 2)); i < len(bins)-1; i++ {
		if p.ctx.Err() != nil {
			return 0, 0, "halo incomplete: cancelled"
		}
		strong, valid := 0, 0
		var medians []float64
		for _, vals := range bins[i] {
			if len(vals) < 4 {
				continue
			}
			valid++
			v := starMedian(vals)
			medians = append(medians, v)
			// Cap the gain from averaging correlated drizzle pixels at four.
			threshold := math.Max(math.Max(3*p.noise/math.Sqrt(math.Min(16, float64(len(vals)))), 1e-7), p.quiet)
			if v > threshold {
				strong++
			}
		}
		if valid < 8 {
			return 0, 0, "halo incomplete: missing radial support"
		}
		median := starMedian(medians)
		finalMedian = median
		if strong >= 6 {
			if first == 0 {
				first = median
			}
			last, lastValue = float64((i+1)*2), median
			strongBins++
			quiet = 0
		} else {
			quiet++
		}
		// Eight quiet pixels allow narrow gaps between diffraction rings.
		if quiet >= 4 && strongBins > 0 {
			closed = true
			break
		}
	}
	if strongBins < 3 {
		return 0, 0, "no extended circular halo validated"
	}
	note := "halo validated"
	if !closed {
		// A bright star's halo can stay above the quiet level to the safe
		// boundary. If it is still declining and its light there is below the
		// level at which the gentler correction is visible, the feather may end
		// inside the boundary without a step; otherwise the halo is incomplete.
		feather := math.Max(4, math.Min(12, limit*.15))
		if lastValue > .7*first || finalMedian <= 0 || finalMedian >= p.negligible || last <= limit-feather-2 {
			return 0, 0, "halo incomplete: signal reaches safe boundary"
		}
		last = limit - feather - 2
		closed = true
		note = "halo closes at safe boundary below negligible correction"
	}
	if lastValue > .7*first {
		return 0, 0, "halo incomplete: no declining radial support"
	}
	outer := last + math.Max(4, math.Min(12, last*.15))
	if outer >= limit {
		return 0, 0, "halo incomplete: no room for outer feather"
	}
	return last, outer, note
}

// Local transverse bands are sampled at the same radius. Circular halos do
// not create contrast, and the other arm of a cross is not its background.
func (p extendedStarProbe) contrast(angle, r, side float64) (float64, bool) {
	ca, sa := math.Cos(angle), math.Sin(angle)
	var center, flank []float64
	for _, dr := range []float64{-1, 0, 1} {
		rr := r + dr
		for _, t := range []float64{-.5, 0, .5, -side, side} {
			if math.Abs(t) >= rr {
				return 0, false
			}
			u := math.Sqrt(rr*rr - t*t)
			v, ok := p.sample(u*ca-t*sa, u*sa+t*ca)
			if !ok {
				return 0, false
			}
			if math.Abs(t) <= .5 {
				center = append(center, v)
			} else {
				flank = append(flank, v)
			}
		}
	}
	return starMedian(center) - starMedian(flank), true
}

func (p extendedStarProbe) crossScore(angle, start, end float64) float64 {
	var energy [4]float64
	var counts [4]int
	threshold := math.Max(p.noise, 1e-7)
	for r := start; r < end; r += 4 {
		if p.ctx.Err() != nil {
			return 0
		}
		for arm := 0; arm < 4; arm++ {
			v, ok := p.contrast(angle+float64(arm)*math.Pi/2, r, 4)
			if ok && v > threshold {
				energy[arm] += v
				counts[arm]++
			}
		}
	}
	lo, hi := math.Inf(1), 0.
	for i, v := range energy {
		if counts[i] < 3 {
			return 0
		}
		lo = math.Min(lo, v)
		hi = math.Max(hi, v)
	}
	if hi > 10*lo {
		return 0
	}
	return lo
}

func (p extendedStarProbe) spikes(extent float64) ([]StarTreatmentSpike, string) {
	start := math.Max(6, math.Max(3*p.fit.Sigma, p.fit.CoreRadius+3))
	searchEnd := math.Min(extent-8, math.Max(start+24, 64))
	if searchEnd < start+12 {
		return nil, "spikes incomplete: insufficient search margin"
	}
	angle, score := 0., 0.
	for deg := 0.; deg < 90; deg += 2 {
		if p.ctx.Err() != nil {
			return nil, "spikes incomplete: cancelled"
		}
		a := deg * math.Pi / 180
		if s := p.crossScore(a, start, searchEnd); s > score {
			angle, score = a, s
		}
	}
	if score == 0 {
		return nil, "no paired diffraction cross validated"
	}
	coarse := angle
	for deg := -2.; deg <= 2; deg += .25 {
		a := coarse + deg*math.Pi/180
		if s := p.crossScore(a, start, searchEnd); s > score {
			angle, score = a, s
		}
	}
	var arms []StarTreatmentSpike
	for arm := 0; arm < 4; arm++ {
		a := angle + float64(arm)*math.Pi/2
		last, firstValue, lastValue := 0., 0., 0.
		strong, quiet := 0, 0
		closed := false
		for r := start; r < extent-6; r += 2 {
			if p.ctx.Err() != nil {
				return nil, "spikes incomplete: cancelled"
			}
			v, ok := p.contrast(a, r, 4)
			if !ok {
				break
			}
			if v > math.Max(.9*p.noise, 1e-7) {
				if strong == 0 && r > start+6 {
					break
				}
				if firstValue == 0 {
					firstValue = v
				}
				last, lastValue = r, v
				strong++
				quiet = 0
			} else {
				quiet++
			}
			if quiet >= 3 {
				closed = true
				break
			}
		}
		if !closed || strong < 4 || last < start+8 || lastValue > .8*firstValue {
			return nil, "spikes incomplete: arm lacks clean declining extent"
		}
		width := 1.5
		for _, frac := range []float64{.25, .5, .75} {
			r := start + (last-start)*frac
			center, ok := p.contrast(a, r, 6)
			if !ok {
				return nil, "spikes incomplete: invalid transverse support"
			}
			ca, sa := math.Cos(a), math.Sin(a)
			for t := .5; t <= 5; t += .5 {
				u := math.Sqrt(math.Max(0, r*r-t*t))
				v1, ok1 := p.sample(u*ca-t*sa, u*sa+t*ca)
				v2, ok2 := p.sample(u*ca+t*sa, u*sa-t*ca)
				edge := math.Sqrt(math.Max(0, r*r-36))
				b1, ok3 := p.sample(edge*ca-6*sa, edge*sa+6*ca)
				b2, ok4 := p.sample(edge*ca+6*sa, edge*sa-6*ca)
				if !ok1 || !ok2 || !ok3 || !ok4 {
					return nil, "spikes incomplete: invalid transverse support"
				}
				if math.Max(v1, v2)-.5*(b1+b2) > math.Max(p.noise, .1*center) {
					width = math.Max(width, t+1.5)
				}
			}
		}
		end := last + 6
		if end <= p.fit.OuterRadius+8 {
			return nil, "no spike extension beyond core validated"
		}
		// Validate the entire arm and its feather, not just its centerline.
		for r := math.Max(0, p.fit.InnerRadius-3); r <= end; r++ {
			if p.ctx.Err() != nil {
				return nil, "spikes incomplete: cancelled"
			}
			for t := -width; t <= width; t++ {
				if _, ok := p.sample(r*math.Cos(a)-t*math.Sin(a), r*math.Sin(a)+t*math.Cos(a)); !ok {
					return nil, "spikes incomplete: unsafe arm footprint"
				}
			}
		}
		arms = append(arms, StarTreatmentSpike{Angle: a, StartRadius: math.Max(0, p.fit.InnerRadius-3), EndRadius: end, Width: width})
	}
	lo, hi := arms[0].EndRadius, arms[0].EndRadius
	for _, a := range arms[1:] {
		lo = math.Min(lo, a.EndRadius)
		hi = math.Max(hi, a.EndRadius)
	}
	return arms, fmt.Sprintf("four paired spike arms measured (%.1f-%.1f px)", lo, hi)
}
