package processing

import "math"

// bundleConstraint is one cross-frame star correspondence, referencing the two
// frames and the local catalog indices that matched. Indices (not coordinates)
// are stored so the residual can be re-evaluated as frames are nudged during the
// adjustment.
type bundleConstraint struct {
	i, li int
	j, lj int
}

const (
	// bundleMatchRadiusPx is the proximity radius for establishing cross-frame
	// correspondences. The inputs are already aligned to within ~1 px, so a tight
	// radius keeps only true matches.
	bundleMatchRadiusPx = 3.0
	// bundleMinMatches is the minimum number of correspondences a frame needs
	// before it is adjusted; below this the per-frame fit is under-constrained.
	bundleMinMatches = 6
	// bundleMaxUpdatePx bounds how far a single adjustment step may move a frame
	// corner. Bundle updates are residual corrections on an already-good
	// alignment, so a large step signals a bad fit and is rejected.
	bundleMaxUpdatePx = 25.0
)

// matchIndicesByProximity returns mutual-nearest-neighbour index pairs (ai, bj)
// between two catalogs within radius. Deterministic: ties resolve by the first
// (lowest-index) candidate encountered.
func matchIndicesByProximity(a, b []Star, radius float64) [][2]int {
	r2 := radius * radius
	bestB := make([]int, len(a))
	bestBd := make([]float64, len(a))
	for i := range bestB {
		bestB[i] = -1
		bestBd[i] = r2
	}
	bestA := make([]int, len(b))
	bestAd := make([]float64, len(b))
	for j := range bestA {
		bestA[j] = -1
		bestAd[j] = r2
	}
	for i, sa := range a {
		for j, sb := range b {
			dx := sa.X - sb.X
			dy := sa.Y - sb.Y
			d := dx*dx + dy*dy
			if d < bestBd[i] {
				bestBd[i] = d
				bestB[i] = j
			}
			if d < bestAd[j] {
				bestAd[j] = d
				bestA[j] = i
			}
		}
	}
	var out [][2]int
	for i, j := range bestB {
		if j >= 0 && bestA[j] == i {
			out = append(out, [2]int{i, j})
		}
	}
	return out
}

func solveByGeom(pairs []MatchedPair, fitgeom string) (AffineTransform, error) {
	if fitgeom == "rscale" {
		return solveRScaleLeastSquares(pairs)
	}
	return solveLeastSquares(pairs)
}

// GlobalBundleAdjust performs a deterministic Gauss-Seidel bundle adjustment over
// frames already projected into a shared reference space, minimizing the residual
// between matched stars across every overlapping frame pair simultaneously rather
// than fitting each frame to the reference independently.
//
// cats[k] is frame k's catalog in reference-pixel space under the current
// solution; fixed[k] marks frames held constant (the reference and any frame that
// must not move). mayOverlap reports whether two frames can share stars (a cheap
// footprint test). fitgeom is "rscale" or "general"; sweeps caps the Gauss-Seidel
// iterations.
//
// It returns, for each frame, a reference-space update transform to LEFT-compose
// onto that frame's current solution (ComposeAffineTransforms(update, current)),
// plus ok=true only when the adjustment strictly reduced the global cross-frame
// residual. Callers must apply the updates only when ok is true; otherwise every
// returned update is the identity. This monotonic gate guarantees the adjustment
// never degrades an alignment — worst case it is a no-op.
func GlobalBundleAdjust(cats [][]Star, fixed []bool, mayOverlap func(a, b int) bool, fitgeom string, refWidth, refHeight, sweeps int) ([]AffineTransform, bool) {
	n := len(cats)
	updates := make([]AffineTransform, n)
	for k := range updates {
		updates[k] = IdentityTransform()
	}
	if n < 2 || mayOverlap == nil {
		return updates, false
	}

	// Working copies, mutated in place as frames are nudged.
	work := make([][]Star, n)
	for k := range cats {
		work[k] = append([]Star(nil), cats[k]...)
	}

	// Fixed correspondences established once from the initial projection. Holding
	// the correspondence set constant makes the optimised quantity a well-defined
	// least-squares objective and keeps the improvement gate meaningful.
	var cons []bundleConstraint
	byFrame := make([][]int, n)
	for a := 0; a < n; a++ {
		for b := a + 1; b < n; b++ {
			if len(work[a]) == 0 || len(work[b]) == 0 || !mayOverlap(a, b) {
				continue
			}
			for _, m := range matchIndicesByProximity(work[a], work[b], bundleMatchRadiusPx) {
				idx := len(cons)
				cons = append(cons, bundleConstraint{i: a, li: m[0], j: b, lj: m[1]})
				byFrame[a] = append(byFrame[a], idx)
				byFrame[b] = append(byFrame[b], idx)
			}
		}
	}
	if len(cons) == 0 {
		return updates, false
	}

	rms := func() float64 {
		var s float64
		for _, c := range cons {
			p := work[c.i][c.li]
			q := work[c.j][c.lj]
			dx := p.X - q.X
			dy := p.Y - q.Y
			s += dx*dx + dy*dy
		}
		return math.Sqrt(s / float64(len(cons)))
	}
	initialRMS := rms()

	applyUpdate := func(k int, u AffineTransform) {
		for t := range work[k] {
			x, y := ApplyAffineTransform(u, work[k][t].X, work[k][t].Y)
			work[k][t].X = x
			work[k][t].Y = y
		}
		updates[k] = ComposeAffineTransforms(u, updates[k])
	}

	for sweep := 0; sweep < sweeps; sweep++ {
		moved := false
		for k := 0; k < n; k++ {
			if fixed[k] || len(byFrame[k]) < bundleMinMatches {
				continue
			}
			// Build pairs that map frame k's current positions onto the matched
			// positions in its (current) neighbours.
			pairs := make([]MatchedPair, 0, len(byFrame[k]))
			for _, ci := range byFrame[k] {
				c := cons[ci]
				var src, tgt Star
				if c.i == k {
					src, tgt = work[c.i][c.li], work[c.j][c.lj]
				} else {
					src, tgt = work[c.j][c.lj], work[c.i][c.li]
				}
				pairs = append(pairs, MatchedPair{RefX: src.X, RefY: src.Y, TargetX: tgt.X, TargetY: tgt.Y})
			}
			u, err := solveByGeom(pairs, fitgeom)
			if err != nil {
				continue
			}
			if clipped := sigmaClipPairs(pairs, u, 3.0); len(clipped) >= bundleMinMatches {
				if u2, err := solveByGeom(clipped, fitgeom); err == nil {
					u = u2
				}
			}
			if !tweakRegTransformIsPhysical(u) || maxCornerShift(u, refWidth, refHeight) > bundleMaxUpdatePx {
				continue
			}
			applyUpdate(k, u)
			moved = true
		}
		if !moved {
			break
		}
	}

	if rms() < initialRMS*0.999 {
		return updates, true
	}
	for k := range updates {
		updates[k] = IdentityTransform()
	}
	return updates, false
}
