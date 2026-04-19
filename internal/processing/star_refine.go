package processing

import (
	"fmt"
	"math"
	"sort"

	"gofitsv3/internal/fitsio"
)

func EstimateTranslationAfterWCS(targetPixels []float32, targetWidth, targetHeight int, targetHeader fitsio.Header, refPixels []float32, refWidth, refHeight int, refHeader fitsio.Header, initialOffsetX, initialOffsetY float64) (float64, float64, error) {
	transform, err := ComputeWCSTransform(targetHeader, refHeader)
	if err != nil {
		return 0, 0, err
	}

	// Start from the current WCS placement plus any existing manual/star offsets.
	transform.C = transform.C - (transform.A * initialOffsetX) - (transform.B * initialOffsetY)
	transform.F = transform.F - (transform.D * initialOffsetX) - (transform.E * initialOffsetY)

	warpedTarget, validMask := WarpImageToSizeWithMask(targetPixels, targetWidth, targetHeight, refWidth, refHeight, transform)
	maskedRef := make([]float32, len(refPixels))
	maskedTarget := make([]float32, len(refPixels))
	for i := range refPixels {
		refVal := float64(refPixels[i])
		targetVal := float64(warpedTarget[i])
		if !validMask[i] || math.IsNaN(refVal) || math.IsInf(refVal, 0) || math.IsNaN(targetVal) || math.IsInf(targetVal, 0) {
			maskedRef[i] = float32(math.NaN())
			maskedTarget[i] = float32(math.NaN())
			continue
		}
		maskedRef[i] = refPixels[i]
		maskedTarget[i] = warpedTarget[i]
	}

	// Filter stars within 15 px of a NaN boundary (chip gap, chip edge).
	// Those stars have truncated PSFs whose flux-weighted centroids are biased
	// toward the chip interior; they give inconsistent displacements between
	// exposures and degrade the alignment estimate.
	const nanGuard = 15
	refStars := filterStarsNearNaN(
		ExtractStars(maskedRef, refWidth, refHeight, 4.0, 3),
		maskedRef, refWidth, refHeight, nanGuard,
	)
	targetStars := filterStarsNearNaN(
		ExtractStars(maskedTarget, refWidth, refHeight, 4.0, 3),
		maskedTarget, refWidth, refHeight, nanGuard,
	)
	if len(refStars) < 3 || len(targetStars) < 3 {
		return 0, 0, fmt.Errorf("insufficient stars in shared region (ref: %d, target: %d)", len(refStars), len(targetStars))
	}

	// Triangle-invariant matching handles large WCS residuals (e.g. reduced-gyro HST)
	// where nearest-neighbour matching within a fixed radius would fail.
	pairs := MatchStars(refStars, targetStars, 100, 0.01)
	if len(pairs) < 3 {
		return 0, 0, fmt.Errorf("insufficient star matches after WCS correction (found %d)", len(pairs))
	}
	var dxs, dys []float64
	for _, p := range pairs {
		dxs = append(dxs, p.RefX-p.TargetX)
		dys = append(dys, p.RefY-p.TargetY)
	}
	return medianFloat64(dxs), medianFloat64(dys), nil
}

// EstimateTranslationFromRefStars is like EstimateTranslationAfterWCS but uses
// manually provided reference star positions instead of auto-detecting them.
// refStars are positions in the reference image's pixel space.
func EstimateTranslationFromRefStars(
	refStars []Star,
	targetPixels []float32, targetWidth, targetHeight int, targetHeader fitsio.Header,
	refWidth, refHeight int, refHeader fitsio.Header,
	initialOffsetX, initialOffsetY float64,
) (float64, float64, error) {
	if len(refStars) == 0 {
		return 0, 0, fmt.Errorf("no reference stars provided")
	}

	transform, err := ComputeWCSTransform(targetHeader, refHeader)
	if err != nil {
		return 0, 0, err
	}

	transform.C = transform.C - (transform.A * initialOffsetX) - (transform.B * initialOffsetY)
	transform.F = transform.F - (transform.D * initialOffsetX) - (transform.E * initialOffsetY)

	warpedTarget, validMask := WarpImageToSizeWithMask(targetPixels, targetWidth, targetHeight, refWidth, refHeight, transform)
	maskedTarget := make([]float32, len(warpedTarget))
	for i := range warpedTarget {
		v := float64(warpedTarget[i])
		if !validMask[i] || math.IsNaN(v) || math.IsInf(v, 0) {
			maskedTarget[i] = float32(math.NaN())
		} else {
			maskedTarget[i] = warpedTarget[i]
		}
	}

	targetStars := ExtractStars(maskedTarget, refWidth, refHeight, 4.0, 3)
	if len(targetStars) == 0 {
		return 0, 0, fmt.Errorf("no stars detected in warped target image")
	}

	// For each selected reference star, find the nearest auto-detected target star.
	// Skip stars whose position in ref space is not covered by the target image.
	const searchRadius = 30.0
	var dxs, dys []float64
	for _, rs := range refStars {
		px := int(math.Round(rs.X))
		py := int(math.Round(rs.Y))
		if px < 0 || px >= refWidth || py < 0 || py >= refHeight || !validMask[py*refWidth+px] {
			continue
		}
		bestDist := math.MaxFloat64
		var bestDx, bestDy float64
		for _, ts := range targetStars {
			dx := rs.X - ts.X
			dy := rs.Y - ts.Y
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist < bestDist {
				bestDist = dist
				bestDx = dx
				bestDy = dy
			}
		}
		if bestDist <= searchRadius {
			dxs = append(dxs, bestDx)
			dys = append(dys, bestDy)
		}
	}

	if len(dxs) == 0 {
		return 0, 0, fmt.Errorf("no selected stars matched in target image (search radius: %.0f px)", searchRadius)
	}
	return medianFloat64(dxs), medianFloat64(dys), nil
}

// EstimateAffineFromRefStars solves a corrective affine transform using
// manually selected reference-image star positions.
//
// All stars in the target image are extracted once.  For each user-selected ref
// star the function back-projects through the inverse of the current placement
// transform to find the expected position in target pixel space, then picks the
// nearest extracted target star within centroidSearchRadius pixels.  The matched
// target star is forward-mapped back to ref space to form a matched pair.
// RANSAC is run on all pairs to fit the ManualTransform.
//
// Matching by proximity (nearest star) rather than brightness avoids false
// matches in crowded fields where a brighter but unrelated star happens to lie
// inside the search box.
func EstimateAffineFromRefStars(
	refStars []Star,
	targetPixels []float32, targetWidth, targetHeight int, targetHeader fitsio.Header,
	refHeader fitsio.Header,
	initialOffsetX, initialOffsetY float64,
	initialRefinement *AffineTransform,
) (AffineTransform, error) {
	if len(refStars) < 3 {
		return AffineTransform{}, fmt.Errorf("at least 3 reference stars are required")
	}

	refToTarget, err := ComputeWCSTransform(targetHeader, refHeader)
	if err != nil {
		return AffineTransform{}, err
	}
	sourceToRef, err := InvertAffineTransform(refToTarget)
	if err != nil {
		return AffineTransform{}, err
	}

	// current: complete source→ref transform including offset and any prior refinement.
	current := ComposeAffineTransforms(translationTransform(initialOffsetX, initialOffsetY), sourceToRef)
	if initialRefinement != nil {
		current = ComposeAffineTransforms(*initialRefinement, current)
	}

	// refToSource: maps ref positions back to source/target pixel space.
	refToSource, err := InvertAffineTransform(current)
	if err != nil {
		return AffineTransform{}, err
	}

	// Extract all stars from the target image once.
	targetStars := ExtractStars(targetPixels, targetWidth, targetHeight, 4.0, 3)
	if len(targetStars) == 0 {
		return AffineTransform{}, fmt.Errorf("no stars detected in target image")
	}

	// Search radius for matching in target pixel space.  Large enough to handle
	// typical WCS rotation errors (50 px ≈ several arcseconds for HST).
	const centroidSearchRadius = 200.0

	var pairs []MatchedPair
	for _, rs := range refStars {
		// Expected target position under the current (possibly wrong) transform.
		tx, ty := ApplyAffineTransform(refToSource, rs.X, rs.Y)
		if tx < 0 || tx >= float64(targetWidth) || ty < 0 || ty >= float64(targetHeight) {
			continue // ref star has no coverage in target
		}

		// Find the nearest extracted target star within the search radius.
		// Proximity (not brightness) is the right criterion here: the WCS predicts
		// where the star should be, so we want the closest detected source, not
		// the brightest one in the neighbourhood.
		bestDist := centroidSearchRadius
		var bestStar Star
		found := false
		for _, ts := range targetStars {
			d := math.Hypot(ts.X-tx, ts.Y-ty)
			if d < bestDist {
				bestDist = d
				bestStar = ts
				found = true
			}
		}
		if !found {
			continue
		}

		// Forward-map the matched target-star position to "current ref space".
		// The pair tells RANSAC: at this position in current-ref space (wx,wy),
		// ManualTransform should predict the user-selected ref position (rs.X,rs.Y).
		wx, wy := ApplyAffineTransform(current, bestStar.X, bestStar.Y)
		pairs = append(pairs, MatchedPair{
			RefX: wx, RefY: wy,
			TargetX: rs.X, TargetY: rs.Y,
		})
	}

	if len(pairs) < 3 {
		return AffineTransform{}, fmt.Errorf("only %d of %d reference stars found in target image (need 3)", len(pairs), len(refStars))
	}

	// SolveTransformationRANSAC fits T where T(RefX,RefY) ≈ TargetX,TargetY,
	// i.e. T maps (current ref space) → (true ref space) = ManualTransform.
	refinement, err := SolveTransformationRANSAC(pairs, 2000, 2.0)
	if err != nil {
		return AffineTransform{}, fmt.Errorf("affine solve failed: %w", err)
	}
	return refinement, nil
}

// filterStarsNearNaN removes stars whose PSF region overlaps a NaN pixel.
// Stars within nanRadius of a NaN have truncated PSFs; their centroids are
// biased toward the chip interior and give unreliable displacement estimates.
func filterStarsNearNaN(stars []Star, pixels []float32, width, height, nanRadius int) []Star {
	out := stars[:0:0]
	for _, s := range stars {
		cx := int(math.Round(s.X))
		cy := int(math.Round(s.Y))
		x0 := cx - nanRadius
		if x0 < 0 {
			x0 = 0
		}
		x1 := cx + nanRadius
		if x1 >= width {
			x1 = width - 1
		}
		y0 := cy - nanRadius
		if y0 < 0 {
			y0 = 0
		}
		y1 := cy + nanRadius
		if y1 >= height {
			y1 = height - 1
		}
		hasNaN := false
		for y := y0; y <= y1 && !hasNaN; y++ {
			for x := x0; x <= x1 && !hasNaN; x++ {
				if math.IsNaN(float64(pixels[y*width+x])) {
					hasNaN = true
				}
			}
		}
		if !hasNaN {
			out = append(out, s)
		}
	}
	return out
}

// EstimateAffineAfterWCS is like EstimateTranslationAfterWCS but solves for a
// full affine transform rather than
// translation only.  This captures residual rotation between images that share
// the same nominal telescope orientation but differ by a small angle (e.g. due
// to guide-star differences between visits).
//
// The returned AffineTransform is intended to be stored as ManualTransform.
// initialOffsetX/Y should be the current input.OffsetX/Y so the WCS warp
// accounts for any previously applied translation offset.
func EstimateAffineAfterWCS(
	targetPixels []float32, targetWidth, targetHeight int, targetHeader fitsio.Header,
	refPixels []float32, refWidth, refHeight int, refHeader fitsio.Header,
	initialOffsetX, initialOffsetY float64,
) (AffineTransform, error) {
	transform, err := ComputeWCSTransform(targetHeader, refHeader)
	if err != nil {
		return AffineTransform{}, err
	}
	transform.C = transform.C - (transform.A*initialOffsetX) - (transform.B*initialOffsetY)
	transform.F = transform.F - (transform.D*initialOffsetX) - (transform.E*initialOffsetY)

	warpedTarget, validMask := WarpImageToSizeWithMask(targetPixels, targetWidth, targetHeight, refWidth, refHeight, transform)
	maskedRef := make([]float32, len(refPixels))
	maskedTarget := make([]float32, len(refPixels))
	for i := range refPixels {
		refVal := float64(refPixels[i])
		targetVal := float64(warpedTarget[i])
		if !validMask[i] || math.IsNaN(refVal) || math.IsInf(refVal, 0) || math.IsNaN(targetVal) || math.IsInf(targetVal, 0) {
			maskedRef[i] = float32(math.NaN())
			maskedTarget[i] = float32(math.NaN())
			continue
		}
		maskedRef[i] = refPixels[i]
		maskedTarget[i] = warpedTarget[i]
	}

	const nanGuard = 15
	refStars := filterStarsNearNaN(
		ExtractStars(maskedRef, refWidth, refHeight, 4.0, 3),
		maskedRef, refWidth, refHeight, nanGuard,
	)
	targetStars := filterStarsNearNaN(
		ExtractStars(maskedTarget, refWidth, refHeight, 4.0, 3),
		maskedTarget, refWidth, refHeight, nanGuard,
	)
	if len(refStars) < 3 || len(targetStars) < 3 {
		return AffineTransform{}, fmt.Errorf("insufficient stars in shared region (ref: %d, target: %d)", len(refStars), len(targetStars))
	}

	// MatchStars(target, ref) → RefX=target pos, TargetX=ref pos, so
	// SolveTransformationRANSAC fits T where T(target) ≈ ref = ManualTransform direction.
	// Triangle matching handles the large orientation residuals produced by reduced-gyro HST.
	// Full 6-parameter affine (via SolveTransformationRANSAC) captures differential scale
	// and shear that a similarity-only fit misses.
	pairs := MatchStars(targetStars, refStars, 100, 0.01)
	if len(pairs) < 3 {
		return AffineTransform{}, fmt.Errorf("insufficient star matches after WCS correction (found %d)", len(pairs))
	}
	return SolveTransformationRANSAC(pairs, 2000, 1.5)
}

func translationTransform(dx, dy float64) AffineTransform {
	return AffineTransform{A: 1, E: 1, C: dx, F: dy}
}

func medianFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
