package processing

import (
	"fmt"
	"math"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
)

// EstimateTweakRegAlignment aligns a source image to a reference image using a
// TweakReg-style catalog-matching strategy.  Unlike the warp-based estimators,
// this function never resamples pixel data: it extracts stars in each image's
// native pixel space, projects the source catalog to reference pixel space
// through the full WCS pipeline (SIP + D2IMARR), matches catalogs, and fits a
// residual correction transform.
//
// mapper must be built with NewWCSMapper (including D2I tables if available).
// searchRadiusArcsec is the matching tolerance in arcseconds; typical values
// are 1.0–2.0.  fitgeom selects the fitting model: "rscale" for similarity
// (translation + rotation + uniform scale) or "general" for full affine.
//
// The returned AffineTransform is the residual correction that maps projected
// source positions to true reference positions.  It is intended to be stored as
// ManualTransform and is equivalent to what EstimateRScaleAfterWCS /
// EstimateAffineAfterWCS return, but derived without a linearization step.
// AlignStats reports how well a fitted alignment transform is supported by the
// data so callers can surface (or reject) low-confidence solutions instead of
// silently applying a possibly-wrong transform.
type AlignStats struct {
	MatchedStars  int     // matched pairs used for the fit
	GlobalInliers int     // full-catalog source stars that land on a ref star under the fit
	RMS           float64 // RMS residual of the matched pairs (px)
	MaxError      float64 // worst matched-pair residual (px)
}

const (
	// tweakRegGlobalTolPx is the radius within which a projected source star is
	// considered aligned with a reference star when verifying a fit globally.
	tweakRegGlobalTolPx = 2.0
	// tweakRegMinSupport is a low floor ensuring at least a few stars corroborate
	// the fit (it guards against 2-point rscale overfits). It is intentionally
	// small so star-poor deep extragalactic fields — which may only have a handful
	// of usable point sources — still align. The primary false-match guard is the
	// residual-shift bound below, NOT a star count.
	tweakRegMinSupport = 3
	// maxResidualShiftPx bounds how far the residual correction may move any image
	// corner. HST WCS places frames to within tens of pixels, so a genuine
	// TweakReg residual is small; a much larger shift means the fit latched onto
	// false matches. This is the gate that rejects the catastrophic 100–2000 px
	// misalignments while leaving legitimate small corrections (and star-poor
	// fields) untouched. It assumes the input WCS is roughly correct, which is the
	// normal case for calibrated HST products.
	maxResidualShiftPx = 150.0
)

// maxCornerShift returns the largest distance the transform moves any of the
// image's four corners — i.e. the worst-case residual displacement it applies.
func maxCornerShift(t AffineTransform, width, height int) float64 {
	corners := [4][2]float64{{0, 0}, {float64(width), 0}, {0, float64(height)}, {float64(width), float64(height)}}
	maxShift := 0.0
	for _, c := range corners {
		nx := t.A*c[0] + t.B*c[1] + t.C
		ny := t.D*c[0] + t.E*c[1] + t.F
		if s := math.Hypot(nx-c[0], ny-c[1]); s > maxShift {
			maxShift = s
		}
	}
	return maxShift
}

func EstimateTweakRegAlignment(
	sourcePixels []float32, sourceWidth, sourceHeight int,
	mapper *WCSMapper,
	refPixels []float32, refWidth, refHeight int, refHeader fitsio.Header,
	searchRadiusArcsec float64,
	fitgeom string,
) (AffineTransform, AlignStats, error) {
	debuglog.Log("EstimateTweakRegAlignment: starting")
	defer debuglog.Log("EstimateTweakRegAlignment: finished")
	const maxCatalogStars = 200
	refStars := ExtractAndLimitStars(refPixels, refWidth, refHeight, 4.0, 3, maxCatalogStars)
	return EstimateTweakRegAlignmentWithRefStars(
		sourcePixels, sourceWidth, sourceHeight,
		mapper, refPixels, refStars, refWidth, refHeight, refHeader,
		searchRadiusArcsec, fitgeom,
	)
}

// transformGlobalSupport counts how many of the projected source stars land
// within tol pixels of some reference star after applying t (which maps a
// projected source position to reference-pixel space). This validates a
// candidate transform against the FULL catalogs rather than only the matched
// pairs, exposing a false consensus that explains its seed matches but nothing
// else.
func transformGlobalSupport(projected, refStars []Star, t AffineTransform, tol float64) int {
	if len(projected) == 0 || len(refStars) == 0 {
		return 0
	}
	tolSq := tol * tol
	count := 0
	for _, s := range projected {
		px := t.A*s.X + t.B*s.Y + t.C
		py := t.D*s.X + t.E*s.Y + t.F
		best := math.Inf(1)
		for _, r := range refStars {
			dx := px - r.X
			dy := py - r.Y
			d := dx*dx + dy*dy
			if d < best {
				best = d
			}
		}
		if best <= tolSq {
			count++
		}
	}
	return count
}

// EstimateTweakRegAlignmentWithRefStars is like EstimateTweakRegAlignment but
// accepts a pre-extracted reference catalog. Use this when aligning multiple
// source images to the same reference so extraction only happens once.
// refPixels is used only for the debug visualization (AlignmentDebugHook); pass
// nil when not needed.
func EstimateTweakRegAlignmentWithRefStars(
	sourcePixels []float32, sourceWidth, sourceHeight int,
	mapper *WCSMapper,
	refPixels []float32,
	refStars []Star,
	refWidth, refHeight int, refHeader fitsio.Header,
	searchRadiusArcsec float64,
	fitgeom string,
) (AffineTransform, AlignStats, error) {
	if mapper == nil {
		return AffineTransform{}, AlignStats{}, fmt.Errorf("WCSMapper is required for TweakReg alignment")
	}

	// Capture diagnostic data for the debug hook, regardless of success or failure.
	var dbgRef, dbgSrc []Star
	var dbgPairs []MatchedPair
	if AlignmentDebugHook != nil {
		defer func() {
			AlignmentDebugHook(AlignmentDiag{
				RefPixels:   refPixels,
				RefW:        refWidth,
				RefH:        refHeight,
				RefStars:    dbgRef,
				SourceStars: dbgSrc,
				Pairs:       dbgPairs,
			})
		}()
	}

	plateScale, ok := plateScaleArcsecPerPixel(refHeader)
	if !ok || plateScale <= 0 {
		plateScale = 0.04
	}
	searchRadiusPx := searchRadiusArcsec / plateScale

	const maxCatalogStars = 200
	sourceStars := ExtractAndLimitStars(sourcePixels, sourceWidth, sourceHeight, 4.0, 3, maxCatalogStars)
	debuglog.Log(fmt.Sprintf("EstimateTweakRegAlignmentWithRefStars: %d source stars detected, %d ref stars provided", len(sourceStars), len(refStars)))
	if len(sourceStars) < 2 {
		return AffineTransform{}, AlignStats{}, fmt.Errorf("too few stars in source image (%d)", len(sourceStars))
	}
	if len(refStars) < 2 {
		return AffineTransform{}, AlignStats{}, fmt.Errorf("too few stars in reference image (%d)", len(refStars))
	}
	dbgRef = refStars

	// Project source stars to reference pixel space through the full WCS pipeline.
	projected := make([]Star, 0, len(sourceStars))
	for _, s := range sourceStars {
		rx, ry := mapper.MapPixel(s.X, s.Y)
		if rx < -searchRadiusPx || rx >= float64(refWidth)+searchRadiusPx ||
			ry < -searchRadiusPx || ry >= float64(refHeight)+searchRadiusPx {
			continue
		}
		projected = append(projected, Star{X: rx, Y: ry, Flux: s.Flux})
	}
	dbgSrc = projected
	debuglog.Log(fmt.Sprintf("EstimateTweakRegAlignmentWithRefStars: %d/%d source stars project into reference frame", len(projected), len(sourceStars)))
	if len(projected) < 2 {
		return AffineTransform{}, AlignStats{}, fmt.Errorf("too few source stars project into reference frame (%d)", len(projected))
	}

	result, stats, pairs, err := fitCatalogTransform(projected, refStars, refWidth, refHeight, searchRadiusPx, fitgeom)
	dbgPairs = pairs
	if err != nil {
		return AffineTransform{}, stats, err
	}
	return result, stats, nil
}

// fitCatalogTransform matches a projected source catalog against a reference
// catalog (both already in reference-pixel space) and fits the transform that
// maps projected positions onto the reference positions, applying the same
// robustness gates used throughout the mosaic builder. It returns the matched
// pairs so callers can feed the alignment debug hook.
//
// searchRadiusPx is the nearest-neighbour proximity radius; the 2D offset
// histogram (the primary matcher) uses its own fixed window. fitgeom is
// "rscale" (similarity) or "general" (full affine).
func fitCatalogTransform(projected, refStars []Star, refWidth, refHeight int, searchRadiusPx float64, fitgeom string) (AffineTransform, AlignStats, []MatchedPair, error) {
	// Primary matcher: 2D offset histogram (TweakReg xyxymatch/2dhist style). The
	// dominant pairwise offset between the projected source catalog and the
	// reference catalog is the bulk shift; this is far more robust in crowded or
	// star-poor fields than per-star nearest-neighbour matching, and it tolerates
	// a WCS residual larger than the proximity search radius without resorting to
	// the (false-match-prone) triangle matcher.
	var pairs []MatchedPair
	if hp := matchStarsByOffsetHistogram(projected, refStars, histWindowPx, histBinPx, histMatchRadiusPx); len(hp) >= 4 {
		debuglog.Log(fmt.Sprintf("fitCatalogTransform: 2dhist matched %d pairs", len(hp)))
		pairs = hp
	}

	// Fallback: iterative nearest-neighbour match + fit (start wide, sigma-clip,
	// tighten). Used when the histogram did not lock on (e.g. essentially zero
	// residual, or too few stars to form a clear peak).
	currentRadius := searchRadiusPx
	for iter := 0; len(pairs) < 4 && iter < 3; iter++ {
		pairs = matchStarsByMutualProximity(projected, refStars, 80, currentRadius, 0.80)
		if len(pairs) < 4 {
			break
		}
		var t AffineTransform
		var err error
		if fitgeom == "rscale" {
			t, err = solveRScaleLeastSquares(pairs)
		} else {
			t, err = solveLeastSquares(pairs)
		}
		if err != nil {
			break
		}
		pairs = sigmaClipPairs(pairs, t, 3.0)
		currentRadius = math.Max(searchRadiusPx/3.0, 5.0)
	}

	// Wider-radius fallback: if the iterative loop didn't yield enough pairs,
	// try a single one-shot match at 2× the nominal radius before giving up.
	// Only attempt this when there is substantial projected coverage; with fewer
	// than 30 projected stars the matches are too ambiguous to be trusted.
	if len(pairs) < 4 && len(projected) >= 30 {
		wide := matchStarsByMutualProximity(projected, refStars, 80, searchRadiusPx*2, 0.80)
		debuglog.Log(fmt.Sprintf("fitCatalogTransform: wide-radius retry: %d pairs (was %d)", len(wide), len(pairs)))
		if len(wide) > len(pairs) {
			pairs = wide
		}
	}

	// Translation-invariant (Triangle) fallback: if the offset is significantly larger
	// than the search radius (e.g. severe HST gyroscope drift), proximity matching fails entirely.
	// We fall back to the triangle matcher which is invariant to translation, rotation, and scale.
	if len(pairs) < 4 {
		trianglePairs := MatchStars(projected, refStars, 80, 0.01)
		debuglog.Log(fmt.Sprintf("fitCatalogTransform: triangle fallback: %d pairs", len(trianglePairs)))
		if len(trianglePairs) > len(pairs) {
			pairs = trianglePairs
		}
	}

	minPairs := 3
	if fitgeom == "rscale" {
		minPairs = 2
	}
	debuglog.Log(fmt.Sprintf("fitCatalogTransform: %d pairs after matching (need %d)", len(pairs), minPairs))
	if len(pairs) < minPairs {
		return AffineTransform{}, AlignStats{}, pairs, fmt.Errorf("insufficient matched pairs after star matching (%d, need %d)", len(pairs), minPairs)
	}

	var result AffineTransform
	if fitgeom == "rscale" {
		rscale, err := SolveRScaleTransformationRANSAC(pairs, 300, 1.5)
		if err != nil {
			return AffineTransform{}, AlignStats{}, pairs, err
		}
		general, generalErr := SolveTransformationRANSAC(pairs, 2000, 1.5)
		if generalErr == nil && shouldUpgradeTweakRegFit(pairs, rscale, general, refWidth, refHeight) {
			rRMS, rMax := residualStats(pairs, rscale)
			gRMS, gMax := residualStats(pairs, general)
			debuglog.Log(fmt.Sprintf("fitCatalogTransform: upgrading fitgeom from rscale to general (pairs=%d, rscale rms=%.2f max=%.2f, general rms=%.2f max=%.2f)", len(pairs), rRMS, rMax, gRMS, gMax))
			result = general
		} else {
			result = rscale
		}
	} else {
		var err error
		result, err = SolveTransformationRANSAC(pairs, 2000, 1.5)
		if err != nil {
			return AffineTransform{}, AlignStats{}, pairs, err
		}
	}

	rms, maxErr := residualStats(pairs, result)
	support := transformGlobalSupport(projected, refStars, result, tweakRegGlobalTolPx)
	stats := AlignStats{MatchedStars: len(pairs), GlobalInliers: support, RMS: rms, MaxError: maxErr}

	// Sanity-check: the fitted correction must be near-identity in scale/rotation.
	// Any computed transform with scale far from 1.0 or a large rotation is
	// evidence of false star matches.
	if !tweakRegTransformIsPhysical(result) {
		det := result.A*result.E - result.B*result.D
		angle := math.Atan2(result.D-result.B, result.A+result.E) * 180.0 / math.Pi
		debuglog.Log(fmt.Sprintf("fitCatalogTransform: transform rejected (det=%.4f angle=%.1f°) — likely false star matches", det, angle))
		return AffineTransform{}, stats, pairs, fmt.Errorf("transform implausible (scale or rotation out of range): likely false star matches")
	}

	// Residual-shift gate: catches a fit that latched onto false matches and
	// produced a catastrophic 100–2000 px jump. Does not depend on star count.
	if shift := maxCornerShift(result, refWidth, refHeight); shift > maxResidualShiftPx {
		debuglog.Log(fmt.Sprintf("fitCatalogTransform: transform rejected — shift %.0f px > %.0f (pairs=%d, %d/%d stars align, rms=%.2f) — likely false matches", shift, maxResidualShiftPx, len(pairs), support, len(projected), rms))
		return AffineTransform{}, stats, pairs, fmt.Errorf("residual shift too large (%.0f px > %.0f): likely false star matches", shift, maxResidualShiftPx)
	}
	// Low corroboration floor: require at least a few stars to agree.
	if support < tweakRegMinSupport {
		debuglog.Log(fmt.Sprintf("fitCatalogTransform: transform rejected — only %d stars corroborate (need %d), rms=%.2f — likely false matches", support, tweakRegMinSupport, rms))
		return AffineTransform{}, stats, pairs, fmt.Errorf("fit corroborated by too few stars (%d, need %d)", support, tweakRegMinSupport)
	}
	debuglog.Log(fmt.Sprintf("fitCatalogTransform: accepted — %d pairs, %d/%d catalog stars align, shift=%.1f px, rms=%.2f max=%.2f", len(pairs), support, len(projected), maxCornerShift(result, refWidth, refHeight), rms, maxErr))
	return result, stats, pairs, nil
}

// AlignChannelByStars aligns target to ref in shared pixel space using the same
// robust star-matching cascade as the mosaic builder (2D offset histogram →
// mutual proximity → triangle), then fits and applies an affine. It is
// WCS-independent: it assumes the two channels are already roughly co-registered
// (e.g. drizzled to a common grid) and only need a star-based correction, so it
// does not break when channel WCS headers disagree with the actual pixel
// registration. searchRadiusPx is the proximity radius; fitgeom is "rscale" or
// "general". The returned transform maps target pixels to reference pixels.
func AlignChannelByStars(targetPixels []float32, targetWidth, targetHeight int, refPixels []float32, refWidth, refHeight int, searchRadiusPx float64, fitgeom string) ([]float32, AffineTransform, AlignStats, error) {
	if targetWidth <= 0 || targetHeight <= 0 || refWidth <= 0 || refHeight <= 0 {
		return nil, AffineTransform{}, AlignStats{}, fmt.Errorf("invalid image dimensions (target %dx%d, ref %dx%d)", targetWidth, targetHeight, refWidth, refHeight)
	}

	aligned := targetPixels
	if targetWidth != refWidth || targetHeight != refHeight {
		aligned = ResizeChannel(targetPixels, targetWidth, targetHeight, refWidth, refHeight)
		targetWidth, targetHeight = refWidth, refHeight
	}

	const maxCatalogStars = 200
	refStars := ExtractAndLimitStars(refPixels, refWidth, refHeight, 4.0, 3, maxCatalogStars)
	targetStars := ExtractAndLimitStars(aligned, targetWidth, targetHeight, 4.0, 3, maxCatalogStars)
	if len(refStars) < 3 || len(targetStars) < 3 {
		return nil, AffineTransform{}, AlignStats{}, fmt.Errorf("insufficient stars for alignment (ref %d, target %d)", len(refStars), len(targetStars))
	}

	// Target stars already live in reference-pixel space (identity projection), so
	// they play the "projected" role; the fit maps target → ref.
	t, stats, _, err := fitCatalogTransform(targetStars, refStars, refWidth, refHeight, searchRadiusPx, fitgeom)
	if err != nil {
		return nil, AffineTransform{}, stats, err
	}

	// WarpImageToSize samples the source at warpT(outputPixel); we need ref → target.
	warpT, err := InvertAffineTransform(t)
	if err != nil {
		return nil, AffineTransform{}, stats, err
	}
	warped := WarpImageToSize(aligned, refWidth, refHeight, refWidth, refHeight, warpT)
	return warped, t, stats, nil
}

func shouldUpgradeTweakRegFit(pairs []MatchedPair, rscale, general AffineTransform, refWidth, refHeight int) bool {
	if len(pairs) < 6 || refWidth <= 0 || refHeight <= 0 {
		return false
	}
	rRMS, rMax := residualStats(pairs, rscale)
	gRMS, gMax := residualStats(pairs, general)
	if !(gRMS < rRMS && gMax < rMax) {
		return false
	}
	if rRMS <= 1.25 && rMax <= 3.5 {
		return false
	}
	if gRMS > 0.75*rRMS && gMax > 0.8*rMax {
		return false
	}
	w, h, diag := pairBoundsTarget(pairs)
	shortAxis := math.Min(w, h)
	minShortAxis := math.Max(120, 0.20*math.Min(float64(refWidth), float64(refHeight)))
	minDiag := math.Max(250, 0.35*math.Hypot(float64(refWidth), float64(refHeight)))
	return shortAxis >= minShortAxis && diag >= minDiag
}

// tweakRegTransformIsPhysical returns true when the transform looks like a
// plausible WCS residual correction. TweakReg refines a small offset on top
// of the WCS; the result should be nearly identity: scale ≈ 1 and rotation
// < 10°. Transforms with large rotation or extreme scale are almost certainly
// caused by false star-pair matches and should be rejected.
func tweakRegTransformIsPhysical(t AffineTransform) bool {
	det := t.A*t.E - t.B*t.D
	if det <= 0 {
		return false // mirroring is never a valid residual correction
	}
	scale := math.Sqrt(det)
	if scale < 0.85 || scale > 1.15 {
		return false
	}
	// Rotation angle via polar decomposition approximation: atan2((D-B)/2, (A+E)/2).
	// For a pure rscale [a,-b; b,a]: A+E=2a, D-B=2b → atan2(b,a) = rotation. ✓
	angle := math.Atan2(t.D-t.B, t.A+t.E) * 180.0 / math.Pi
	return math.Abs(angle) < 10.0
}

func residualStats(pairs []MatchedPair, t AffineTransform) (rms, maxErr float64) {
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

// sigmaClipPairs removes pairs whose residual under transform t exceeds
// sigma * RMS of all residuals.
func sigmaClipPairs(pairs []MatchedPair, t AffineTransform, sigma float64) []MatchedPair {
	if len(pairs) == 0 {
		return pairs
	}
	// Compute RMS.
	var sumSq float64
	for _, p := range pairs {
		px := t.A*p.RefX + t.B*p.RefY + t.C
		py := t.D*p.RefX + t.E*p.RefY + t.F
		dx := px - p.TargetX
		dy := py - p.TargetY
		sumSq += dx*dx + dy*dy
	}
	rms := math.Sqrt(sumSq / float64(len(pairs)))
	threshold := sigma * rms

	out := pairs[:0:0]
	for _, p := range pairs {
		px := t.A*p.RefX + t.B*p.RefY + t.C
		py := t.D*p.RefX + t.E*p.RefY + t.F
		dx := px - p.TargetX
		dy := py - p.TargetY
		if math.Sqrt(dx*dx+dy*dy) <= threshold {
			out = append(out, p)
		}
	}
	return out
}

// plateScaleArcsecPerPixel returns the native plate scale in arcsec/pixel from
// the CD matrix, or false if unavailable.
func plateScaleArcsecPerPixel(header fitsio.Header) (float64, bool) {
	cd11, ok11 := tryHeaderFloat(header, "CD1_1")
	cd12, ok12 := tryHeaderFloat(header, "CD1_2")
	cd21, ok21 := tryHeaderFloat(header, "CD2_1")
	cd22, ok22 := tryHeaderFloat(header, "CD2_2")
	if ok11 && ok12 && ok21 && ok22 {
		det := math.Abs(cd11*cd22 - cd12*cd21)
		if det > 0 {
			return math.Sqrt(det) * 3600, true
		}
	}
	if cdelt1, ok := tryHeaderFloat(header, "CDELT1"); ok && cdelt1 != 0 {
		return math.Abs(cdelt1) * 3600, true
	}
	return 0, false
}
