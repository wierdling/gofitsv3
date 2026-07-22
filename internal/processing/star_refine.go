package processing

import (
	"fmt"
	"math"
	"sort"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
)

func EstimateTranslationAfterWCS(targetPixels []float32, targetWidth, targetHeight int, targetHeader fitsio.Header, refPixels []float32, refWidth, refHeight int, refHeader fitsio.Header, initialOffsetX, initialOffsetY float64) (float64, float64, error) {
	debuglog.Log("EstimateTranslationAfterWCS: starting")
	defer debuglog.Log("EstimateTranslationAfterWCS: finished")
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
	const nanGuard = 5
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

// EstimateTranslationFromCatalogs estimates the residual (dx, dy) translation,
// in the intermediate image's pixel frame, that aligns src onto intermediate.
//
// Unlike EstimateTranslationAfterWCS it works purely from pre-extracted star
// catalogs plus the WCS — no full-image warp and no re-extraction — so it is
// cheap enough to call for every overlapping pair in the chain-alignment
// fallback (the warp + double extraction in the warp-based version made
// auto-alignment of large mosaics appear to hang). srcStars / intermediateStars
// are in their own native pixel spaces. Returns the matched-star count.
//
// The returned dx,dy has the same meaning as EstimateTranslationAfterWCS: it is
// the offset to add to src's WCS placement so it lands on intermediate, so the
// two are interchangeable at the call site.
func EstimateTranslationFromCatalogs(
	srcStars []Star, srcHeader fitsio.Header, srcD2IX, srcD2IY *D2ITable,
	intermediateStars []Star, intermediateHeader fitsio.Header, intermediateD2IX, intermediateD2IY *D2ITable,
) (float64, float64, int, error) {
	if len(srcStars) < 3 || len(intermediateStars) < 3 {
		return 0, 0, 0, fmt.Errorf("insufficient stars (src %d, intermediate %d)", len(srcStars), len(intermediateStars))
	}
	mapper, err := NewWCSMapper(srcHeader, srcD2IX, srcD2IY, intermediateHeader, intermediateD2IX, intermediateD2IY)
	if err != nil {
		return 0, 0, 0, err
	}
	projected := make([]Star, len(srcStars))
	for i, s := range srcStars {
		rx, ry := mapper.MapPixel(s.X, s.Y)
		projected[i] = Star{X: rx, Y: ry, Flux: s.Flux}
	}
	// Match via the 2D offset histogram (robust to crowding and to which stars are
	// detected), falling back to triangle matching when no clear peak forms (e.g.
	// a residual rotation/scale spreads the offsets). The histogram gives the
	// correspondences; the precise translation is the median pair displacement,
	// so accuracy is sub-pixel rather than bin-quantised.
	pairs := matchStarsByOffsetHistogram(projected, intermediateStars, histWindowPx, histBinPx, histMatchRadiusPx)
	if len(pairs) < 3 {
		pairs = MatchStars(projected, intermediateStars, triangleMatchHardCap, 0.01)
	}
	if len(pairs) < 3 {
		return 0, 0, 0, fmt.Errorf("insufficient star matches (found %d)", len(pairs))
	}
	dxs := make([]float64, len(pairs))
	dys := make([]float64, len(pairs))
	for i, p := range pairs {
		// pairs: RefX/RefY = projected src (in intermediate frame),
		//        TargetX/TargetY = intermediate star. Residual to add to src's
		//        placement is intermediate - projected_src.
		dxs[i] = p.TargetX - p.RefX
		dys[i] = p.TargetY - p.RefY
	}
	dxMed := medianFloat64(dxs)
	dyMed := medianFloat64(dys)

	// Verify globally: count how many projected source stars land on an
	// intermediate star after the candidate translation. Frames that barely
	// overlap (e.g. an ACS chip vs a different chip ~2048 px away) produce a
	// handful of false triangle matches whose median is a garbage offset; those
	// align almost no stars and must be rejected, leaving the frame at its WCS
	// placement rather than shoving it by a bogus ~thousand-pixel translation.
	inliers := 0
	tolSq := tweakRegGlobalTolPx * tweakRegGlobalTolPx
	for _, s := range projected {
		px := s.X + dxMed
		py := s.Y + dyMed
		best := math.Inf(1)
		for _, r := range intermediateStars {
			d := (px-r.X)*(px-r.X) + (py-r.Y)*(py-r.Y)
			if d < best {
				best = d
			}
		}
		if best <= tolSq {
			inliers++
		}
	}
	// Reject a large residual (the WCS already places overlapping frames to within
	// tens of pixels, so a real neighbour residual is small) and require a few
	// corroborating stars. Together these reject the false cross-chip / no-overlap
	// matches that produced ~2000 px offsets, without penalising star-poor fields.
	if shift := math.Hypot(dxMed, dyMed); shift > maxResidualShiftPx {
		return 0, 0, inliers, fmt.Errorf("chain translation too large (%.0f px > %.0f): likely false matches", shift, maxResidualShiftPx)
	}
	if inliers < chainMinSupport {
		return 0, 0, inliers, fmt.Errorf("chain translation corroborated by too few stars (%d, need %d)", inliers, chainMinSupport)
	}
	return dxMed, dyMed, inliers, nil
}

// chainMinSupport is the low corroboration floor for the chain fallback — small
// enough that sparse neighbour overlaps still align, with the residual-shift
// bound (maxResidualShiftPx) doing the real false-match rejection.
const chainMinSupport = 3

// EstimateTranslationFromRefStars is like EstimateTranslationAfterWCS but uses
// manually provided reference star positions instead of auto-detecting them.
// refStars are positions in the reference image's pixel space.
func EstimateTranslationFromRefStars(
	refStars []Star,
	targetPixels []float32, targetWidth, targetHeight int, targetHeader fitsio.Header,
	refWidth, refHeight int, refHeader fitsio.Header,
	initialOffsetX, initialOffsetY float64,
) (float64, float64, error) {
	debuglog.Log("EstimateTranslationFromRefStars: starting")
	defer debuglog.Log("EstimateTranslationFromRefStars: finished")
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
// EstimateRScaleFromRefStars is like EstimateAffineFromRefStars but constrains
// the refinement to a similarity transform: translation, rotation, and one
// uniform scale (TweakReg-style rscale).
func EstimateRScaleFromRefStars(
	refStars []Star,
	targetPixels []float32, targetWidth, targetHeight int, targetHeader fitsio.Header,
	refHeader fitsio.Header,
	initialOffsetX, initialOffsetY float64,
	initialRefinement *AffineTransform,
) (AffineTransform, error) {
	debuglog.Log("EstimateRScaleFromRefStars: starting")
	defer debuglog.Log("EstimateRScaleFromRefStars: finished")
	if len(refStars) < 2 {
		return AffineTransform{}, fmt.Errorf("at least 2 reference stars are required")
	}

	refToTarget, err := ComputeWCSTransform(targetHeader, refHeader)
	if err != nil {
		return AffineTransform{}, err
	}
	sourceToRef, err := InvertAffineTransform(refToTarget)
	if err != nil {
		return AffineTransform{}, err
	}

	current := ComposeAffineTransforms(translationTransform(initialOffsetX, initialOffsetY), sourceToRef)
	if initialRefinement != nil {
		current = ComposeAffineTransforms(*initialRefinement, current)
	}

	refToSource, err := InvertAffineTransform(current)
	if err != nil {
		return AffineTransform{}, err
	}

	targetStars := ExtractStars(targetPixels, targetWidth, targetHeight, 4.0, 3)
	debuglog.Log(fmt.Sprintf("EstimateRScaleFromRefStars: %d target stars detected", len(targetStars)))
	if len(targetStars) == 0 {
		return AffineTransform{}, fmt.Errorf("no stars detected in target image")
	}

	const centroidSearchRadius = 200.0

	var pairs []MatchedPair
	inBounds := 0
	for _, rs := range refStars {
		tx, ty := ApplyAffineTransform(refToSource, rs.X, rs.Y)
		if tx < 0 || tx >= float64(targetWidth) || ty < 0 || ty >= float64(targetHeight) {
			debuglog.Log(fmt.Sprintf("EstimateRScaleFromRefStars: ref star (%.1f,%.1f) projects out of bounds → (%.1f,%.1f)", rs.X, rs.Y, tx, ty))
			continue
		}
		inBounds++

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
			debuglog.Log(fmt.Sprintf("EstimateRScaleFromRefStars: ref star (%.1f,%.1f) → predicted (%.1f,%.1f) no match within %.0fpx", rs.X, rs.Y, tx, ty, centroidSearchRadius))
			continue
		}
		debuglog.Log(fmt.Sprintf("EstimateRScaleFromRefStars: ref star (%.1f,%.1f) matched target star (%.1f,%.1f) dist=%.1fpx", rs.X, rs.Y, bestStar.X, bestStar.Y, bestDist))

		wx, wy := ApplyAffineTransform(current, bestStar.X, bestStar.Y)
		pairs = append(pairs, MatchedPair{
			RefX: wx, RefY: wy,
			TargetX: rs.X, TargetY: rs.Y,
		})
	}

	debuglog.Log(fmt.Sprintf("EstimateRScaleFromRefStars: %d/%d ref stars in-bounds, %d matched", inBounds, len(refStars), len(pairs)))
	if len(pairs) < 2 {
		return AffineTransform{}, fmt.Errorf("only %d of %d reference stars found in target image (need 2)", len(pairs), len(refStars))
	}

	refinement, err := SolveRScaleTransformationRANSAC(pairs, 300, 2.0)
	if err != nil {
		return AffineTransform{}, fmt.Errorf("rscale solve failed: %w", err)
	}
	return refinement, nil
}

func EstimateAffineFromRefStars(
	refStars []Star,
	targetPixels []float32, targetWidth, targetHeight int, targetHeader fitsio.Header,
	refHeader fitsio.Header,
	initialOffsetX, initialOffsetY float64,
	initialRefinement *AffineTransform,
) (AffineTransform, error) {
	debuglog.Log("EstimateAffineFromRefStars: starting")
	defer debuglog.Log("EstimateAffineFromRefStars: finished")
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

type candidateMatch struct {
	refIdx    int
	targetIdx int
	distSq    float64
}

// Offset-histogram matching parameters (TweakReg xyxymatch / 2dhist style).
const (
	// histWindowPx is the half-width of the offset search window. Offsets beyond
	// this are ignored; it is sized to the residual we are willing to accept
	// (maxResidualShiftPx) since anything larger would be rejected downstream.
	histWindowPx = maxResidualShiftPx
	// histBinPx is the offset histogram bin size. Small enough to localise the
	// peak, large enough that centroid noise doesn't split a real cluster.
	histBinPx = 3.0
	// histMinClusterVotes is the minimum 3x3-neighbourhood vote count for the
	// peak to be trusted as the bulk offset. Low, because everything downstream
	// (proximity re-match, fit, shift gate) re-validates the result.
	histMinClusterVotes = 4
	// histMatchRadiusPx is the proximity radius used to re-pair stars after the
	// bulk offset is removed. Generous enough to absorb the small per-star spread
	// from any residual field rotation; the subsequent fit captures the rotation.
	histMatchRadiusPx = 8.0
)

// dominantOffset finds the most common pairwise offset (refStar - projected)
// between two catalogs via a 2D histogram vote — the TweakReg xyxymatch idea.
// Truly-corresponding star pairs all share (nearly) the same offset and pile up
// at one histogram cell, while false pairings scatter, so the peak is the bulk
// translation even in crowded or star-poor fields and even when the residual
// exceeds a nearest-neighbour search radius. Returns the (sub-bin centroided)
// offset and whether a confident peak was found.
func dominantOffset(projected, refStars []Star, windowPx, binPx float64) (float64, float64, bool) {
	if len(projected) == 0 || len(refStars) == 0 {
		return 0, 0, false
	}
	if binPx <= 0 {
		binPx = 3
	}
	type cell struct{ x, y int }
	hist := make(map[cell]int)
	peak := cell{}
	peakCount := 0
	for _, p := range projected {
		for _, r := range refStars {
			ox := r.X - p.X
			oy := r.Y - p.Y
			if ox < -windowPx || ox > windowPx || oy < -windowPx || oy > windowPx {
				continue
			}
			c := cell{int(math.Floor((ox + windowPx) / binPx)), int(math.Floor((oy + windowPx) / binPx))}
			hist[c]++
			if hist[c] > peakCount {
				peakCount = hist[c]
				peak = c
			}
		}
	}
	if peakCount == 0 {
		return 0, 0, false
	}
	// Sum and centroid the 3x3 neighbourhood of the peak so a cluster split
	// across a bin boundary is still recognised and localised to sub-bin accuracy.
	var sum, sumX, sumY float64
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			c := cell{peak.x + dx, peak.y + dy}
			cnt := hist[c]
			if cnt == 0 {
				continue
			}
			cx := (float64(c.x)+0.5)*binPx - windowPx
			cy := (float64(c.y)+0.5)*binPx - windowPx
			sum += float64(cnt)
			sumX += float64(cnt) * cx
			sumY += float64(cnt) * cy
		}
	}
	if int(sum) < histMinClusterVotes {
		return 0, 0, false
	}
	return sumX / sum, sumY / sum, true
}

// matchStarsByOffsetHistogram pairs stars by first finding the bulk translation
// with dominantOffset, then re-matching by proximity after removing it. The
// returned pairs use the original projected coordinates as Ref (so a subsequent
// fit solves the full residual transform, including the bulk shift). Returns nil
// if no confident bulk offset is found.
func matchStarsByOffsetHistogram(projected, refStars []Star, windowPx, binPx, matchRadiusPx float64) []MatchedPair {
	dx, dy, ok := dominantOffset(projected, refStars, windowPx, binPx)
	if !ok {
		return nil
	}
	shifted := make([]Star, len(projected))
	for i, s := range projected {
		shifted[i] = Star{X: s.X + dx, Y: s.Y + dy, Flux: s.Flux}
	}
	pairs := matchStarsByMutualProximity(shifted, refStars, 0, matchRadiusPx, 0.85)
	// Undo the bulk shift so Ref is the original projected position again.
	for k := range pairs {
		pairs[k].RefX -= dx
		pairs[k].RefY -= dy
	}
	return pairs
}

// matchStarsByMutualProximity matches stars already brought into roughly the
// same pixel coordinate system. It is much cheaper than triangle matching and
// is intended for the post-WCS refinement path. Only mutual nearest-neighbour
// matches are accepted, and ambiguous matches are rejected using a best-vs-
// second-best distance ratio test.
func matchStarsByMutualProximity(refStars, targetStars []Star, maxStars int, maxRadius, maxRatio float64) []MatchedPair {
	if len(refStars) == 0 || len(targetStars) == 0 {
		return nil
	}
	if maxStars > 0 && len(refStars) > maxStars {
		refStars = refStars[:maxStars]
	}
	if maxStars > 0 && len(targetStars) > maxStars {
		targetStars = targetStars[:maxStars]
	}
	maxDistSq := maxRadius * maxRadius
	ratioSq := maxRatio * maxRatio

	bestTargetForRef := make([]int, len(refStars))
	bestDistRef := make([]float64, len(refStars))
	secondDistRef := make([]float64, len(refStars))
	for i := range bestTargetForRef {
		bestTargetForRef[i] = -1
		bestDistRef[i] = math.Inf(1)
		secondDistRef[i] = math.Inf(1)
	}
	for ri, rs := range refStars {
		for ti, ts := range targetStars {
			dx := rs.X - ts.X
			dy := rs.Y - ts.Y
			distSq := dx*dx + dy*dy
			if distSq > maxDistSq {
				continue
			}
			if distSq < bestDistRef[ri] {
				secondDistRef[ri] = bestDistRef[ri]
				bestDistRef[ri] = distSq
				bestTargetForRef[ri] = ti
			} else if distSq < secondDistRef[ri] {
				secondDistRef[ri] = distSq
			}
		}
	}

	bestRefForTarget := make([]int, len(targetStars))
	bestDistTarget := make([]float64, len(targetStars))
	secondDistTarget := make([]float64, len(targetStars))
	for i := range bestRefForTarget {
		bestRefForTarget[i] = -1
		bestDistTarget[i] = math.Inf(1)
		secondDistTarget[i] = math.Inf(1)
	}
	for ti, ts := range targetStars {
		for ri, rs := range refStars {
			dx := rs.X - ts.X
			dy := rs.Y - ts.Y
			distSq := dx*dx + dy*dy
			if distSq > maxDistSq {
				continue
			}
			if distSq < bestDistTarget[ti] {
				secondDistTarget[ti] = bestDistTarget[ti]
				bestDistTarget[ti] = distSq
				bestRefForTarget[ti] = ri
			} else if distSq < secondDistTarget[ti] {
				secondDistTarget[ti] = distSq
			}
		}
	}

	candidates := make([]candidateMatch, 0, minInt(len(refStars), len(targetStars)))
	for ri, ti := range bestTargetForRef {
		if ti < 0 || bestRefForTarget[ti] != ri {
			continue
		}
		if math.IsInf(bestDistRef[ri], 1) || math.IsInf(bestDistTarget[ti], 1) {
			continue
		}
		if !math.IsInf(secondDistRef[ri], 1) && bestDistRef[ri] > ratioSq*secondDistRef[ri] {
			continue
		}
		if !math.IsInf(secondDistTarget[ti], 1) && bestDistTarget[ti] > ratioSq*secondDistTarget[ti] {
			continue
		}
		candidates = append(candidates, candidateMatch{refIdx: ri, targetIdx: ti, distSq: bestDistRef[ri]})
	}
	if len(candidates) == 0 {
		return nil
	}
	// Sort by proximity, with index tiebreakers so equal-distance candidates keep
	// a deterministic order (an unstable sort could otherwise reorder ties between
	// runs and change which mutual matches survive the greedy one-to-one pass).
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].distSq != candidates[j].distSq {
			return candidates[i].distSq < candidates[j].distSq
		}
		if candidates[i].refIdx != candidates[j].refIdx {
			return candidates[i].refIdx < candidates[j].refIdx
		}
		return candidates[i].targetIdx < candidates[j].targetIdx
	})

	pairs := make([]MatchedPair, 0, len(candidates))
	usedRef := make([]bool, len(refStars))
	usedTarget := make([]bool, len(targetStars))
	for _, c := range candidates {
		if usedRef[c.refIdx] || usedTarget[c.targetIdx] {
			continue
		}
		usedRef[c.refIdx] = true
		usedTarget[c.targetIdx] = true
		rs := refStars[c.refIdx]
		ts := targetStars[c.targetIdx]
		pairs = append(pairs, MatchedPair{
			RefX: rs.X, RefY: rs.Y,
			TargetX: ts.X, TargetY: ts.Y,
		})
	}
	return pairs
}

func pairBoundsTarget(pairs []MatchedPair) (width, height, diag float64) {
	if len(pairs) == 0 {
		return 0, 0, 0
	}
	minX, maxX := pairs[0].TargetX, pairs[0].TargetX
	minY, maxY := pairs[0].TargetY, pairs[0].TargetY
	for _, p := range pairs[1:] {
		if p.TargetX < minX {
			minX = p.TargetX
		}
		if p.TargetX > maxX {
			maxX = p.TargetX
		}
		if p.TargetY < minY {
			minY = p.TargetY
		}
		if p.TargetY > maxY {
			maxY = p.TargetY
		}
	}
	width = maxX - minX
	height = maxY - minY
	diag = math.Hypot(width, height)
	return width, height, diag
}

func rscaleResidualStats(pairs []MatchedPair, t AffineTransform) (rms, maxErr float64) {
	if len(pairs) == 0 {
		return 0, 0
	}
	var sumSq float64
	for _, p := range pairs {
		px := t.A*p.RefX + t.B*p.RefY + t.C
		py := t.D*p.RefX + t.E*p.RefY + t.F
		err := math.Hypot(px-p.TargetX, py-p.TargetY)
		sumSq += err * err
		if err > maxErr {
			maxErr = err
		}
	}
	return math.Sqrt(sumSq / float64(len(pairs))), maxErr
}

func validateLocalRScalePairs(pairs []MatchedPair) error {
	const (
		minPairs     = 8
		minShortAxis = 120.0
		minDiag      = 250.0
		maxRMS       = 1.75
		maxErr       = 4.5
	)
	if len(pairs) < minPairs {
		return fmt.Errorf("local matcher found only %d pairs", len(pairs))
	}
	w, h, diag := pairBoundsTarget(pairs)
	shortAxis := math.Min(w, h)
	if shortAxis < minShortAxis || diag < minDiag {
		return fmt.Errorf("local matches poorly distributed (bbox %.1fx%.1f, diag %.1f)", w, h, diag)
	}
	t, err := solveRScaleLeastSquares(pairs)
	if err != nil {
		return err
	}
	rms, peak := rscaleResidualStats(pairs, t)
	if rms > maxRMS || peak > maxErr {
		return fmt.Errorf("local matches inconsistent (rms %.2f px, max %.2f px)", rms, peak)
	}
	return nil
}

func validateFallbackRScalePairs(pairs []MatchedPair) error {
	const (
		minPairs = 4
		maxRMS   = 2.25
		maxErr   = 6.0
	)
	if len(pairs) < minPairs {
		return fmt.Errorf("triangle matcher found only %d pairs", len(pairs))
	}
	t, err := solveRScaleLeastSquares(pairs)
	if err != nil {
		return err
	}
	rms, peak := rscaleResidualStats(pairs, t)
	if rms > maxRMS || peak > maxErr {
		return fmt.Errorf("triangle matches inconsistent (rms %.2f px, max %.2f px)", rms, peak)
	}
	return nil
}

// selectPairsForRScaleAfterWCS tries the cheap local matcher first because the
// WCS warp should already have brought the stars into nearly the same frame.
// It only accepts that result when the matches are numerous, spatially well
// spread, and geometrically self-consistent. Otherwise it falls back to the
// slower triangle matcher.
func selectPairsForRScaleAfterWCS(targetStars, refStars []Star) ([]MatchedPair, error) {
	local := matchStarsByMutualProximity(targetStars, refStars, 40, 12.0, 0.80)
	localErr := validateLocalRScalePairs(local)
	if localErr == nil {
		return local, nil
	}

	fallback := MatchStars(targetStars, refStars, 30, 0.01)
	fallbackErr := validateFallbackRScalePairs(fallback)
	if fallbackErr == nil {
		return fallback, nil
	}

	return nil, fmt.Errorf("rscale matching failed: local=%v; triangle=%v", localErr, fallbackErr)
}

// EstimateRScaleAfterWCS is like EstimateTranslationAfterWCS but solves for a
// similarity transform (translation + rotation + uniform scale) rather than a
// full affine transform.
func EstimateRScaleAfterWCS(
	targetPixels []float32, targetWidth, targetHeight int, targetHeader fitsio.Header,
	refPixels []float32, refWidth, refHeight int, refHeader fitsio.Header,
	initialOffsetX, initialOffsetY float64,
) (AffineTransform, error) {
	debuglog.Log("EstimateRScaleAfterWCS: starting")
	defer debuglog.Log("EstimateRScaleAfterWCS: finished")
	transform, err := ComputeWCSTransform(targetHeader, refHeader)
	if err != nil {
		return AffineTransform{}, err
	}
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

	const nanGuard = 5
	refStars := filterStarsNearNaN(
		ExtractStars(maskedRef, refWidth, refHeight, 4.0, 3),
		maskedRef, refWidth, refHeight, nanGuard,
	)
	targetStars := filterStarsNearNaN(
		ExtractStars(maskedTarget, refWidth, refHeight, 4.0, 3),
		maskedTarget, refWidth, refHeight, nanGuard,
	)
	if len(refStars) < 2 || len(targetStars) < 2 {
		return AffineTransform{}, fmt.Errorf("insufficient stars in shared region (ref: %d, target: %d)", len(refStars), len(targetStars))
	}

	pairs, err := selectPairsForRScaleAfterWCS(targetStars, refStars)
	if err != nil {
		return AffineTransform{}, err
	}
	return SolveRScaleTransformationRANSAC(pairs, 300, 1.5)
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
	debuglog.Log("EstimateAffineAfterWCS: starting")
	defer debuglog.Log("EstimateAffineAfterWCS: finished")
	transform, err := ComputeWCSTransform(targetHeader, refHeader)
	if err != nil {
		return AffineTransform{}, err
	}
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

	const nanGuard = 5
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
