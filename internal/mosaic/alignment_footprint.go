package mosaic

import (
	"fmt"
	"math"

	"gofitsv3/internal/processing"
)

// referenceStarsForTarget limits a full reference catalog to the part of the
// reference grid a target can occupy. Sampling every edge (rather than just
// corners) remains conservative for mildly nonlinear WCS mappings while
// avoiding a global bright-star bias in multi-tile reference mosaics.
func referenceStarsForTarget(target, reference Input, mapper *processing.WCSMapper, full []processing.Star, searchRadiusArcsec float64) ([]processing.Star, error) {
	if mapper == nil || target.HDU.Data.Width <= 0 || target.HDU.Data.Height <= 0 || reference.HDU.Data.Width <= 0 || reference.HDU.Data.Height <= 0 {
		return nil, fmt.Errorf("invalid alignment footprint geometry")
	}
	points := make([][2]float64, 0, 36)
	const edgeSamples = 8
	for i := 0; i <= edgeSamples; i++ {
		t := float64(i) / float64(edgeSamples)
		points = append(points,
			[2]float64{t * float64(target.HDU.Data.Width), 0},
			[2]float64{t * float64(target.HDU.Data.Width), float64(target.HDU.Data.Height)},
			[2]float64{0, t * float64(target.HDU.Data.Height)},
			[2]float64{float64(target.HDU.Data.Width), t * float64(target.HDU.Data.Height)})
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, p := range points {
		x, y := mapper.MapPixel(p[0], p[1])
		if !finite(x) || !finite(y) {
			return nil, fmt.Errorf("alignment footprint mapping returned non-finite coordinates")
		}
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minY, maxY = math.Min(minY, y), math.Max(maxY, y)
	}
	plateScale, ok := nativePlateScaleArcsec(reference.HDU.Header)
	if !ok || plateScale <= 0 {
		plateScale = 0.04
	}
	pad := 150.0 // the estimator's physical residual gate
	if searchRadiusArcsec > 0 && searchRadiusArcsec/plateScale > pad {
		pad = searchRadiusArcsec / plateScale
	}
	minX -= pad
	maxX += pad
	minY -= pad
	maxY += pad
	local := make([]processing.Star, 0, len(full))
	for _, star := range full {
		if !finite(star.X) || !finite(star.Y) {
			continue
		}
		if star.X >= minX && star.X <= maxX && star.Y >= minY && star.Y <= maxY {
			local = append(local, star)
		}
	}
	if len(local) <= processing.TweakRegCatalogMaxStars {
		return local, nil
	}
	// The spatial limiter expects frame-local coordinates. Shift only its
	// working copy into the footprint bounds, then restore the original full
	// reference-grid coordinates before returning the catalog to TweakReg.
	shifted := make([]processing.Star, len(local))
	for i, star := range local {
		shifted[i] = star
		shifted[i].X -= minX
		shifted[i].Y -= minY
	}
	width := int(math.Ceil(maxX - minX))
	height := int(math.Ceil(maxY - minY))
	selected := processing.SelectSpatiallyDistributedStars(shifted, width, height, processing.TweakRegCatalogMaxStars)
	for i := range selected {
		selected[i].X += minX
		selected[i].Y += minY
	}
	return selected, nil
}
