package processing

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"

	"gofitsv3/internal/models"
)

// PrepareStarStretchFits extends the treatment's full-strength inner region
// through the measured visible core/halo at the current display settings. The
// fit is copied; the original catalog and linear model are not changed. It must
// precede BuildStarTreatmentMask so the displayed mask matches the renderer.
// A star whose footprint cannot be validated is returned with Usable false and
// a Reason instead of failing the whole set, so one ambiguous source never
// blocks treatment of the rest of the field.
func PrepareStarStretchFits(ctx context.Context, pixels []float32, w, h int, meta models.LoadedImage, fits []StarTreatmentFit, sources []StarMapSource) ([]StarTreatmentFit, error) {
	if !validStarTreatmentDimensions(w, h) || len(pixels) != w*h {
		return nil, fmt.Errorf("invalid footprint dimensions")
	}
	if !starFinite(meta.Peak) || !starFinite(meta.Background) || meta.Peak <= meta.Background {
		return nil, fmt.Errorf("invalid footprint stretch levels")
	}
	out := append([]StarTreatmentFit(nil), fits...)
	for i := range out {
		out[i].Spikes = append([]StarTreatmentSpike(nil), out[i].Spikes...)
	}
	// Stars are independent, so preparation runs in parallel; results keep
	// their input order and the first error wins.
	workers := max(1, runtime.NumCPU())
	jobs := make(chan int)
	errs := make([]error, len(out))
	var wg sync.WaitGroup
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				prepared, err := prepareOneStarStretchFit(ctx, pixels, w, h, meta, out[j], sources)
				out[j], errs[j] = prepared, err
			}
		}()
	}
	for j := range out {
		if ctx.Err() != nil {
			break
		}
		jobs <- j
	}
	close(jobs)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, ctx.Err()
}

// prepareOneStarStretchFit prepares a single fit; see PrepareStarStretchFits.
func prepareOneStarStretchFit(ctx context.Context, pixels []float32, w, h int, meta models.LoadedImage, f StarTreatmentFit, sources []StarMapSource) (StarTreatmentFit, error) {
	out := f
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !f.Usable {
		return out, nil
	}
	if !validStarTreatmentFit(f, w, h) {
		return out, fmt.Errorf("star %d has invalid footprint geometry", f.SourceID)
	}
	safeLimit := math.Min(math.Min(f.X, f.Y), math.Min(float64(w-1)-f.X, float64(h-1)-f.Y)) - 1
	// Blended companions share this star's footprint; they never bound it.
	for _, other := range sources {
		if other.ID == f.SourceID || starTreatmentIsCompanion(f, other.ID) || !other.Accepted() || !starFinite(other.X) || !starFinite(other.Y) || !starFinite(other.Radius) || other.Radius <= 0 {
			continue
		}
		safeLimit = math.Min(safeLimit, math.Hypot(other.X-f.X, other.Y-f.Y)-other.Radius)
	}
	limit := math.Min(32, safeLimit)
	inner := math.Max(2*f.Sigma, f.OuterRadius*.62)
	if f.Saturated && f.WingValidated {
		inner = math.Max(inner, f.CoreRadius+1)
	}
	lastSignal, lastClip := inner, 0.
	quiet := 0
	// A noise floor protects against growing a stellar footprint along a
	// bright nebular feature. Require agreement from three radial sectors.
	// Light that the current stretch cannot show is quiet as well, so the
	// footprint follows the display rather than the detector.
	quietLevel := starDisplayQuietLevel(meta, f.Background, starQuietContrast)
	negligibleLevel := starDisplayQuietLevel(meta, f.Background, starNegligibleContrast)
	threshold := math.Max(math.Max(3*f.Noise, f.Signal*.001), quietLevel)
	for r := math.Max(1, math.Floor(f.Sigma)); r < limit-3; r++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var sectors [4][]float64
		clipped, valid := 0, 0
		for y := max(0, int(math.Floor(f.Y-r-1))); y <= min(h-1, int(math.Ceil(f.Y+r+1))); y++ {
			for x := max(0, int(math.Floor(f.X-r-1))); x <= min(w-1, int(math.Ceil(f.X+r+1))); x++ {
				dx, dy := float64(x)-f.X, float64(y)-f.Y
				d := math.Hypot(dx, dy)
				if d < r || d >= r+1 {
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
				b := f.Background + f.BackgroundX*dx + f.BackgroundY*dy + companion
				q := 0
				if dx < 0 {
					q++
				}
				if dy < 0 {
					q += 2
				}
				sectors[q] = append(sectors[q], v-b)
				valid++
				if v >= meta.Peak && b < meta.Peak && v-b > threshold {
					clipped++
				}
			}
		}
		strong := 0
		for _, s := range sectors {
			if len(s) >= 2 && starMedian(s) > threshold {
				strong++
			}
		}
		if strong >= 3 {
			lastSignal = r + 1
			quiet = 0
		} else {
			quiet++
		}
		if valid > 0 && clipped*4 >= valid {
			lastClip = r + 1
			quiet = 0
		}
		if quiet >= 3 && r > inner {
			break
		}
	}
	inner = math.Max(inner, math.Max(lastSignal, lastClip)+1)
	outer := inner + math.Max(3, inner*.6)
	// The ordinary footprint remains the safety boundary for the core. A
	// separately validated extension may enlarge the geometry only when the
	// larger annulus has finite, symmetric support and safe margins.
	candidate := f
	candidate.InnerRadius, candidate.OuterRadius = inner, outer
	extended, coverage := detectExtendedStarComponents(ctx, pixels, w, h, candidate, sources, quietLevel, negligibleLevel)
	if err := ctx.Err(); err != nil {
		return out, err
	}
	// An unsafe footprint skips this star only; the remaining fits still
	// render. The reason is kept so the UI can report the skipped source.
	if outer >= safeLimit || (outer >= limit && !extended.HaloValidated) {
		out.Usable = false
		out.Reason = "visible halo reaches a neighbor or preview boundary"
		return out, nil
	}
	out.ExtendedCoverage = coverage
	out.HaloValidated = false
	out.HaloInnerRadius, out.HaloRadius = 0, 0
	out.Spikes = nil
	if extended.HaloValidated {
		out.HaloValidated = true
		out.HaloInnerRadius = extended.HaloInnerRadius
		out.HaloRadius = extended.HaloRadius
	}
	out.ExtendedBackground = extended.ExtendedBackground
	out.ExtendedBackgroundX = extended.ExtendedBackgroundX
	out.ExtendedBackgroundY = extended.ExtendedBackgroundY
	out.ExtendedNoise = extended.ExtendedNoise
	if len(extended.Spikes) > 0 {
		out.Spikes = append([]StarTreatmentSpike(nil), extended.Spikes...)
	}
	out.InnerRadius = inner
	out.OuterRadius = outer
	if !f.Saturated && footprintHasStructuredResidual(ctx, pixels, w, h, out, inner, outer) {
		out.Usable = false
		out.Reason = "structured local residual; stellar footprint is ambiguous"
		return out, nil
	}
	if out.ExtendedCoverage != "" && (out.HaloValidated || len(out.Spikes) > 0) {
		out.Reason = out.ExtendedCoverage
	} else if f.Saturated && f.WingValidated {
		out.Reason = "validated saturated wings; clipped core held at full strength with measured feather"
	} else {
		out.Reason = "local stellar excess compressed before clipping; measured core/halo feather"
	}
	if !out.HaloValidated && len(out.Spikes) == 0 {
		out.Reason += "; " + coverage
	}
	return out, nil
}

// starCompanionModelError is the assumed relative error of a subtracted
// blended companion's profile model.
const starCompanionModelError = .3

// starQuietContrast is the stretched contrast, as a fraction of the display
// range, below which stellar light is treated as invisible for footprint
// growth and halo detection.
//
// starNegligibleContrast is the stretched excess below which the gentler
// curve's own correction, E - E/(1+4*strength*E) <= 4*E*E, stays under about
// 1.5% of the display range even at full strength. A declining halo that
// reaches its safe search boundary below this level may end there without a
// visible step.
const (
	starQuietContrast      = .02
	starNegligibleContrast = .06
)

// starDisplayQuietLevel returns the linear excess above a background whose
// stretched contrast at the current settings equals the given fraction of
// the display range.
func starDisplayQuietLevel(meta models.LoadedImage, background, fraction float64) float64 {
	base := unclippedStarStretch(background, meta)
	contrast := func(e float64) float64 { return unclippedStarStretch(background+e, meta) - base }
	lo, hi := 0., math.Max(meta.Peak-meta.Background, 1e-9)
	if !starFinite(base) || contrast(hi) <= fraction {
		return hi
	}
	for i := 0; i < 60; i++ {
		mid := .5 * (lo + hi)
		if contrast(mid) > fraction {
			hi = mid
		} else {
			lo = mid
		}
	}
	return lo
}

// footprintHasStructuredResidual rejects an otherwise good circular fit when
// the proposed feather is supported mainly by a directional residual. A
// smooth stellar halo is present in all radial sectors; an offset nebular knot
// is not. This is deliberately a whole-footprint decision so the mask remains
// smooth rather than developing per-pixel holes.
func footprintHasStructuredResidual(ctx context.Context, pixels []float32, w, h int, f StarTreatmentFit, inner, outer float64) bool {
	start := math.Max(2*f.Sigma, inner*.55)
	end := math.Min(outer, start+math.Max(3, f.Sigma*3.5))
	if end <= start+1 {
		return false
	}
	flagged := 0
	for r0 := start; r0 < end; r0 += .75 {
		if err := ctx.Err(); err != nil {
			return false
		}
		var sectors [4][]float64
		companionPeak := 0.
		for y := max(0, int(math.Floor(f.Y-r0-1))); y <= min(h-1, int(math.Ceil(f.Y+r0+1))); y++ {
			for x := max(0, int(math.Floor(f.X-r0-1))); x <= min(w-1, int(math.Ceil(f.X+r0+1))); x++ {
				dx, dy := float64(x)-f.X, float64(y)-f.Y
				r := math.Hypot(dx, dy)
				if r < r0 || r >= r0+.75 {
					continue
				}
				inSpike := false
				for _, spike := range f.Spikes {
					if starTreatmentSpikeWeight(dx, dy, spike) > 0 {
						inSpike = true
						break
					}
				}
				if inSpike {
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
				companionPeak = math.Max(companionPeak, companion)
				residual := v - (f.Background + f.BackgroundX*dx + f.BackgroundY*dy) - companion
				if residual <= 0 {
					continue
				}
				q := 0
				if dx < 0 {
					q++
				}
				if dy < 0 {
					q += 2
				}
				sectors[q] = append(sectors[q], residual)
			}
		}
		medians := make([]float64, 0, 4)
		for _, sector := range sectors {
			if len(sector) >= 2 {
				medians = append(medians, starMedian(sector))
			}
		}
		if len(medians) < 4 {
			continue
		}
		sort.Float64s(medians)
		low := medians[0]
		high := medians[len(medians)-1]
		// Compare the directional excess with the fitted stellar profile. A
		// curved rim can be well below the old absolute signal floor while
		// still greatly exceeding the compact star's expected tail. Keep the
		// legacy signal threshold as an upper bound while adding sensitivity
		// to a directional excess above the fitted stellar tail.
		// The noise margin remains explicit so ordinary profile scatter is not
		// rejected.
		radius := r0 + .375
		stellarTail := f.Signal * math.Exp(-.5*radius*radius/(f.Sigma*f.Sigma))
		mismatchLimit := math.Max(4*f.Noise, math.Min(f.Signal*.015, 3*stellarTail+4*f.Noise))
		// A subtracted blended companion is a model, not a measurement: its
		// error is one-sided and, for a faint member whose width the joint fit
		// constrains poorly, a sizable fraction of its own peak. Allow that
		// fraction only where a companion actually contributes.
		companionError := math.Max(f.Residual, starCompanionModelError) * companionPeak
		mismatchLimit += companionError
		if high > mismatchLimit && high > 2.25*math.Max(low, math.Max(f.Noise, 1e-9))+companionError {
			flagged++
			if flagged >= 2 {
				return true
			}
		} else if flagged > 0 {
			flagged--
		}
	}
	return false
}
