package processing

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// StarTreatmentOptions controls the bounded linear fit used by the Wite-Star
// renderer.  The fit is deliberately local: it models a star and its local
// plane, never the whole cutout.
type StarTreatmentOptions struct {
	MinSNR      float64
	MinSamples  int
	OuterRadius float64
	Progress    func(string, int, int)
}

// StarTreatmentFit is the linear star/background model for one catalog source.
// A false Usable value means the source is intentionally skipped.
type StarTreatmentFit struct {
	SourceID                             int
	X, Y                                 float64
	Background, BackgroundX, BackgroundY float64
	Signal, Sigma, OuterRadius           float64
	InnerRadius                          float64 // prepared visible footprint; zero uses the default taper
	Noise, SNR                           float64
	Residual                             float64
	CoreRadius                           float64 // excluded saturated core; covered at full mask strength
	Usable                               bool
	Saturated                            bool
	WingValidated                        bool
	WingModel                            string
	// Extended components are populated only after independent validation by
	// PrepareStarStretchFits. They are deliberately separate from OuterRadius
	// so an unsafe halo or spike can never enlarge the ordinary core mask.
	HaloValidated                                                bool
	HaloInnerRadius                                              float64
	HaloRadius                                                   float64
	ExtendedBackground, ExtendedBackgroundX, ExtendedBackgroundY float64
	ExtendedNoise                                                float64
	Spikes                                                       []StarTreatmentSpike
	ExtendedCoverage                                             string
	Reason                                                       string
	// Blended sources are fit jointly (see fitBlendedStarGroup). GroupID is
	// the smallest member ID; Companions hold the other members' profiles so
	// footprint tests subtract a modelled neighbor instead of rejecting it.
	GroupID    int
	Companions []StarTreatmentCompanion
}

// StarTreatmentSpike describes one validated diffraction-spike arm. Angle is
// measured in image coordinates (positive x, positive y), and the opposite
// arm is represented separately so paired validation remains explicit.
type StarTreatmentSpike struct {
	Angle, StartRadius, EndRadius, Width float64
}

const maxStarTreatmentExtent = 192.0

func defaultStarTreatmentOptions(o StarTreatmentOptions) StarTreatmentOptions {
	if o.MinSNR == 0 {
		o.MinSNR = 3
	}
	if o.MinSamples == 0 {
		o.MinSamples = 20
	}
	if o.OuterRadius == 0 {
		o.OuterRadius = 16
	}
	return o
}

// FitStarTreatment estimates a non-negative stellar increment above a local
// planar background from linear pixels. Only accepted map sources are fit.
func FitStarTreatment(ctx context.Context, m *StarMap, pixels []float32, w, h int, opt StarTreatmentOptions) ([]StarTreatmentFit, error) {
	if m == nil || !validStarTreatmentDimensions(w, h) || m.Width != w || m.Height != h || len(pixels) != w*h {
		return nil, fmt.Errorf("invalid star treatment dimensions")
	}
	opt = defaultStarTreatmentOptions(opt)
	if !starFinite(opt.MinSNR) || opt.MinSNR < 0 || opt.MinSamples < 6 || !starFinite(opt.OuterRadius) || opt.OuterRadius <= 0 || opt.OuterRadius > 100 {
		return nil, fmt.Errorf("invalid star treatment options")
	}
	out := make([]StarTreatmentFit, 0, len(m.Sources))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	groups := starBlendGroups(m.Sources, w, h)
	groupFits := map[int]StarTreatmentFit{}
	for n, s := range m.Sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if opt.Progress != nil {
			opt.Progress("fitting stars", n, len(m.Sources))
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f := StarTreatmentFit{SourceID: s.ID, X: s.X, Y: s.Y, Reason: "not accepted"}
		if !s.Accepted() {
			out = append(out, f)
			continue
		}
		if !starFinite(s.X) || !starFinite(s.Y) || s.X < 0 || s.Y < 0 || s.X >= float64(w) || s.Y >= float64(h) || !starFinite(s.Radius) || s.Radius <= 0 || s.Radius > 100 {
			f.Reason = "invalid center"
			out = append(out, f)
			continue
		}
		if members, ok := groups[s.ID]; ok {
			if _, done := groupFits[s.ID]; !done {
				for _, gf := range fitBlendedStarGroup(ctx, members, m.Sources, m.FWHM, pixels, w, h, opt) {
					groupFits[gf.SourceID] = gf
				}
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			out = append(out, groupFits[s.ID])
			continue
		}
		if s.Saturated {
			f.Saturated = true
			f, _ = fitSaturatedStarWings(ctx, f, s, m.Sources, pixels, w, h, opt)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			out = append(out, f)
			continue
		}
		f.Sigma = math.Max(starSourceFWHM(s)/2.355, .65)
		outer := math.Max(1.25*s.Radius, 4*f.Sigma)
		outer = math.Min(outer, opt.OuterRadius)
		if outer <= 2*f.Sigma {
			f.Reason = "insufficient bounded support"
			out = append(out, f)
			continue
		}
		if f.X-1.9*outer < 0 || f.Y-1.9*outer < 0 || f.X+1.9*outer >= float64(w) || f.Y+1.9*outer >= float64(h) {
			f.Reason = "truncated background annulus"
			out = append(out, f)
			continue
		}
		// Fit a plane in an annulus, keeping the stellar core out of the model.
		var a [3][3]float64
		var b [3]float64
		var vals []float64
		var sampleDX, sampleDY []float64
		nearby := nearbyStarSources(f.X, f.Y, outer*1.9, func(id int) bool { return id == s.ID }, m.Sources)
		for y := max(0, int(math.Floor(f.Y-outer*2))); y <= min(h-1, int(math.Ceil(f.Y+outer*2))); y++ {
			for x := max(0, int(math.Floor(f.X-outer*2))); x <= min(w-1, int(math.Ceil(f.X+outer*2))); x++ {
				dx, dy := float64(x)-f.X, float64(y)-f.Y
				r := math.Hypot(dx, dy)
				if r < outer*1.35 || r > outer*1.9 {
					continue
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
				vals = append(vals, v)
				sampleDX = append(sampleDX, dx)
				sampleDY = append(sampleDY, dy)
			}
		}
		if len(vals) < opt.MinSamples {
			f.Reason = "insufficient background samples"
			out = append(out, f)
			continue
		}
		plane, ok := solve3(a, b)
		if !ok {
			f.Reason = "unstable background fit"
			out = append(out, f)
			continue
		}
		// Iteratively reject annular contaminants rather than letting an isolated
		// hot pixel or nebular knot pull the background plane toward the star.
		for iteration := 0; iteration < 3; iteration++ {
			residuals := make([]float64, len(vals))
			for i, v := range vals {
				residuals[i] = math.Abs(v - plane[0] - plane[1]*sampleDX[i] - plane[2]*sampleDY[i])
			}
			limit := math.Max(4*1.4826*starMedian(residuals), 1e-8)
			a, b = [3][3]float64{}, [3]float64{}
			kept := 0
			for i, v := range vals {
				if math.Abs(v-plane[0]-plane[1]*sampleDX[i]-plane[2]*sampleDY[i]) > limit {
					continue
				}
				q := [3]float64{1, sampleDX[i], sampleDY[i]}
				for j := 0; j < 3; j++ {
					b[j] += q[j] * v
					for k := 0; k < 3; k++ {
						a[j][k] += q[j] * q[k]
					}
				}
				kept++
			}
			if kept < opt.MinSamples {
				ok = false
				break
			}
			plane, ok = solve3(a, b)
			if !ok {
				break
			}
		}
		if !ok {
			f.Reason = "insufficient robust background samples"
			out = append(out, f)
			continue
		}
		f.Background, f.BackgroundX, f.BackgroundY = plane[0], plane[1], plane[2]
		dev := make([]float64, len(vals))
		for i := range vals {
			// vals are annular samples; remove the fitted local slope.
			dev[i] = math.Abs(vals[i] - (f.Background + f.BackgroundX*sampleDX[i] + f.BackgroundY*sampleDY[i]))
		}
		sort.Float64s(dev)
		f.Noise = math.Max(1.4826*dev[len(dev)/2], 1e-9)
		type profileSample struct{ r2, v float64 }
		var samples []profileSample
		for y := max(0, int(math.Floor(f.Y-outer))); y <= min(h-1, int(math.Ceil(f.Y+outer))); y++ {
			for x := max(0, int(math.Floor(f.X-outer))); x <= min(w-1, int(math.Ceil(f.X+outer))); x++ {
				dx, dy := float64(x)-f.X, float64(y)-f.Y
				r := math.Hypot(dx, dy)
				if r > outer {
					continue
				}
				v := float64(pixels[y*w+x]) - f.Background - f.BackgroundX*dx - f.BackgroundY*dy
				if !starFinite(v) {
					continue
				}
				samples = append(samples, profileSample{r * r, v})
			}
		}
		if len(samples) < opt.MinSamples {
			f.Reason = "empty stellar support"
			out = append(out, f)
			continue
		}
		// Measure width rather than assuming the detector's FWHM describes this
		// local linear profile exactly. Circular Gaussian is the initial model;
		// a poor match is surfaced instead of treated as a reliable footprint.
		bestLoss := math.Inf(1)
		initialSigma := f.Sigma
		for step := 0; step <= 40; step++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			sigma := initialSigma * (.6 + float64(step)*.025)
			dot, norm := 0., 0.
			for _, a := range samples {
				p := math.Exp(-.5 * a.r2 / (sigma * sigma))
				dot += p * a.v
				norm += p * p
			}
			amplitude := math.Max(0, dot/math.Max(norm, 1e-20))
			loss, energy := 0., 0.
			for _, a := range samples {
				d := a.v - amplitude*math.Exp(-.5*a.r2/(sigma*sigma))
				loss += d * d
				energy += a.v * a.v
			}
			residual := math.Sqrt(loss / math.Max(energy, 1e-20))
			if residual < bestLoss {
				bestLoss = residual
				f.Signal = amplitude
				f.Sigma = sigma
			}
		}
		f.Residual = bestLoss
		if !starFinite(f.Signal) || f.Signal <= 0 || bestLoss > .25 {
			f.Reason = "stellar profile does not match circular model"
			out = append(out, f)
			continue
		}
		f.SNR = f.Signal / math.Max(f.Noise, 1e-9)
		f.OuterRadius = measureStarOuterRadius(ctx, f, outer, pixels, w, h)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !starFinite(f.Signal) || f.Signal <= 0 || f.SNR < opt.MinSNR {
			f.Reason = "low stellar contrast"
			out = append(out, f)
			continue
		}
		f.Usable = true
		f.Reason = "bounded circular Gaussian fit; outer halo/spikes not validated"
		out = append(out, f)
	}
	return out, nil
}

// BuildStarTreatmentMask creates a bounded cosine feather. Overlapping stars
// are combined by maximum support, so a pixel is never treated twice.
func BuildStarTreatmentMask(ctx context.Context, fits []StarTreatmentFit, w, h int) ([]float32, error) {
	if !validStarTreatmentDimensions(w, h) {
		return nil, fmt.Errorf("invalid mask dimensions")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mask := make([]float32, w*h)
	for _, f := range fits {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !f.Usable {
			continue
		}
		if !validStarTreatmentFit(f, w, h) {
			return nil, fmt.Errorf("star %d has invalid mask geometry", f.SourceID)
		}
		extent := StarTreatmentExtent(f)
		for y := max(0, int(math.Floor(f.Y-extent))); y <= min(h-1, int(math.Ceil(f.Y+extent))); y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for x := max(0, int(math.Floor(f.X-extent))); x <= min(w-1, int(math.Ceil(f.X+extent))); x++ {
				v := starTreatmentFitWeight(float64(x), float64(y), f)
				if float32(v) > mask[y*w+x] {
					mask[y*w+x] = float32(v)
				}
			}
		}
	}
	return mask, ctx.Err()
}

func solve3(a [3][3]float64, b [3]float64) ([3]float64, bool) {
	for i := 0; i < 3; i++ {
		p := i
		for j := i + 1; j < 3; j++ {
			if math.Abs(a[j][i]) > math.Abs(a[p][i]) {
				p = j
			}
		}
		if math.Abs(a[p][i]) < 1e-12 {
			return [3]float64{}, false
		}
		a[i], a[p] = a[p], a[i]
		b[i], b[p] = b[p], b[i]
		for j := i + 1; j < 3; j++ {
			q := a[j][i] / a[i][i]
			for k := i; k < 3; k++ {
				a[j][k] -= q * a[i][k]
			}
			b[j] -= q * b[i]
		}
	}
	var x [3]float64
	for i := 2; i >= 0; i-- {
		x[i] = b[i]
		for j := i + 1; j < 3; j++ {
			x[i] -= a[i][j] * x[j]
		}
		x[i] /= a[i][i]
	}
	return x, true
}
