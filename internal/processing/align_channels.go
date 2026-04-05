package processing

import "fmt"

func AlignChannel(targetPixels []float32, targetWidth, targetHeight int, refPixels []float32, refWidth, refHeight int) ([]float32, AffineTransform, error) {
	if targetWidth <= 0 || targetHeight <= 0 || refWidth <= 0 || refHeight <= 0 {
		return nil, AffineTransform{}, fmt.Errorf("invalid image dimensions (target: %dx%d, ref: %dx%d)", targetWidth, targetHeight, refWidth, refHeight)
	}

	alignedTarget := targetPixels
	if targetWidth != refWidth || targetHeight != refHeight {
		alignedTarget = ResizeChannel(targetPixels, targetWidth, targetHeight, refWidth, refHeight)
		targetWidth = refWidth
		targetHeight = refHeight
	}

	refStars := ExtractStars(refPixels, refWidth, refHeight, 4.0, 3)
	targetStars := ExtractStars(alignedTarget, targetWidth, targetHeight, 4.0, 3)

	if len(refStars) < 3 || len(targetStars) < 3 {
		return nil, AffineTransform{}, fmt.Errorf("insufficient stars found for alignment (ref: %d, target: %d)", len(refStars), len(targetStars))
	}

	pairs := MatchStars(refStars, targetStars, 30, 0.01)
	if len(pairs) < 3 {
		return nil, AffineTransform{}, fmt.Errorf("failed to match at least 3 star pairs (found %d)", len(pairs))
	}

	transform, err := SolveTransformationRANSAC(pairs, 2000, 2.0)
	if err != nil {
		return nil, AffineTransform{}, fmt.Errorf("RANSAC solve failed: %w", err)
	}

	alignedPixels := WarpImage(alignedTarget, refWidth, refHeight, transform)
	return alignedPixels, transform, nil
}
