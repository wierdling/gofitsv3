package processing

import (
	"context"
	"fmt"
	"math"

	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

type StarStretchOptions struct {
	Strength float64
	Progress func(string, int, int)
}

// ApplyGentlerStarStretch compresses only the observed stellar excess above
// each fitted local background. Unsafe models reject the entire render, never
// individual corrected pixels. Samples that clip the ordinary display stretch
// are evaluated with an unbounded scalar curve, so a clipped core can still be
// treated when a saturated source has a validated wing fit. Strength zero is
// the ordinary scalar rendering.
func ApplyGentlerStarStretch(ctx context.Context, linear []float32, w, h int, meta models.LoadedImage, fits []StarTreatmentFit, mask []float32, opt StarStretchOptions) ([]float32, error) {
	if !validStarTreatmentDimensions(w, h) || len(linear) != w*h {
		return nil, fmt.Errorf("invalid star stretch dimensions")
	}
	if len(mask) != 0 && len(mask) != len(linear) {
		return nil, fmt.Errorf("star stretch mask dimensions differ")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateStarStretchMeta(meta); err != nil {
		return nil, err
	}
	if !starFinite(opt.Strength) || opt.Strength < 0 || opt.Strength > 1 {
		return nil, fmt.Errorf("star stretch strength must be between 0 and 1")
	}
	for i, v := range mask {
		if i%65536 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if !starFinite(float64(v)) || v < 0 || v > 1 {
			return nil, fmt.Errorf("invalid treatment mask value")
		}
	}
	model, err := NewStarTreatmentModel(fits, w, h, meta, opt.Strength)
	if err != nil {
		return nil, err
	}
	out := make([]float32, len(linear))
	for i, v := range linear {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		maskWeight := -1.
		if len(mask) > 0 {
			maskWeight = float64(mask[i])
		}
		out[i] = model.stretchAt(v, float64(i%w), float64(i/w), maskWeight)
	}
	if opt.Progress != nil {
		opt.Progress("Rendering stellar increments", len(linear), len(linear))
	}
	return out, ctx.Err()
}

func starTreatmentBackground(x, y float64, f StarTreatmentFit) float64 {
	dx, dy := x-f.X, y-f.Y
	base := f.Background + f.BackgroundX*dx + f.BackgroundY*dy
	if !f.HaloValidated && len(f.Spikes) == 0 {
		return base
	}
	extended := f.ExtendedBackground + f.ExtendedBackgroundX*dx + f.ExtendedBackgroundY*dy
	r := math.Hypot(dx, dy)
	inner := f.InnerRadius
	if inner <= 0 {
		inner = .62 * f.OuterRadius
	}
	if r <= inner {
		return base
	}
	if r >= f.OuterRadius {
		return extended
	}
	t := (r - inner) / (f.OuterRadius - inner)
	t = t * t * (3 - 2*t)
	return base + t*(extended-base)
}

func validStarTreatmentDimensions(w, h int) bool { return w > 0 && h > 0 && w <= int(^uint(0)>>1)/h }

func validStarTreatmentFit(f StarTreatmentFit, w, h int) bool {
	for _, v := range []float64{f.X, f.Y, f.Background, f.BackgroundX, f.BackgroundY, f.Signal, f.Sigma, f.OuterRadius, f.InnerRadius, f.CoreRadius, f.Noise} {
		if !starFinite(v) {
			return false
		}
	}
	if !(f.X >= 0 && f.Y >= 0 && f.X < float64(w) && f.Y < float64(h) && f.Signal > 0 && f.Sigma > 0 && f.OuterRadius > 0 && f.OuterRadius <= maxStarTreatmentExtent && f.InnerRadius >= 0 && f.InnerRadius < f.OuterRadius && f.CoreRadius >= 0 && f.CoreRadius < f.OuterRadius && f.Noise >= 0 && (!f.Saturated || (f.WingValidated && f.CoreRadius > 0))) {
		return false
	}
	for _, v := range []float64{f.HaloInnerRadius, f.HaloRadius, f.ExtendedBackground, f.ExtendedBackgroundX, f.ExtendedBackgroundY, f.ExtendedNoise} {
		if !starFinite(v) {
			return false
		}
	}
	if f.HaloValidated && (f.HaloInnerRadius < f.OuterRadius || f.HaloInnerRadius >= f.HaloRadius || f.HaloRadius > maxStarTreatmentExtent) {
		return false
	}
	if f.ExtendedNoise < 0 || ((f.HaloValidated || len(f.Spikes) > 0) && f.ExtendedNoise <= 0) {
		return false
	}
	for _, s := range f.Spikes {
		if !validStarTreatmentSpike(s, f.OuterRadius) {
			return false
		}
	}
	for _, c := range f.Companions {
		for _, v := range []float64{c.X, c.Y, c.Signal, c.Sigma, c.Beta, c.CoreRadius} {
			if !starFinite(v) {
				return false
			}
		}
		if c.SourceID == f.SourceID || c.Signal <= 0 || c.Sigma <= 0 || c.Beta < 0 || c.CoreRadius < 0 {
			return false
		}
	}
	return true
}

func validStarTreatmentSpike(s StarTreatmentSpike, coreOuter float64) bool {
	for _, v := range []float64{s.Angle, s.StartRadius, s.EndRadius, s.Width} {
		if !starFinite(v) {
			return false
		}
	}
	return s.StartRadius >= 0 && s.StartRadius < coreOuter && s.EndRadius > coreOuter && math.Hypot(s.EndRadius, s.Width) <= maxStarTreatmentExtent && s.Width > 0 && s.Width <= 12 && s.Width < s.EndRadius-s.StartRadius
}

// StarTreatmentExtent returns the largest radial extent represented by a fit,
// including any validated halo or diffraction-spike arms.
func StarTreatmentExtent(f StarTreatmentFit) float64 {
	extent := f.OuterRadius
	if f.HaloValidated {
		extent = math.Max(extent, f.HaloRadius)
	}
	for _, s := range f.Spikes {
		extent = math.Max(extent, math.Hypot(s.EndRadius, s.Width))
	}
	return extent
}

func starTreatmentFeather(r, outer float64) float64 {
	if r >= outer {
		return 0
	}
	inner := outer * .62
	if r <= inner {
		return 1
	}
	return .5 + .5*math.Cos(math.Pi*(r-inner)/(outer-inner))
}

// unclippedStarStretch matches the scalar renderer through its normal display
// range. MTF continues above one with its endpoint tangent, avoiding the rational
// function's possible pole while retaining the linear MTF(.5) case exactly.
func unclippedStarStretch(v float64, meta models.LoadedImage) float64 {
	n := math.Max(0, (v-meta.Background)/(meta.Peak-meta.Background))
	scaled := meta.ScaledPeak
	if scaled <= 0 {
		scaled = 100
	}
	switch meta.Mode {
	case stretch.Log:
		return math.Log1p(n*scaled) / math.Log1p(scaled)
	case stretch.Asinh:
		beta := meta.AsinhScale
		if beta <= 0 {
			beta = stretch.DefaultAsinhScale
		}
		return math.Asinh(n*scaled/beta) / math.Asinh(scaled/beta)
	case stretch.Sqrt:
		return math.Sqrt(n)
	case stretch.MTF:
		m := meta.MTFMidtone
		if m <= 0 || m >= 1 {
			m = stretch.DefaultMTFMidtone
		}
		if n > 1 {
			return 1 + (n-1)*m/(1-m)
		}
		return stretch.Mtf(m, n)
	default:
		return n
	}
}

func starTreatmentFitFeather(r float64, f StarTreatmentFit) float64 {
	inner := f.InnerRadius
	if inner <= 0 {
		inner = .62 * f.OuterRadius
	}
	if f.Saturated && f.WingValidated {
		inner = math.Max(inner, f.CoreRadius)
	}
	if r >= f.OuterRadius {
		return 0
	}
	if r <= inner {
		return 1
	}
	return .5 + .5*math.Cos(math.Pi*(r-inner)/(f.OuterRadius-inner))
}

// starTreatmentFitWeight is the single geometry function used by the mask and
// renderer. Extended components use smooth longitudinal and transverse tapers;
// the returned value is always a maximum, so overlap cannot double-apply a
// correction.
func starTreatmentFitWeight(x, y float64, f StarTreatmentFit) float64 {
	dx, dy := x-f.X, y-f.Y
	weight := starTreatmentFitFeather(math.Hypot(dx, dy), f)
	if f.HaloValidated {
		weight = math.Max(weight, starTreatmentFitFeather(math.Hypot(dx, dy), StarTreatmentFit{OuterRadius: f.HaloRadius, InnerRadius: f.HaloInnerRadius, Saturated: false}))
	}
	for _, s := range f.Spikes {
		weight = math.Max(weight, starTreatmentSpikeWeight(dx, dy, s))
	}
	return math.Max(0, math.Min(1, weight))
}

func starTreatmentSpikeWeight(dx, dy float64, s StarTreatmentSpike) float64 {
	along := dx*math.Cos(s.Angle) + dy*math.Sin(s.Angle)
	across := math.Abs(-dx*math.Sin(s.Angle) + dy*math.Cos(s.Angle))
	if along <= s.StartRadius || along >= s.EndRadius || across >= s.Width {
		return 0
	}
	join := math.Min(3, (s.EndRadius-s.StartRadius)*.2)
	tail := math.Min(6, (s.EndRadius-s.StartRadius)*.2)
	long := 1.
	if along < s.StartRadius+join {
		long = .5 - .5*math.Cos(math.Pi*(along-s.StartRadius)/join)
	}
	if along > s.EndRadius-tail {
		long = .5 + .5*math.Cos(math.Pi*(along-(s.EndRadius-tail))/tail)
	}
	transverse := 1.
	if across > s.Width*.35 {
		transverse = .5 + .5*math.Cos(math.Pi*(across-s.Width*.35)/(s.Width*.65))
	}
	return long * transverse
}
