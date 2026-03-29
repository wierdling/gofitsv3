package processing

import (
	"fmt"
)

// AlignChannel takes a raw target image and aligns it to a raw reference image.
// It returns a new float64 array of the aligned target pixels.
func AlignChannel(targetPixels, refPixels []float64, width, height int) ([]float64, AffineTransform, error) {
	// 1. Extract stars from raw linear data
	// 4.0 sigma threshold, minimum 3 pixels area to reject cosmic rays
	refStars := ExtractStars(refPixels, width, height, 4.0, 3)
	targetStars := ExtractStars(targetPixels, width, height, 4.0, 3)

	if len(refStars) < 3 || len(targetStars) < 3 {
		return nil, AffineTransform{}, fmt.Errorf("insufficient stars found for alignment (ref: %d, target: %d)", len(refStars), len(targetStars))
	}

	// 2. Match triangles using the top 30 brightest stars
	pairs := MatchStars(refStars, targetStars, 30, 0.01)
	if len(pairs) < 3 {
		return nil, AffineTransform{}, fmt.Errorf("failed to match at least 3 star pairs (found %d)", len(pairs))
	}

	// 3. Solve the Affine matrix
	transform, err := SolveTransformationRANSAC(pairs, 2000, 2.0)
	if err != nil {
		return nil, AffineTransform{}, fmt.Errorf("RANSAC solve failed: %w", err)
	}

	// 4. Warp the raw target pixels to match the reference
	alignedPixels := WarpImage(targetPixels, width, height, transform)

	return alignedPixels, transform, nil
}
