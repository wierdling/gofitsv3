package processing

import (
	"fmt"
	"math"

	"gofitsv3/internal/catalog/gaia"
)

// GaiaProjectedStar keeps the source identity separate from the image-star
// measurements. In particular, SourceID must not be narrowed into Star.Area.
type GaiaProjectedStar struct {
	Star     Star
	SourceID uint64
}

// ProjectGaiaSources converts propagated Gaia positions into nominal image
// pixels without lossy source-ID conversion.
func ProjectGaiaSources(sources []gaia.Source, epoch float64, skyToPixel func(gaia.Coordinate) (float64, float64, error)) ([]GaiaProjectedStar, error) {
	if skyToPixel == nil {
		return nil, fmt.Errorf("sky-to-pixel projection is required")
	}
	out := make([]GaiaProjectedStar, 0, len(sources))
	for _, source := range sources {
		x, y, err := skyToPixel(PropagateGaiaPosition(source, epoch))
		if err != nil {
			return nil, err
		}
		if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
			continue
		}
		out = append(out, GaiaProjectedStar{Star: Star{X: x, Y: y}, SourceID: source.SourceID})
	}
	return out, nil
}

// RefinedPixelToSky applies the inverse of a residual transform before the
// nominal projection. A residual maps nominal Gaia pixels to observed pixels;
// therefore observed detections must first be mapped back to nominal space.
func RefinedPixelToSky(pixelToSky func(float64, float64) (gaia.Coordinate, error), residual AffineTransform) (func(float64, float64) (gaia.Coordinate, error), error) {
	if pixelToSky == nil {
		return nil, fmt.Errorf("pixel-to-sky projection is required")
	}
	inverse, err := InvertAffineTransform(residual)
	if err != nil {
		return nil, err
	}
	return func(x, y float64) (gaia.Coordinate, error) {
		nx, ny := ApplyAffineTransform(inverse, x, y)
		return pixelToSky(nx, ny)
	}, nil
}

// FitGaiaResidual fits the transform from nominal Gaia projected pixels to
// observed selected/detected pixels. It is a thin, named handoff to the same
// robust residual fitter used by alignment.
func FitGaiaResidual(projected, observed []Star, width, height int, searchRadiusPx float64, fitgeom string) (AffineTransform, AlignStats, error) {
	return FitCatalogResidual(projected, observed, width, height, searchRadiusPx, fitgeom)
}

// FitGaiaSourceResidual projects a discovered Gaia slice once and fits its
// residual against the user-selected observed catalog.
func FitGaiaSourceResidual(sources []gaia.Source, observed []Star, epoch float64, skyToPixel func(gaia.Coordinate) (float64, float64, error), width, height int, searchRadiusPx float64, fitgeom string) (AffineTransform, AlignStats, error) {
	projectedWithIDs, err := ProjectGaiaSources(sources, epoch, skyToPixel)
	if err != nil {
		return AffineTransform{}, AlignStats{}, err
	}
	projected := make([]Star, len(projectedWithIDs))
	for i := range projectedWithIDs {
		projected[i] = projectedWithIDs[i].Star
	}
	return FitGaiaResidual(projected, observed, width, height, searchRadiusPx, fitgeom)
}
