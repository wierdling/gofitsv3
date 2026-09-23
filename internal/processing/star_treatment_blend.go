package processing

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// StarTreatmentCompanion is a blended neighbor's fitted profile. Footprint
// tests subtract it from a pixel instead of rejecting the neighbor as a
// contaminant. A positive CoreRadius marks a clipped core that is not
// modelled; pixels inside it are skipped rather than subtracted.
type StarTreatmentCompanion struct {
	SourceID                              int
	X, Y, Signal, Sigma, Beta, CoreRadius float64
}

const (
	maxStarBlendMembers = 6
	maxStarBlendOuter   = 40.0
)

func starSourceFWHM(s StarMapSource) float64 {
	if starFinite(s.FWHM) && s.FWHM > 0 {
		return s.FWHM
	}
	return math.Max(s.Radius/1.6, 1.5)
}

func validStarSourceGeometry(s StarMapSource, w, h int) bool {
	return starFinite(s.X) && starFinite(s.Y) && s.X >= 0 && s.Y >= 0 && s.X < float64(w) && s.Y < float64(h) && starFinite(s.Radius) && s.Radius > 0 && s.Radius <= 100
}

// starSourcesBlended reports whether two cores overlap closely enough that
// neither can be fit alone.
func starSourcesBlended(a, b StarMapSource) bool {
	return math.Hypot(a.X-b.X, a.Y-b.Y) < 2*math.Max(starSourceFWHM(a), starSourceFWHM(b))
}

// starBlendCompanionOnly reports whether a source that the map did not
// accept should still be modelled when it sits inside an accepted star's
// core: the detector's "uncertain" verdict, typically an unresolved blend, is
// not an explicit rejection. Such a member is fit jointly but never treated
// on its own.
func starBlendCompanionOnly(s StarMapSource) bool {
	return !s.Accepted() && s.Override != "reject" && s.Status == "uncertain"
}

// starBlendGroups clusters accepted sources with overlapping cores, then
// attaches companion-only sources that overlap an accepted member directly
// (no chaining through unaccepted sources). The result maps every member ID
// to its group (sorted by ID); a group needs an accepted member and at least
// two members in total.
func starBlendGroups(sources []StarMapSource, w, h int) map[int][]StarMapSource {
	var valid []StarMapSource
	var extra []StarMapSource
	for _, s := range sources {
		if !validStarSourceGeometry(s, w, h) {
			continue
		}
		if s.Accepted() {
			valid = append(valid, s)
		} else if starBlendCompanionOnly(s) {
			extra = append(extra, s)
		}
	}
	parent := make([]int, len(valid)+len(extra))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i := range valid {
		for j := i + 1; j < len(valid); j++ {
			if valid[i].ID != valid[j].ID && starSourcesBlended(valid[i], valid[j]) {
				parent[find(i)] = find(j)
			}
		}
	}
	// A companion-only source attaches on the accepted star's own scale; the
	// map's width for an unresolved blend is a placeholder.
	attached := make([]bool, len(extra))
	for k, e := range extra {
		for i := range valid {
			if valid[i].ID != e.ID && math.Hypot(valid[i].X-e.X, valid[i].Y-e.Y) < 2*starSourceFWHM(valid[i]) {
				parent[find(len(valid)+k)] = find(i)
				attached[k] = true
			}
		}
	}
	byRoot := map[int][]StarMapSource{}
	for i, s := range valid {
		r := find(i)
		byRoot[r] = append(byRoot[r], s)
	}
	for k, s := range extra {
		if attached[k] {
			r := find(len(valid) + k)
			byRoot[r] = append(byRoot[r], s)
		}
	}
	out := map[int][]StarMapSource{}
	for _, members := range byRoot {
		if len(members) < 2 {
			continue
		}
		sort.Slice(members, func(a, b int) bool { return members[a].ID < members[b].ID })
		for _, s := range members {
			out[s.ID] = members
		}
	}
	return out
}

func starProfileShape(r2, width, beta float64) float64 {
	if beta > 0 {
		return math.Pow(1+r2/(width*width), -beta)
	}
	return math.Exp(-.5 * r2 / (width * width))
}

// starTreatmentCompanionExcess returns the modelled light of a fit's blended
// companions at an absolute pixel position. The second result is false inside
// a companion's clipped core, where no model exists.
func starTreatmentCompanionExcess(x, y float64, f StarTreatmentFit) (float64, bool) {
	sum := 0.
	for _, c := range f.Companions {
		dx, dy := x-c.X, y-c.Y
		r2 := dx*dx + dy*dy
		if c.CoreRadius > 0 && r2 < c.CoreRadius*c.CoreRadius {
			return sum, false
		}
		sum += c.Signal * starProfileShape(r2, c.Sigma, c.Beta)
	}
	return sum, true
}

func starTreatmentIsCompanion(f StarTreatmentFit, id int) bool {
	for _, c := range f.Companions {
		if c.SourceID == id {
			return true
		}
	}
	return false
}

// solveNonNegative solves the normal equations a·x=b with x >= 0 by dropping
// negative members from the active set until the remaining solution is
// non-negative. Dropped members return zero.
func solveNonNegative(a [][]float64, b []float64) ([]float64, bool) {
	n := len(b)
	active := make([]bool, n)
	for i := range active {
		active[i] = true
	}
	x := make([]float64, n)
	// A profile with no support inside the fitted region cannot be solved
	// for; it simply contributes nothing.
	for i := 0; i < n; i++ {
		if a[i][i] <= 1e-12 {
			active[i] = false
		}
	}
	for {
		var idx []int
		for i := 0; i < n; i++ {
			if active[i] {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 {
			return x, true
		}
		sub := make([][]float64, len(idx))
		rhs := make([]float64, len(idx))
		for i, r := range idx {
			sub[i] = make([]float64, len(idx))
			for j, c := range idx {
				sub[i][j] = a[r][c]
			}
			rhs[i] = b[r]
		}
		sol, ok := solveLinear(sub, rhs)
		if !ok {
			return nil, false
		}
		worst, worstValue := -1, 0.
		for i, v := range sol {
			if v < worstValue {
				worst, worstValue = idx[i], v
			}
		}
		if worst < 0 {
			for i := range x {
				x[i] = 0
			}
			for i, r := range idx {
				x[r] = sol[i]
			}
			return x, true
		}
		active[worst] = false
	}
}

func solveLinear(a [][]float64, b []float64) ([]float64, bool) {
	n := len(b)
	for i := 0; i < n; i++ {
		p := i
		for j := i + 1; j < n; j++ {
			if math.Abs(a[j][i]) > math.Abs(a[p][i]) {
				p = j
			}
		}
		if math.Abs(a[p][i]) < 1e-12 {
			return nil, false
		}
		a[i], a[p] = a[p], a[i]
		b[i], b[p] = b[p], b[i]
		for j := i + 1; j < n; j++ {
			q := a[j][i] / a[i][i]
			for k := i; k < n; k++ {
				a[j][k] -= q * a[i][k]
			}
			b[j] -= q * b[i]
		}
	}
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		x[i] = b[i]
		for j := i + 1; j < n; j++ {
			x[i] -= a[i][j] * x[j]
		}
		x[i] /= a[i][i]
	}
	return x, true
}

// fitBlendedStarGroup fits overlapping stars jointly: one background plane
// from an annulus around the whole group, then simultaneous non-negative
// amplitudes for fixed-center profiles with per-member widths. Saturated
// members exclude their clipped core from the fit, as in the single-star
// wing fit. Every member receives a fit; members without their own usable
// model are still carried as companions so the others' footprints can cover
// them. Companion-only (unaccepted) members never enlarge the fitted region
// and take the field PSF as their width prior, because the map's width for an
// unresolved blend is a placeholder. One fit is returned per member, in
// member order.
func fitBlendedStarGroup(ctx context.Context, members []StarMapSource, sources []StarMapSource, fieldFWHM float64, pixels []float32, w, h int, opt StarTreatmentOptions) []StarTreatmentFit {
	n := len(members)
	fits := make([]StarTreatmentFit, n)
	for i, s := range members {
		fits[i] = StarTreatmentFit{SourceID: s.ID, X: s.X, Y: s.Y, GroupID: members[0].ID, Saturated: s.Saturated}
	}
	fail := func(reason string) []StarTreatmentFit {
		for i := range fits {
			fits[i].Usable = false
			fits[i].Reason = reason
		}
		return fits
	}
	if n > maxStarBlendMembers {
		return fail("blended group has too many members")
	}
	member := map[int]bool{}
	sigma0 := make([]float64, n)
	support := make([]float64, n)
	core := make([]float64, n)
	anySaturated := false
	cx, cy := 0., 0.
	// Width prior for companion-only members: the field PSF, else the
	// narrowest accepted member.
	priorFWHM := fieldFWHM
	if !starFinite(priorFWHM) || priorFWHM <= 0 {
		for _, s := range members {
			if s.Accepted() && (priorFWHM <= 0 || starSourceFWHM(s) < priorFWHM) {
				priorFWHM = starSourceFWHM(s)
			}
		}
	}
	accepted := 0
	for i, s := range members {
		member[s.ID] = true
		fwhm := starSourceFWHM(s)
		if !s.Accepted() {
			fwhm = priorFWHM
			sigma0[i] = math.Max(fwhm/2.355, .65)
			fits[i].Sigma = sigma0[i]
			support[i] = 0 // never defines the fitted region
			if s.Saturated {
				anySaturated = true
				core[i] = math.Max(1.5, .9*fwhm)
			}
			cx += s.X / float64(n)
			cy += s.Y / float64(n)
			continue
		}
		accepted++
		sigma0[i] = math.Max(fwhm/2.355, .65)
		fits[i].Sigma = sigma0[i]
		if s.Saturated {
			anySaturated = true
			support[i] = math.Min(32, math.Max(opt.OuterRadius, math.Max(1.5*s.Radius, 6*sigma0[i])))
			core[i] = math.Max(1.5, .9*fwhm)
			if support[i] <= 2.5*sigma0[i] || core[i] >= support[i]*.55 {
				return fail("blended saturated member has insufficient wing span")
			}
		} else {
			support[i] = math.Min(opt.OuterRadius, math.Max(1.25*s.Radius, 4*sigma0[i]))
			if support[i] <= 2*sigma0[i] {
				return fail("blended member has insufficient bounded support")
			}
		}
		cx += s.X / float64(n)
		cy += s.Y / float64(n)
	}
	if accepted == 0 {
		return fail("blended group has no accepted member")
	}
	outer := 0.
	for i, s := range members {
		reach := support[i]
		if !s.Accepted() {
			reach = math.Max(2*sigma0[i], core[i])
		}
		outer = math.Max(outer, math.Hypot(s.X-cx, s.Y-cy)+reach)
	}
	if outer > maxStarBlendOuter {
		return fail("blended group support is too large")
	}
	if cx-1.9*outer < 0 || cy-1.9*outer < 0 || cx+1.9*outer >= float64(w) || cy+1.9*outer >= float64(h) {
		return fail("truncated background annulus")
	}
	bg, bx, by, noise, ok := saturatedWingBackground(ctx, cx, cy, outer, func(id int) bool { return member[id] }, sources, pixels, w, h, opt.MinSamples)
	if !ok || ctx.Err() != nil {
		return fail("insufficient background samples")
	}
	plane := func(x, y float64) float64 { return bg + bx*(x-cx) + by*(y-cy) }
	for i := range fits {
		fits[i].Background = plane(fits[i].X, fits[i].Y)
		fits[i].BackgroundX, fits[i].BackgroundY, fits[i].Noise = bx, by, noise
	}
	for i, s := range members {
		if !s.Saturated {
			continue
		}
		span := support[i]
		if !s.Accepted() {
			span = outer
		}
		if measured := saturatedPlateauRadius(ctx, s.X, s.Y, core[i], span, fits[i].Background, bx, by, noise, pixels, w, h); measured > core[i] {
			core[i] = measured
		}
		if ctx.Err() != nil {
			return fail("cancelled")
		}
		if s.Accepted() && core[i] >= support[i]*.55 {
			return fail("blended saturated member has insufficient wing span")
		}
		fits[i].CoreRadius = core[i]
	}
	// A companion-only source inside an accepted member's clipped core is a
	// detection on the saturated plateau, not a star to model.
	dropped := make([]bool, n)
	for i, s := range members {
		if s.Accepted() {
			continue
		}
		for j, other := range members {
			if j != i && other.Accepted() && other.Saturated && math.Hypot(s.X-other.X, s.Y-other.Y) < core[j] {
				dropped[i] = true
				fits[i].Reason = "inside a saturated member's clipped core; not modelled"
			}
		}
	}
	// Samples inside any member's support, outside every clipped core.
	var excess []float64
	var sampleDX, sampleDY []float64 // relative to the group center, for the balance test
	var r2 []float64                 // flattened: r2[k*n+i] is sample k's squared distance to member i
	for y := max(0, int(math.Floor(cy-outer))); y <= min(h-1, int(math.Ceil(cy+outer))); y++ {
		if ctx.Err() != nil {
			return fail("cancelled")
		}
		for x := max(0, int(math.Floor(cx-outer))); x <= min(w-1, int(math.Ceil(cx+outer))); x++ {
			inside, clipped := false, false
			var d [maxStarBlendMembers]float64
			for i, s := range members {
				dx, dy := float64(x)-s.X, float64(y)-s.Y
				d[i] = dx*dx + dy*dy
				r := math.Sqrt(d[i])
				if s.Saturated && !dropped[i] && r < core[i] {
					clipped = true
					break
				}
				if s.Accepted() && r <= support[i] {
					inside = true
				}
			}
			if clipped || !inside {
				continue
			}
			v := float64(pixels[y*w+x])
			if !starFinite(v) {
				continue
			}
			e := v - plane(float64(x), float64(y))
			// As in the single-star wing fit, a saturated group fits only the
			// observed positive light: zero-coverage holes and negative noise
			// carry no wing information and would read as one-sided misfit.
			if anySaturated && e <= 0 {
				continue
			}
			excess = append(excess, e)
			sampleDX = append(sampleDX, float64(x)-cx)
			sampleDY = append(sampleDY, float64(y)-cy)
			r2 = append(r2, d[:n]...)
		}
	}
	if len(excess) < max(24, opt.MinSamples) {
		return fail("empty stellar support")
	}
	// Widths are searched per member: a map FWHM measured on an unresolved
	// blend is unreliable, so one shared scale cannot describe both stars.
	// Coordinate descent over a bounded grid keeps the cost linear in members.
	scales := make([]float64, 0, 41)
	betas := []float64{0}
	if anySaturated {
		for step := 0; step <= 32; step++ {
			scales = append(scales, .55+float64(step)*.06)
		}
		betas = []float64{0, 1.5, 2.5, 4}
	} else {
		for step := 0; step <= 40; step++ {
			scales = append(scales, .6+float64(step)*.025)
		}
	}
	energy := 0.
	for _, e := range excess {
		energy += e * e
	}
	basis := make([]float64, n)
	memberBeta := func(i int, beta float64) float64 {
		if members[i].Saturated {
			return beta
		}
		return 0
	}
	shape := func(k, i int, width []float64, beta float64) float64 {
		if dropped[i] {
			return 0
		}
		return starProfileShape(r2[k*n+i], width[i], memberBeta(i, beta))
	}
	evaluate := func(width []float64, beta float64) ([]float64, float64) {
		a := make([][]float64, n)
		for i := range a {
			a[i] = make([]float64, n)
		}
		b := make([]float64, n)
		for k := range excess {
			e := excess[k]
			for i := 0; i < n; i++ {
				basis[i] = shape(k, i, width, beta)
			}
			for i := 0; i < n; i++ {
				b[i] += basis[i] * e
				for j := 0; j < n; j++ {
					a[i][j] += basis[i] * basis[j]
				}
			}
		}
		amp, ok := solveNonNegative(a, b)
		if !ok {
			return nil, math.Inf(1)
		}
		deviations := make([]float64, len(excess))
		for k, e := range excess {
			model := 0.
			for i := 0; i < n; i++ {
				model += amp[i] * shape(k, i, width, beta)
			}
			deviations[k] = e - model
		}
		if anySaturated {
			return amp, trimmedWingResidual(deviations, excess, sampleDX, sampleDY)
		}
		loss := 0.
		for _, d := range deviations {
			loss += d * d
		}
		return amp, math.Sqrt(loss / math.Max(energy, 1e-20))
	}
	bestResidual := math.Inf(1)
	var bestAmp, bestWidth []float64
	bestBeta := 0.
	for _, beta := range betas {
		width := make([]float64, n)
		copy(width, sigma0)
		amp, residual := evaluate(width, beta)
		for sweep := 0; sweep < 3; sweep++ {
			improved := false
			for i := 0; i < n; i++ {
				if ctx.Err() != nil {
					return fail("cancelled")
				}
				for _, scale := range scales {
					trial := make([]float64, n)
					copy(trial, width)
					trial[i] = sigma0[i] * scale
					if a, r := evaluate(trial, beta); r < residual {
						amp, residual, width, improved = a, r, trial, true
					}
				}
			}
			if !improved {
				break
			}
		}
		if residual < bestResidual {
			bestResidual, bestAmp, bestWidth, bestBeta = residual, amp, width, beta
		}
	}
	for i := range fits {
		fits[i].Residual = bestResidual
	}
	limit := .25
	if anySaturated {
		limit = .35
	}
	if bestAmp == nil || !starFinite(bestResidual) || bestResidual > limit {
		return fail("blended profile does not match circular model")
	}
	for i := range fits {
		fits[i].Signal, fits[i].Sigma = bestAmp[i], bestWidth[i]
		for j := range members {
			if j != i && !dropped[j] && bestAmp[j] > 0 {
				fits[i].Companions = append(fits[i].Companions, StarTreatmentCompanion{SourceID: members[j].ID, X: members[j].X, Y: members[j].Y, Signal: bestAmp[j], Sigma: bestWidth[j], Beta: memberBeta(j, bestBeta), CoreRadius: core[j]})
			}
		}
	}
	for i, s := range members {
		if !s.Accepted() {
			if !dropped[i] {
				fits[i].Reason = "not accepted; modelled as a blended companion only"
			}
			continue
		}
		if bestAmp[i] <= 0 {
			fits[i].Reason = "blended member has no positive amplitude; covered by neighbor's footprint"
			continue
		}
		if s.Saturated {
			snr, reason := validateBlendedWings(ctx, fits[i], core[i], support[i], pixels, w, h)
			if reason != "" {
				fits[i].Reason = reason
				continue
			}
			fits[i].SNR = snr
			fits[i].OuterRadius = support[i]
			fits[i].WingValidated = true
			fits[i].WingModel = "Gaussian"
			if bestBeta > 0 {
				fits[i].WingModel = "Moffat"
			}
		} else {
			fits[i].SNR = fits[i].Signal / math.Max(noise, 1e-9)
			fits[i].OuterRadius = measureStarOuterRadius(ctx, fits[i], support[i], pixels, w, h)
		}
		if fits[i].SNR < opt.MinSNR {
			fits[i].Reason = "low stellar contrast; covered by blended neighbor's footprint"
			continue
		}
		fits[i].Usable = true
		fits[i].Reason = fmt.Sprintf("jointly fit with %d blended neighbor(s); outer halo/spikes not validated", n-1)
	}
	return fits
}

// validateBlendedWings applies the single-star saturated wing checks to the
// companion-subtracted residual: declining radial bins outside the clipped
// core and wing energy in every quadrant. It returns the wing SNR and an empty
// reason on success.
func validateBlendedWings(ctx context.Context, f StarTreatmentFit, core, outer float64, pixels []float32, w, h int) (float64, string) {
	var radial [][]float64
	for i := 0; i < int(math.Ceil(outer-core)); i++ {
		radial = append(radial, nil)
	}
	var qsum [4]float64
	var qcount [4]int
	for y := max(0, int(math.Floor(f.Y-outer))); y <= min(h-1, int(math.Ceil(f.Y+outer))); y++ {
		if ctx.Err() != nil {
			return 0, "cancelled"
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
			companion, ok := starTreatmentCompanionExcess(float64(x), float64(y), f)
			if !ok {
				continue
			}
			e := v - (f.Background + f.BackgroundX*dx + f.BackgroundY*dy) - companion
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
			qsum[q] += e
			if e > 3*f.Noise {
				qcount[q]++
				if bin := int(math.Floor(r - core)); bin >= 0 && bin < len(radial) {
					radial[bin] = append(radial[bin], e)
				}
			}
		}
	}
	for _, c := range qcount {
		if c < 3 {
			return 0, "blended saturated member has insufficient spatially distributed wings"
		}
	}
	var profile []float64
	for _, bin := range radial {
		if len(bin) >= 4 {
			profile = append(profile, starMedian(bin))
		}
	}
	if len(profile) < 3 || profile[len(profile)-1] > .65*profile[0] {
		return 0, "blended saturated member has no measurable declining wings"
	}
	total := qsum[0] + qsum[1] + qsum[2] + qsum[3]
	minQ, maxQ := qsum[0], qsum[0]
	for _, v := range qsum[1:] {
		minQ, maxQ = math.Min(minQ, v), math.Max(maxQ, v)
	}
	for _, v := range qsum {
		if v < .02*total || minQ <= 0 || maxQ > 10*minQ {
			return 0, "blended saturated member wings are asymmetric or contaminated"
		}
	}
	return profile[0] / math.Max(f.Noise, 1e-9), ""
}

// measureStarOuterRadius finds the usable outer support from radial residuals
// instead of treating the catalog radius as the treatment boundary. A bin must
// carry positive companion-subtracted signal above the detrended noise floor;
// the final extra sigma provides a continuous transition into the feather.
func measureStarOuterRadius(ctx context.Context, f StarTreatmentFit, outer float64, pixels []float32, w, h int) float64 {
	measured := 2 * f.Sigma
	for r0 := f.Sigma; r0 < outer; r0 += .5 {
		if ctx.Err() != nil {
			break
		}
		var sum float64
		count := 0
		for y := max(0, int(math.Floor(f.Y-r0-1))); y <= min(h-1, int(math.Ceil(f.Y+r0+1))); y++ {
			for x := max(0, int(math.Floor(f.X-r0-1))); x <= min(w-1, int(math.Ceil(f.X+r0+1))); x++ {
				dx, dy := float64(x)-f.X, float64(y)-f.Y
				r := math.Hypot(dx, dy)
				if r < r0 || r >= r0+.5 {
					continue
				}
				companion, ok := starTreatmentCompanionExcess(float64(x), float64(y), f)
				if !ok {
					continue
				}
				v := float64(pixels[y*w+x]) - f.Background - f.BackgroundX*dx - f.BackgroundY*dy - companion
				if starFinite(v) {
					sum += v
					count++
				}
			}
		}
		if count > 0 && sum/float64(count) > 2*f.Noise {
			measured = r0 + .5
		}
	}
	return math.Min(outer, math.Max(2*f.Sigma, measured+f.Sigma))
}

// nearbyStarSources returns the accepted sources (other than excluded ones)
// whose exclusion disk can touch a region of the given radius around a
// center, so per-pixel neighbor tests scan a short list instead of the map.
func nearbyStarSources(cx, cy, radius float64, excluded func(int) bool, sources []StarMapSource) []StarMapSource {
	var out []StarMapSource
	for _, other := range sources {
		if excluded(other.ID) || !other.Accepted() || !starFinite(other.X) || !starFinite(other.Y) {
			continue
		}
		if !starFinite(other.Radius) || other.Radius <= 0 {
			other.Radius = 3
		}
		if math.Hypot(other.X-cx, other.Y-cy) <= radius+other.Radius+1 {
			out = append(out, other)
		}
	}
	return out
}

func nearStarSource(x, y float64, nearby []StarMapSource) bool {
	for _, other := range nearby {
		if math.Hypot(x-other.X, y-other.Y) < other.Radius {
			return true
		}
	}
	return false
}
