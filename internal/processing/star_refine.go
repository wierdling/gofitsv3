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

	refStars := ExtractStars(maskedRef, refWidth, refHeight, 4.0, 3)
	targetStars := ExtractStars(maskedTarget, refWidth, refHeight, 4.0, 3)
	if len(refStars) < 3 || len(targetStars) < 3 {
		return 0, 0, fmt.Errorf("insufficient stars in shared region (ref: %d, target: %d)", len(refStars), len(targetStars))
	}

	pairs := MatchStars(refStars, targetStars, 40, 0.02)
	if len(pairs) < 3 {
		return 0, 0, fmt.Errorf("failed to match enough stars in shared region (found %d)", len(pairs))
	}

	dxs := make([]float64, 0, len(pairs))
	dys := make([]float64, 0, len(pairs))
	for _, pair := range pairs {
		dx := pair.RefX - pair.TargetX
		dy := pair.RefY - pair.TargetY
		if math.Abs(dx) > 10 || math.Abs(dy) > 10 {
			continue
		}
		dxs = append(dxs, dx)
		dys = append(dys, dy)
	}
	if len(dxs) < 3 || len(dys) < 3 {
		return 0, 0, fmt.Errorf("insufficient close star matches within 10 pixels")
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
	const searchRadius = 30.0
	var dxs, dys []float64
	for _, rs := range refStars {
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
