package processing

import (
	"errors"
	"math"
	"math/rand"

	"gonum.org/v1/gonum/mat"

	"gofitsv3/internal/debuglog"
)

// deterministicSeed derives a stable RANSAC seed from the matched pairs so the
// solver returns the same transform for the same input on every run. Previously
// the seed came from the wall clock, which made alignment non-reproducible: with
// any ambiguous matches one run could lock onto the true consensus and the next
// onto a false one, producing wildly different (sometimes >100 px off) results
// for identical inputs. Determinism is required for reliable alignment.
func deterministicSeed(pairs []MatchedPair) int64 {
	const (
		offset uint64 = 1469598103934665603
		prime  uint64 = 1099511628211
	)
	h := offset
	mix := func(f float64) {
		// Quantize to 1/100 px so trivial float noise can't change the seed.
		bits := math.Float64bits(math.Round(f*100) / 100)
		for s := 0; s < 64; s += 8 {
			h ^= (bits >> uint(s)) & 0xff
			h *= prime
		}
	}
	h ^= uint64(len(pairs))
	h *= prime
	for _, p := range pairs {
		mix(p.RefX)
		mix(p.RefY)
		mix(p.TargetX)
		mix(p.TargetY)
	}
	return int64(h)
}

// SolveTransformationRANSAC calculates the optimal Affine matrix while aggressively rejecting false matches.
// iterations: typically 2000 for geometric matching.
// threshold: max pixel distance for a point to be considered an inlier (e.g., 2.0).
func SolveTransformationRANSAC(pairs []MatchedPair, iterations int, threshold float64) (AffineTransform, error) {
	debuglog.Log("SolveTransformationRANSAC: starting")
	defer debuglog.Log("SolveTransformationRANSAC: finished")
	n := len(pairs)
	if n < 3 {
		return AffineTransform{}, errors.New("at least 3 matched pairs are required")
	}

	// If we only have exactly 3, just do a direct solve without RANSAC
	if n == 3 {
		return solveLeastSquares(pairs)
	}

	rng := rand.New(rand.NewSource(deterministicSeed(pairs)))
	thresholdSq := threshold * threshold
	var bestInliers []MatchedPair
	maxInlierCount := 0
	bestInlierSSE := math.Inf(1)

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
		currentSSE := 0.0
		for _, p := range pairs {
			// Predict where the Reference point lands in the Target image
			predX := transform.A*p.RefX + transform.B*p.RefY + transform.C
			predY := transform.D*p.RefX + transform.E*p.RefY + transform.F

			// Squared Euclidean distance error (avoids a Sqrt per point)
			dx := predX - p.TargetX
			dy := predY - p.TargetY
			distSq := dx*dx + dy*dy

			if distSq <= thresholdSq {
				currentInliers = append(currentInliers, p)
				currentSSE += distSq
			}
		}

		// 4. Keep the model with the most inliers, breaking ties on the lower
		// inlier residual (SSE). Without the tie-break the first equally-supported
		// model found wins, which is deterministic but not necessarily the best
		// geometric fit; preferring lower SSE picks the tightest consensus.
		if len(currentInliers) > maxInlierCount ||
			(len(currentInliers) == maxInlierCount && currentSSE < bestInlierSSE) {
			maxInlierCount = len(currentInliers)
			bestInlierSSE = currentSSE
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

// SolveRScaleTransformationRANSAC calculates the optimal similarity transform
// (translation + rotation + uniform scale) while rejecting false matches.
// iterations: typically a few hundred for post-WCS refinement.
// threshold: max pixel distance for a point to be considered an inlier.
func SolveRScaleTransformationRANSAC(pairs []MatchedPair, iterations int, threshold float64) (AffineTransform, error) {
	debuglog.Log("SolveRScaleTransformationRANSAC: starting")
	defer debuglog.Log("SolveRScaleTransformationRANSAC: finished")
	n := len(pairs)
	if n < 2 {
		return AffineTransform{}, errors.New("at least 2 matched pairs are required")
	}

	if n == 2 {
		return solveRScaleLeastSquares(pairs)
	}

	rng := rand.New(rand.NewSource(deterministicSeed(pairs)))
	thresholdSq := threshold * threshold
	minInliers := 3
	if n >= 8 {
		minInliers = 4
	}

	bestTransform := AffineTransform{}
	bestInlierCount := 0
	bestInlierSSE := math.Inf(1)

	for i := 0; i < iterations; i++ {
		sample := getRandomPairs(pairs, rng, 2)
		if isDegenerateRScaleSample(sample) {
			continue
		}

		transform, err := solveRScaleLeastSquares(sample)
		if err != nil {
			continue
		}

		currentInlierCount := 0
		currentInlierSSE := 0.0
		for _, p := range pairs {
			predX := transform.A*p.RefX + transform.B*p.RefY + transform.C
			predY := transform.D*p.RefX + transform.E*p.RefY + transform.F
			dx := predX - p.TargetX
			dy := predY - p.TargetY
			dsq := dx*dx + dy*dy
			if dsq <= thresholdSq {
				currentInlierCount++
				currentInlierSSE += dsq
			}
		}

		if currentInlierCount > bestInlierCount || (currentInlierCount == bestInlierCount && currentInlierSSE < bestInlierSSE) {
			bestInlierCount = currentInlierCount
			bestInlierSSE = currentInlierSSE
			bestTransform = transform
			if bestInlierCount >= n*9/10 && bestInlierSSE/float64(bestInlierCount) <= 0.25*thresholdSq {
				break
			}
		}
	}

	if bestInlierCount < minInliers {
		return AffineTransform{}, errors.New("RANSAC failed to find a valid consensus model")
	}

	bestInliers := make([]MatchedPair, 0, bestInlierCount)
	for _, p := range pairs {
		predX := bestTransform.A*p.RefX + bestTransform.B*p.RefY + bestTransform.C
		predY := bestTransform.D*p.RefX + bestTransform.E*p.RefY + bestTransform.F
		dx := predX - p.TargetX
		dy := predY - p.TargetY
		if dx*dx+dy*dy <= thresholdSq {
			bestInliers = append(bestInliers, p)
		}
	}

	finalTransform, err := solveRScaleLeastSquares(bestInliers)
	if err != nil {
		return AffineTransform{}, err
	}
	if rms := rscaleInlierRMS(bestInliers, finalTransform); rms > threshold {
		return AffineTransform{}, errors.New("RANSAC consensus was too inconsistent for a stable rscale fit")
	}
	return finalTransform, nil
}

func isDegenerateRScaleSample(pairs []MatchedPair) bool {
	if len(pairs) < 2 {
		return true
	}
	const minSpan = 20.0
	dxRef := pairs[1].RefX - pairs[0].RefX
	dyRef := pairs[1].RefY - pairs[0].RefY
	dxTarget := pairs[1].TargetX - pairs[0].TargetX
	dyTarget := pairs[1].TargetY - pairs[0].TargetY
	return math.Hypot(dxRef, dyRef) < minSpan || math.Hypot(dxTarget, dyTarget) < minSpan
}

func rscaleInlierRMS(pairs []MatchedPair, t AffineTransform) float64 {
	if len(pairs) == 0 {
		return 0
	}
	var sumSq float64
	for _, p := range pairs {
		predX := t.A*p.RefX + t.B*p.RefY + t.C
		predY := t.D*p.RefX + t.E*p.RefY + t.F
		dx := predX - p.TargetX
		dy := predY - p.TargetY
		sumSq += dx*dx + dy*dy
	}
	return math.Sqrt(sumSq / float64(len(pairs)))
}

// solveRScaleLeastSquares fits a similarity transform [a,-b,tx; b,a,ty] to the
// matched pairs using the closed-form normal equations.  Avoids all matrix
// allocation — critical because this is called inside the RANSAC loop.
//
// Design matrix rows: [xi,-yi,1,0] and [yi,xi,0,1] mapping to [ux,uy].
// Solving AtA·x=Atb analytically yields:
//
//	D  = Σ(xi²+yi²) − (Σxi)²/n − (Σyi)²/n
//	a  = [Σ(xi·ux+yi·uy) − (Σxi·Σux+Σyi·Σuy)/n] / D
//	b  = [Σ(xi·uy−yi·ux) + (Σyi·Σux−Σxi·Σuy)/n] / D
//	tx = (Σux − Σxi·a + Σyi·b) / n
//	ty = (Σuy − Σyi·a − Σxi·b) / n
func solveRScaleLeastSquares(pairs []MatchedPair) (AffineTransform, error) {
	n := len(pairs)
	if n < 2 {
		return AffineTransform{}, errors.New("at least 2 matched pairs are required for an rscale transform")
	}

	var s2, sx, sy, sux, suy, sxuxYuy, sxuyYux float64
	for _, p := range pairs {
		xi, yi := p.RefX, p.RefY
		ux, uy := p.TargetX, p.TargetY
		s2 += xi*xi + yi*yi
		sx += xi
		sy += yi
		sux += ux
		suy += uy
		sxuxYuy += xi*ux + yi*uy
		sxuyYux += xi*uy - yi*ux
	}
	fn := float64(n)
	D := s2 - (sx*sx+sy*sy)/fn
	if math.Abs(D) < 1e-15 {
		return AffineTransform{}, errors.New("degenerate rscale transform: reference points are coincident")
	}
	a := (sxuxYuy - (sx*sux+sy*suy)/fn) / D
	b := (sxuyYux + (sy*sux-sx*suy)/fn) / D
	tx := (sux - sx*a + sy*b) / fn
	ty := (suy - sy*a - sx*b) / fn

	if math.Abs(a)+math.Abs(b) < 1e-15 {
		return AffineTransform{}, errors.New("degenerate rscale transform")
	}
	return AffineTransform{
		A: a, B: -b, C: tx,
		D: b, E: a, F: ty,
	}, nil
}

func getThreeRandomPairs(pairs []MatchedPair, rng *rand.Rand) []MatchedPair {
	n := len(pairs)
	indices := rng.Perm(n)[:3]
	return []MatchedPair{pairs[indices[0]], pairs[indices[1]], pairs[indices[2]]}
}

func getRandomPairs(pairs []MatchedPair, rng *rand.Rand, count int) []MatchedPair {
	n := len(pairs)
	indices := rng.Perm(n)[:count]
	out := make([]MatchedPair, count)
	for i, idx := range indices {
		out[i] = pairs[idx]
	}
	return out
}
