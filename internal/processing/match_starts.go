package processing

import (
	"math"
	"sort"
)

// MatchedPair holds the coordinate pairs that are confirmed to be the same star.
type MatchedPair struct {
	RefX, RefY       float64
	TargetX, TargetY float64
	Votes            int
}

type triangle struct {
	V1, V2, V3 int     // Indices of the stars in the original array
	RatioX     float64 // a/c
	RatioY     float64 // b/c
}

// MatchStars uses a triangle-matching voting algorithm to pair stars.
// tolerance is usually between 0.005 and 0.01.
func MatchStars(refStars, targetStars []Star, maxStars int, tolerance float64) []MatchedPair {
	// 1. Limit to the brightest stars to prevent combinatorial explosion
	if len(refStars) > maxStars {
		refStars = refStars[:maxStars]
	}
	if len(targetStars) > maxStars {
		targetStars = targetStars[:maxStars]
	}

	// 2. Generate Triangles
	refTriangles := generateTriangles(refStars)
	targetTriangles := generateTriangles(targetStars)

	// 3. Voting Matrix [refIndex][targetIndex]
	votes := make([][]int, len(refStars))
	for i := range votes {
		votes[i] = make([]int, len(targetStars))
	}

	// 4. Match Triangles
	// For N=20, 1140 triangles per image. 1.3M comparisons is virtually instantaneous in Go.
	for _, rt := range refTriangles {
		for _, tt := range targetTriangles {
			if math.Abs(rt.RatioX-tt.RatioX) < tolerance && math.Abs(rt.RatioY-tt.RatioY) < tolerance {
				// Triangles match! Add a vote for each corresponding vertex.
				// Because the generateTriangles function orders V1, V2, V3 based on side length,
				// V1 in ref corresponds to V1 in target, etc.
				votes[rt.V1][tt.V1]++
				votes[rt.V2][tt.V2]++
				votes[rt.V3][tt.V3]++
			}
		}
	}

	// 5. Extract the winning pairs
	var pairs []MatchedPair
	for rIdx, targetVotes := range votes {
		bestTargetIdx := -1
		maxVotes := 0

		for tIdx, v := range targetVotes {
			if v > maxVotes {
				maxVotes = v
				bestTargetIdx = tIdx
			}
		}

		// Require at least 2 votes to prevent random coincidence
		if maxVotes > 1 && bestTargetIdx != -1 {
			pairs = append(pairs, MatchedPair{
				RefX:    refStars[rIdx].X,
				RefY:    refStars[rIdx].Y,
				TargetX: targetStars[bestTargetIdx].X,
				TargetY: targetStars[bestTargetIdx].Y,
				Votes:   maxVotes,
			})
		}
	}

	return pairs
}

// generateTriangles creates invariant triangles from a list of stars.
func generateTriangles(stars []Star) []triangle {
	var triangles []triangle
	n := len(stars)

	for i := 0; i < n-2; i++ {
		for j := i + 1; j < n-1; j++ {
			for k := j + 1; k < n; k++ {
				// Calculate lengths squared to avoid expensive Sqrt where possible,
				// but we need actual lengths for the ratio.
				d12 := distance(stars[i], stars[j]) // Side opposite to k
				d23 := distance(stars[j], stars[k]) // Side opposite to i
				d13 := distance(stars[i], stars[k]) // Side opposite to j

				// Create an array of sides with their opposite vertex index
				sides := []struct {
					length   float64
					opposite int
				}{
					{d12, k},
					{d23, i},
					{d13, j},
				}

				// Sort sides a < b < c
				sort.Slice(sides, func(a, b int) bool {
					return sides[a].length < sides[b].length
				})

				a := sides[0].length
				b := sides[1].length
				c := sides[2].length

				// Avoid colinear points or extremely skinny triangles
				if c == 0 || (a/c) < 0.05 {
					continue
				}

				triangles = append(triangles, triangle{
					V1:     sides[0].opposite, // Vertex opposite smallest side
					V2:     sides[1].opposite, // Vertex opposite middle side
					V3:     sides[2].opposite, // Vertex opposite longest side
					RatioX: a / c,
					RatioY: b / c,
				})
			}
		}
	}
	return triangles
}

func distance(s1, s2 Star) float64 {
	dx := s2.X - s1.X
	dy := s2.Y - s1.Y
	return math.Sqrt(dx*dx + dy*dy)
}
