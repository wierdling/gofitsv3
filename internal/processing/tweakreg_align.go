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
	// TweakRegCatalogMaxStars is the shared cap for extracted alignment catalogs.
	TweakRegCatalogMaxStars = 500
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
	refStars := ExtractAndLimitStars(refPixels, refWidth, refHeight, 4.0, 3, TweakRegCatalogMaxStars)
	return EstimateTweakRegAlignmentWithRefStars(
		sourcePixels, sourceWidth, sourceHeight,
		mapper, refPixels, refStars, refWidth, refHeight, refHeader,
		searchRadiusArcsec, fitgeom,
	)
}

// transformGlobalSupport counts one-to-one projected/reference correspondences
// within tol pixels after applying t (which maps projected positions to
// reference-pixel space). This validates a candidate against the FULL catalogs
// rather than only matched pairs, without counting duplicate detections near a
// single reference star.
func transformGlobalSupport(projected, refStars []Star, t AffineTransform, tol float64) int {
	return len(pairByTransform(projected, refStars, t, tol))
}

// preferIdentityTweakRegFit keeps an already-corroborated identity alignment
// from being replaced by a candidate that explains no more of the full
// catalogs. A candidate must strictly improve global support; ties go to the
// identity baseline.
func preferIdentityTweakRegFit(projected, refStars []Star, candidate AffineTransform, candidateStats AlignStats) (AffineTransform, AlignStats, bool) {
	identity := AffineTransform{A: 1, E: 1}
	identityStats := statsForTransform(projected, refStars, identity)
	if candidateStats.GlobalInliers <= identityStats.GlobalInliers && identityStats.GlobalInliers >= tweakRegMinSupport {
		return identity, identityStats, true
	}
	return candidate, candidateStats, false
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
	sourceStars := ExtractAndLimitStars(sourcePixels, sourceWidth, sourceHeight, 4.0, 3, TweakRegCatalogMaxStars)
	result, stats, projected, pairs, err := estimateTweakRegFromCatalogs(
		sourceStars, mapper, refStars, refWidth, refHeight, refHeader, searchRadiusArcsec, fitgeom)

	// Fire the debug hook with the image-backed diagnostic data; it is only set
	// during debug-alignment sessions.
	if AlignmentDebugHook != nil {
		AlignmentDebugHook(AlignmentDiag{
			RefPixels:   refPixels,
			RefW:        refWidth,
			RefH:        refHeight,
			RefStars:    refStars,
			SourceStars: projected,
			Pairs:       pairs,
		})
	}
	return result, stats, err
}

// EstimateTweakRegAlignmentFromCatalogs aligns a pre-extracted source star
// catalog to a reference star catalog through the WCS mapper, with no image
// pixels. Given the same source catalog it is exactly equivalent to
// EstimateTweakRegAlignmentWithRefStars; it exists so the mosaic alignment path
// can stream pixels — extract each catalog once, then align on catalogs alone —
// keeping the memory footprint bounded for large mosaics.
func EstimateTweakRegAlignmentFromCatalogs(
	sourceStars []Star,
	mapper *WCSMapper,
	refStars []Star,
	refWidth, refHeight int, refHeader fitsio.Header,
	searchRadiusArcsec float64,
	fitgeom string,
) (AffineTransform, AlignStats, error) {
	result, stats, _, _, err := estimateTweakRegFromCatalogs(
		sourceStars, mapper, refStars, refWidth, refHeight, refHeader, searchRadiusArcsec, fitgeom)
	return result, stats, err
}

// estimateTweakRegFromCatalogs is the catalog-only core shared by the pixel and
// streaming TweakReg entry points. It projects the source catalog into reference
// pixel space through the WCS mapper and fits the residual against the reference
// catalog, returning the projected source stars and matched pairs for diagnostics.
func estimateTweakRegFromCatalogs(
	sourceStars []Star,
	mapper *WCSMapper,
	refStars []Star,
	refWidth, refHeight int, refHeader fitsio.Header,
	searchRadiusArcsec float64,
	fitgeom string,
) (AffineTransform, AlignStats, []Star, []MatchedPair, error) {
	if mapper == nil {
		return AffineTransform{}, AlignStats{}, nil, nil, fmt.Errorf("WCSMapper is required for TweakReg alignment")
	}

	plateScale, ok := plateScaleArcsecPerPixel(refHeader)
	if !ok || plateScale <= 0 {
		plateScale = 0.04
	}
	searchRadiusPx := searchRadiusArcsec / plateScale

	debuglog.Log(fmt.Sprintf("estimateTweakRegFromCatalogs: %d source stars, %d ref stars provided", len(sourceStars), len(refStars)))
	if len(sourceStars) < 2 {
		return AffineTransform{}, AlignStats{}, nil, nil, fmt.Errorf("too few stars in source image (%d)", len(sourceStars))
	}
	if len(refStars) < 2 {
		return AffineTransform{}, AlignStats{}, nil, nil, fmt.Errorf("too few stars in reference image (%d)", len(refStars))
	}

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
	debuglog.Log(fmt.Sprintf("estimateTweakRegFromCatalogs: %d/%d source stars project into reference frame", len(projected), len(sourceStars)))
	if len(projected) < 2 {
		return AffineTransform{}, AlignStats{}, projected, nil, fmt.Errorf("too few source stars project into reference frame (%d)", len(projected))
	}

	result, stats, pairs, err := fitCatalogTransform(projected, refStars, refWidth, refHeight, searchRadiusPx, fitgeom)
	if err != nil {
		return AffineTransform{}, stats, projected, pairs, err
	}
	return result, stats, projected, pairs, nil
}

// FitCatalogResidual fits the residual transform mapping the projected source
// catalog onto the target catalog when both are already expressed in the same
// (reference) pixel space, applying the full robustness cascade and gates used
// by the primary TweakReg path. It is the catalog-only entry point used by the
// mosaic chain fallback to align a frame to an already-aligned intermediate with
// a complete rscale/affine correction — not merely a translation. fitgeom is
// "rscale" or "general".
func FitCatalogResidual(projected, target []Star, refWidth, refHeight int, searchRadiusPx float64, fitgeom string) (AffineTransform, AlignStats, error) {
	t, stats, _, err := fitCatalogTransform(projected, target, refWidth, refHeight, searchRadiusPx, fitgeom)
	return t, stats, err
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
	effectiveFitgeom := fitgeom
	var recoveryRScaleErr error
	if fitgeom == "rscale" {
		rscale, err := SolveRScaleTransformationRANSAC(pairs, 300, 1.5)
		if err != nil {
			debuglog.Log(fmt.Sprintf("fitCatalogTransform: rscale RANSAC failed (pairs=%d projected=%d reference=%d search-radius=%.1f): %v", len(pairs), len(projected), len(refStars), searchRadiusPx, err))
			if len(pairs) < 3 {
				return AffineTransform{}, AlignStats{}, pairs, err
			}
			recoveryRScaleErr = err
			debuglog.Log(fmt.Sprintf("fitCatalogTransform: starting guarded general RANSAC recovery (pairs=%d, rscale error=%v)", len(pairs), err))
			general, generalErr := SolveTransformationRANSAC(pairs, 2000, 1.5)
			if generalErr != nil {
				debuglog.Log(fmt.Sprintf("fitCatalogTransform: guarded general RANSAC recovery failed (pairs=%d): %v", len(pairs), generalErr))
				return AffineTransform{}, AlignStats{}, pairs, fmt.Errorf("rscale RANSAC failed: %w; guarded general recovery failed: %v", err, generalErr)
			}
			result = general
			effectiveFitgeom = "general"
		} else {
			general, generalErr := SolveTransformationRANSAC(pairs, 2000, 1.5)
			if generalErr == nil && shouldUpgradeTweakRegFit(pairs, rscale, general, refWidth, refHeight) {
				rRMS, rMax := residualStats(pairs, rscale)
				gRMS, gMax := residualStats(pairs, general)
				debuglog.Log(fmt.Sprintf("fitCatalogTransform: upgrading fitgeom from rscale to general (pairs=%d, rscale rms=%.2f max=%.2f, general rms=%.2f max=%.2f)", len(pairs), rRMS, rMax, gRMS, gMax))
				result = general
				effectiveFitgeom = "general"
			} else {
				if generalErr != nil {
					debuglog.Log(fmt.Sprintf("fitCatalogTransform: optional general RANSAC comparison failed (pairs=%d): %v", len(pairs), generalErr))
				}
				result = rscale
			}
		}
	} else {
		var err error
		result, err = SolveTransformationRANSAC(pairs, 2000, 1.5)
		if err != nil {
			debuglog.Log(fmt.Sprintf("fitCatalogTransform: general RANSAC failed (pairs=%d projected=%d reference=%d search-radius=%.1f): %v", len(pairs), len(projected), len(refStars), searchRadiusPx, err))
			return AffineTransform{}, AlignStats{}, pairs, err
		}
	}

	rms, maxErr := residualStats(pairs, result)
	support := transformGlobalSupport(projected, refStars, result, tweakRegGlobalTolPx)
	stats := AlignStats{MatchedStars: len(pairs), GlobalInliers: support, RMS: rms, MaxError: maxErr}
	if identity, identityStats, preferred := preferIdentityTweakRegFit(projected, refStars, result, stats); preferred {
		debuglog.Log(fmt.Sprintf("fitCatalogTransform: candidate support %d does not improve identity support %d; preferring identity", support, identityStats.GlobalInliers))
		return identity, identityStats, pairs, nil
	}
	debuglog.Log(fmt.Sprintf("fitCatalogTransform: solved fitgeom=%s transform=[%.8f %.8f %.3f; %.8f %.8f %.3f] pairs=%d support=%d rms=%.3f max=%.3f", effectiveFitgeom, result.A, result.B, result.C, result.D, result.E, result.F, len(pairs), support, rms, maxErr))
	if recoveryRScaleErr != nil {
		debuglog.Log(fmt.Sprintf("fitCatalogTransform: guarded general RANSAC recovery succeeded (pairs=%d, rscale=failed: %v, general=success, selected=general recovery, rms=%.3f max=%.3f, transform=[%.8f %.8f %.3f; %.8f %.8f %.3f])", len(pairs), recoveryRScaleErr, rms, maxErr, result.A, result.B, result.C, result.D, result.E, result.F))
	}

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
	minSupport := tweakRegMinSupport
	if effectiveFitgeom == "general" && len(projected) > tweakRegMinSupport && len(refStars) > tweakRegMinSupport {
		minSupport++
	}
	if support < minSupport {
		debuglog.Log(fmt.Sprintf("fitCatalogTransform: transform rejected — only %d stars corroborate (need %d), rms=%.2f — likely false matches", support, minSupport, rms))
		return AffineTransform{}, stats, pairs, fmt.Errorf("fit corroborated by too few stars (%d, need %d)", support, minSupport)
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

	// Spread the alignment catalog across the frame so the global rotation/scale is
	// well constrained; the brightest-N alone can cluster and leave the fit drifting
	// toward the edges (see selectSpatiallyDistributedStars).
	refStars := selectSpatiallyDistributedStars(ExtractStars(refPixels, refWidth, refHeight, 4.0, 3), refWidth, refHeight, TweakRegCatalogMaxStars)
	targetStars := selectSpatiallyDistributedStars(ExtractStars(aligned, targetWidth, targetHeight, 4.0, 3), targetWidth, targetHeight, TweakRegCatalogMaxStars)
	if len(refStars) < 3 || len(targetStars) < 3 {
		return nil, AffineTransform{}, AlignStats{}, fmt.Errorf("insufficient stars for alignment (ref %d, target %d)", len(refStars), len(targetStars))
	}

	// Target stars already live in reference-pixel space (identity projection), so
	// they play the "projected" role; the fit maps target → ref.
	t, stats, _, err := fitCatalogTransform(targetStars, refStars, refWidth, refHeight, searchRadiusPx, fitgeom)
	if err != nil {
		return nil, AffineTransform{}, stats, err
	}

	// Globally refine: the initial fit may be anchored by a bright cluster (the
	// histogram re-match radius drops stars displaced by residual rotation), so
	// re-pair the full distributed catalog through the current fit and refit a full
	// affine. This pulls in edge stars and pins down the rotation/scale that causes
	// "aligned here, drifting there".
	t, stats = refineGlobalAffine(targetStars, refStars, t, stats, refWidth, refHeight)

	// WarpImageToSize samples the source at warpT(outputPixel); we need ref → target.
	warpT, err := InvertAffineTransform(t)
	if err != nil {
		return nil, AffineTransform{}, stats, err
	}
	warped := WarpImageToSize(aligned, refWidth, refHeight, refWidth, refHeight, warpT)
	return warped, t, stats, nil
}

// pairByTransform pairs target stars to ref stars by nearest neighbour within
// radius, after mapping each target star through t (which maps target → ref). The
// returned pairs carry the ORIGINAL target coordinates as Ref and the matched ref
// coordinates as Target, so a subsequent solve fits the target → ref transform
// (matching residualStats / the rest of this file's convention). A ref star is
// used at most once.
func pairByTransform(targetStars, refStars []Star, t AffineTransform, radius float64) []MatchedPair {
	r2 := radius * radius
	usedRef := make([]bool, len(refStars))
	pairs := make([]MatchedPair, 0, len(targetStars))
	for _, ts := range targetStars {
		px := t.A*ts.X + t.B*ts.Y + t.C
		py := t.D*ts.X + t.E*ts.Y + t.F
		best := -1
		bestD := r2
		for j, rs := range refStars {
			if usedRef[j] {
				continue
			}
			dx := px - rs.X
			dy := py - rs.Y
			if d := dx*dx + dy*dy; d < bestD {
				bestD = d
				best = j
			}
		}
		if best >= 0 {
			usedRef[best] = true
			pairs = append(pairs, MatchedPair{RefX: ts.X, RefY: ts.Y, TargetX: refStars[best].X, TargetY: refStars[best].Y})
		}
	}
	return pairs
}

// statsForTransform reports how well t (target → ref) registers the full
// catalogs: global support within tweakRegGlobalTolPx plus the RMS/max of the
// stars that land within that tolerance.
func statsForTransform(targetStars, refStars []Star, t AffineTransform) AlignStats {
	pairs := pairByTransform(targetStars, refStars, t, tweakRegGlobalTolPx)
	rms, maxErr := residualStats(pairs, t)
	return AlignStats{MatchedStars: len(pairs), GlobalInliers: len(pairs), RMS: rms, MaxError: maxErr}
}

// refineGlobalAffine improves an initial target → ref fit by re-pairing the full
// (spatially distributed) catalogs through the current transform and refitting a
// full affine. Because the initial fit already registers the bulk of the field,
// the re-match now pairs stars across the whole frame at a tight radius, which
// constrains the global rotation/scale/shear and removes edge drift. It iterates a
// few times and keeps a refined transform only when it stays physical and
// corroborates with at least as many stars as the current best.
func refineGlobalAffine(targetStars, refStars []Star, init AffineTransform, initStats AlignStats, refWidth, refHeight int) (AffineTransform, AlignStats) {
	best := init
	bestStats := initStats
	identityBaseline := init == (AffineTransform{A: 1, E: 1})
	if bestStats.GlobalInliers == 0 {
		bestStats = statsForTransform(targetStars, refStars, init)
	}
	current := init
	// Start wide: a small rotation/scale error in a centre-anchored initial fit
	// extrapolates to a large displacement at the corners, so edge stars can sit
	// well beyond a tight radius. Pull them in first, then tighten over passes to
	// lock the global solution.
	radii := []float64{16.0, 8.0, 4.0, 4.0}
	for _, radius := range radii {
		pairs := pairByTransform(targetStars, refStars, current, radius)
		if len(pairs) < 6 {
			break
		}
		cand, err := solveLeastSquares(pairs)
		if err != nil {
			break
		}
		if clipped := sigmaClipPairs(pairs, cand, 3.0); len(clipped) >= 6 {
			if c2, err := solveLeastSquares(clipped); err == nil {
				cand = c2
			}
		}
		if !tweakRegTransformIsPhysical(cand) || maxCornerShift(cand, refWidth, refHeight) > maxResidualShiftPx {
			break
		}
		st := statsForTransform(targetStars, refStars, cand)
		// An exact identity baseline is already a safe no-op: only a strict support
		// increase justifies replacing it. Preserve the historical equal-support
		// behavior for non-identity starting transforms.
		if st.GlobalInliers < bestStats.GlobalInliers || (identityBaseline && best == (AffineTransform{A: 1, E: 1}) && st.GlobalInliers <= bestStats.GlobalInliers) {
			break
		}
		best, bestStats, current = cand, st, cand
	}
	debuglog.Log(fmt.Sprintf("refineGlobalAffine: support %d→%d, rms %.2f→%.2f", initStats.GlobalInliers, bestStats.GlobalInliers, initStats.RMS, bestStats.RMS))
	return best, bestStats
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
