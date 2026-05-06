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
func EstimateTweakRegAlignment(
	sourcePixels []float32, sourceWidth, sourceHeight int,
	mapper *WCSMapper,
	refPixels []float32, refWidth, refHeight int, refHeader fitsio.Header,
	searchRadiusArcsec float64,
	fitgeom string,
) (AffineTransform, error) {
	debuglog.Log("EstimateTweakRegAlignment: starting")
	defer debuglog.Log("EstimateTweakRegAlignment: finished")
	const maxCatalogStars = 200
	refStars := ExtractAndLimitStars(refPixels, refWidth, refHeight, 4.0, 3, maxCatalogStars)
	return EstimateTweakRegAlignmentWithRefStars(
		sourcePixels, sourceWidth, sourceHeight,
		mapper, refStars, refWidth, refHeight, refHeader,
		searchRadiusArcsec, fitgeom,
	)
}

// EstimateTweakRegAlignmentWithRefStars is like EstimateTweakRegAlignment but
// accepts a pre-extracted reference catalog. Use this when aligning multiple
// source images to the same reference so extraction only happens once.
func EstimateTweakRegAlignmentWithRefStars(
	sourcePixels []float32, sourceWidth, sourceHeight int,
	mapper *WCSMapper,
	refStars []Star,
	refWidth, refHeight int, refHeader fitsio.Header,
	searchRadiusArcsec float64,
	fitgeom string,
) (AffineTransform, error) {
	if mapper == nil {
		return AffineTransform{}, fmt.Errorf("WCSMapper is required for TweakReg alignment")
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
		return AffineTransform{}, fmt.Errorf("too few stars in source image (%d)", len(sourceStars))
	}
	if len(refStars) < 2 {
		return AffineTransform{}, fmt.Errorf("too few stars in reference image (%d)", len(refStars))
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
	debuglog.Log(fmt.Sprintf("EstimateTweakRegAlignmentWithRefStars: %d/%d source stars project into reference frame", len(projected), len(sourceStars)))
	if len(projected) < 2 {
		return AffineTransform{}, fmt.Errorf("too few source stars project into reference frame (%d)", len(projected))
	}

	// Iterative match + fit: start wide, sigma-clip, tighten.
	var pairs []MatchedPair
	currentRadius := searchRadiusPx
	for iter := 0; iter < 3; iter++ {
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

	minPairs := 3
	if fitgeom == "rscale" {
		minPairs = 2
	}
	debuglog.Log(fmt.Sprintf("EstimateTweakRegAlignmentWithRefStars: %d pairs after matching (need %d)", len(pairs), minPairs))
	if len(pairs) < minPairs {
		return AffineTransform{}, fmt.Errorf("insufficient matched pairs after TweakReg matching (%d, need %d)", len(pairs), minPairs)
	}

	if fitgeom == "rscale" {
		rscale, err := SolveRScaleTransformationRANSAC(pairs, 300, 1.5)
		if err != nil {
			return AffineTransform{}, err
		}
		general, generalErr := SolveTransformationRANSAC(pairs, 2000, 1.5)
		if generalErr == nil && shouldUpgradeTweakRegFit(pairs, rscale, general, refWidth, refHeight) {
			rRMS, rMax := residualStats(pairs, rscale)
			gRMS, gMax := residualStats(pairs, general)
			debuglog.Log(fmt.Sprintf("EstimateTweakRegAlignmentWithRefStars: upgrading fitgeom from rscale to general (pairs=%d, rscale rms=%.2f max=%.2f, general rms=%.2f max=%.2f)", len(pairs), rRMS, rMax, gRMS, gMax))
			return general, nil
		}
		return rscale, nil
	}
	return SolveTransformationRANSAC(pairs, 2000, 1.5)
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
