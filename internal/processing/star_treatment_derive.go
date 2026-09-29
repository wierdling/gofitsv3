package processing

import (
	"context"
	"fmt"
	"math"
)

// DeriveStarTreatmentFits transfers prepared star geometry from a reference
// source to another source on the same grid. Every usable reference fit keeps
// its position, core, inner/outer radii, validated halo and spikes; only the
// quantities that live in the target's own units are re-measured there: the
// local background plane and noise in the annulus outside the footprint, the
// extended background for a halo or spikes, and the central excess. The
// result is ready for NewStarTreatmentModel; it is not re-prepared, because
// sharing the reference footprint is the point. Reference sources (the map
// catalog) drive neighbor exclusion in the annuli.
func DeriveStarTreatmentFits(ctx context.Context, reference []StarTreatmentFit, sources []StarMapSource, pixels []float32, w, h int, opt StarTreatmentOptions) ([]StarTreatmentFit, error) {
	if !validStarTreatmentDimensions(w, h) || len(pixels) != w*h {
		return nil, fmt.Errorf("invalid derived treatment dimensions")
	}
	opt = defaultStarTreatmentOptions(opt)
	out := make([]StarTreatmentFit, 0, len(reference))
	for n, ref := range reference {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if opt.Progress != nil && n%64 == 0 {
			opt.Progress("deriving star backgrounds", n, len(reference))
		}
		f := ref
		f.Companions = nil // reference-unit profiles; footprint tests are not rerun
		if !ref.Usable || !validStarTreatmentFit(ref, w, h) {
			f.Usable = false
			if ref.Usable {
				f.Reason = "reference geometry does not fit this grid"
			}
			out = append(out, f)
			continue
		}
		f.Reason = "geometry from reference source; background measured here"
		excluded := func(id int) bool { return id == ref.SourceID || starTreatmentIsCompanion(ref, id) }
		bg, bx, by, noise, ok := saturatedWingBackground(ctx, ref.X, ref.Y, ref.OuterRadius, excluded, sources, pixels, w, h, opt.MinSamples)
		if !ok {
			f.Usable = false
			f.Reason = "insufficient background samples in this source"
			out = append(out, f)
			continue
		}
		f.Background, f.BackgroundX, f.BackgroundY, f.Noise = bg, bx, by, noise
		if ref.HaloValidated || len(ref.Spikes) > 0 {
			extent := math.Min(maxStarTreatmentExtent, math.Min(math.Min(ref.X, ref.Y), math.Min(float64(w-1)-ref.X, float64(h-1)-ref.Y))-3)
			ebg, ebx, eby, enoise, ok := saturatedWingBackground(ctx, ref.X, ref.Y, extent/1.95, excluded, sources, pixels, w, h, 40)
			if !ok {
				// Without an extended background the halo and spikes cannot be
				// rendered here; keep the core footprint only.
				f.HaloValidated, f.HaloInnerRadius, f.HaloRadius = false, 0, 0
				f.Spikes = nil
				f.ExtendedBackground, f.ExtendedBackgroundX, f.ExtendedBackgroundY, f.ExtendedNoise = 0, 0, 0, 0
				f.Reason += "; extended background unavailable, core only"
			} else {
				f.ExtendedBackground, f.ExtendedBackgroundX, f.ExtendedBackgroundY, f.ExtendedNoise = ebg, ebx, eby, math.Max(enoise, 1e-9)
			}
		}
		// The central excess is diagnostic only; rendering uses the observed
		// samples. Keep it positive so the fit stays valid.
		cx, cy := int(math.Round(ref.X)), int(math.Round(ref.Y))
		signal := 0.
		if cx >= 0 && cy >= 0 && cx < w && cy < h {
			signal = float64(pixels[cy*w+cx]) - bg
		}
		if !starFinite(signal) {
			signal = 0
		}
		f.Signal = math.Max(signal, 1e-9)
		f.SNR = f.Signal / math.Max(noise, 1e-9)
		if !validStarTreatmentFit(f, w, h) {
			f.Usable = false
			f.Reason = "derived fit invalid on this source"
		}
		out = append(out, f)
	}
	return out, ctx.Err()
}
