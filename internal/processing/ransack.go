package processing

import (
	"errors"
	"math"
	"math/rand"
	"time"

	"gonum.org/v1/gonum/mat"
)

// SolveTransformationRANSAC calculates the optimal Affine matrix while aggressively rejecting false matches.
// iterations: typically 2000 for geometric matching.
// threshold: max pixel distance for a point to be considered an inlier (e.g., 2.0).
func SolveTransformationRANSAC(pairs []MatchedPair, iterations int, threshold float64) (AffineTransform, error) {
	n := len(pairs)
	if n < 3 {
		return AffineTransform{}, errors.New("at least 3 matched pairs are required")
	}

	// If we only have exactly 3, just do a direct solve without RANSAC
	if n == 3 {
		return solveLeastSquares(pairs)
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var bestInliers []MatchedPair
	maxInlierCount := 0

	for i := 0; i < iterations; i++ {
		// 1. Pick 3 random distinct pairs
		sample := getThreeRandomPairs(pairs, rng)

		// 2. Solve the matrix for just these 3 pairs
		transform, err := solveLeastSquares(sample)
		if err != nil {
			continue // Collinear points might cause a singular matrix
		}

		// 3. Test all points against this transformation
		var currentInliers []MatchedPair
		for _, p := range pairs {
			// Predict where the Reference point lands in the Target image
			predX := transform.A*p.RefX + transform.B*p.RefY + transform.C
			predY := transform.D*p.RefX + transform.E*p.RefY + transform.F

			// Calculate Euclidean distance error
			dx := predX - p.TargetX
			dy := predY - p.TargetY
			dist := math.Sqrt(dx*dx + dy*dy)

			if dist <= threshold {
				currentInliers = append(currentInliers, p)
			}
		}

		// 4. Keep the model with the most inliers
		if len(currentInliers) > maxInlierCount {
			maxInlierCount = len(currentInliers)
			bestInliers = currentInliers
		}
	}

	if maxInlierCount < 3 {
		return AffineTransform{}, errors.New("RANSAC failed to find a valid consensus model")
	}

	// 5. Final Least Squares solve using ONLY the confirmed inliers
	return solveLeastSquares(bestInliers)
}

// solveLeastSquares is your original gonum solver, extracted into a private helper.
func solveLeastSquares(pairs []MatchedPair) (AffineTransform, error) {
	n := len(pairs)
	aData := make([]float64, n*3)
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
	if err := vecX.SolveVec(A, bx); err != nil {
		return AffineTransform{}, err
	}
	if err := vecY.SolveVec(A, by); err != nil {
		return AffineTransform{}, err
	}

	return AffineTransform{
		A: vecX.AtVec(0), B: vecX.AtVec(1), C: vecX.AtVec(2),
		D: vecY.AtVec(0), E: vecY.AtVec(1), F: vecY.AtVec(2),
	}, nil
}

func getThreeRandomPairs(pairs []MatchedPair, rng *rand.Rand) []MatchedPair {
	n := len(pairs)
	indices := rng.Perm(n)[:3]
	return []MatchedPair{pairs[indices[0]], pairs[indices[1]], pairs[indices[2]]}
}
