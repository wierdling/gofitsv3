package processing

import (
	"errors"
	"math"

	"gonum.org/v1/gonum/mat"
)

// AffineTransform holds the 6 parameters mapping Destination (Ref) to Source (Target).
type AffineTransform struct {
	A, B, C float64 // X parameters
	D, E, F float64 // Y parameters
}

// SolveTransformation calculates the optimal Affine matrix using Least Squares.
func SolveTransformation(pairs []MatchedPair) (AffineTransform, error) {
	n := len(pairs)
	if n < 3 {
		return AffineTransform{}, errors.New("at least 3 matched pairs are required for an affine transform")
	}

	// Matrix A holds the Reference coordinates (Destination)
	aData := make([]float64, n*3)
	// Vectors bx and by hold the Target coordinates (Source)
	bxData := make([]float64, n)
	byData := make([]float64, n)

	for i, p := range pairs {
		aData[i*3+0] = p.RefX
		aData[i*3+1] = p.RefY
		aData[i*3+2] = 1.0

		bxData[i] = p.TargetX
		byData[i] = p.TargetY
	}

	A := mat.NewDense(n, 3, aData)
	bx := mat.NewVecDense(n, bxData)
	by := mat.NewVecDense(n, byData)

	var vecX, vecY mat.VecDense

	// Solve A * [A, B, C]^T = bx
	if err := vecX.SolveVec(A, bx); err != nil {
		return AffineTransform{}, err
	}

	// Solve A * [D, E, F]^T = by
	if err := vecY.SolveVec(A, by); err != nil {
		return AffineTransform{}, err
	}

	return AffineTransform{
		A: vecX.AtVec(0),
		B: vecX.AtVec(1),
		C: vecX.AtVec(2),
		D: vecY.AtVec(0),
		E: vecY.AtVec(1),
		F: vecY.AtVec(2),
	}, nil
}

// WarpImage applies the inverse transformation matrix using bilinear interpolation.
// It returns a new pixel array matching the dimensions of the reference image.
func WarpImage(targetPixels []float64, width, height int, t AffineTransform) []float64 {
	out := make([]float64, width*height)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// Calculate the exact float coordinate in the original target image
			srcX := t.A*float64(x) + t.B*float64(y) + t.C
			srcY := t.D*float64(x) + t.E*float64(y) + t.F

			outIdx := y*width + x

			// Get the integer bounds of the 4 surrounding pixels
			x0 := int(math.Floor(srcX))
			y0 := int(math.Floor(srcY))
			x1 := x0 + 1
			y1 := y0 + 1

			// Bounds checking: if it maps outside the original image, set to 0 or NaN
			if x0 < 0 || x1 >= width || y0 < 0 || y1 >= height {
				out[outIdx] = 0 // Or math.NaN() depending on how your stretch pipeline handles edges
				continue
			}

			// Calculate weights based on fractional distance
			wx := srcX - float64(x0)
			wy := srcY - float64(y0)

			// Fetch the 4 surrounding pixel values
			p00 := targetPixels[y0*width+x0]
			p10 := targetPixels[y0*width+x1]
			p01 := targetPixels[y1*width+x0]
			p11 := targetPixels[y1*width+x1]

			// If the raw data has NaNs (from previous masking), interpolation is voided
			if math.IsNaN(p00) || math.IsNaN(p10) || math.IsNaN(p01) || math.IsNaN(p11) {
				out[outIdx] = math.NaN()
				continue
			}

			// Apply Bilinear Interpolation
			val := p00*(1-wx)*(1-wy) + p10*wx*(1-wy) + p01*(1-wx)*wy + p11*wx*wy
			out[outIdx] = val
		}
	}

	return out
}
