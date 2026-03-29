package processing

import (
	"math"
	"sort"
)

// Star represents a calculated sub-pixel star location.
type Star struct {
	X    float64
	Y    float64
	Flux float64
	Area int // Number of pixels in the blob
}

// ExtractStars identifies star centroids in a linear float64 image array.
// thresholdSigma: multiplier for background noise (typically 3.0 to 5.0).
// minArea: minimum pixel count to filter cosmic rays (typically 3 to 5).
func ExtractStars(pixels []float64, width, height int, thresholdSigma float64, minArea int) []Star {
	median, sigma := EstimateBackground(pixels)
	threshold := median + (thresholdSigma * sigma)

	visited := make([]bool, len(pixels))
	var stars []Star

	// 8-way neighborhood offsets for blob connected-components
	dirs := [][2]int{
		{-1, -1}, {0, -1}, {1, -1},
		{-1, 0}, {1, 0},
		{-1, 1}, {0, 1}, {1, 1},
	}

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			idx := y*width + x

			// Skip if already evaluated or below background threshold
			if visited[idx] || pixels[idx] < threshold || math.IsNaN(pixels[idx]) {
				visited[idx] = true
				continue
			}

			// Begin Breadth-First Search (BFS) to map the contiguous blob
			var q [][2]int
			q = append(q, [2]int{x, y})
			visited[idx] = true

			var blob [][2]int
			var fluxSum float64

			for len(q) > 0 {
				curr := q[0]
				q = q[1:]
				blob = append(blob, curr)

				cx, cy := curr[0], curr[1]
				cIdx := cy*width + cx

				// Calculate net intensity (I) above the background
				netFlux := pixels[cIdx] - median
				if netFlux < 0 {
					netFlux = 0
				}
				fluxSum += netFlux

				// Evaluate surrounding 8 pixels
				for _, d := range dirs {
					nx, ny := cx+d[0], cy+d[1]
					if nx >= 0 && nx < width && ny >= 0 && ny < height {
						nIdx := ny*width + nx
						if !visited[nIdx] && pixels[nIdx] >= threshold {
							visited[nIdx] = true
							q = append(q, [2]int{nx, ny})
						}
					}
				}
			}

			// Reject cosmic rays and hot pixels (blobs that are too small)
			if len(blob) < minArea {
				continue
			}

			// Calculate Flux-Weighted Centroid
			var centerX, centerY float64
			for _, p := range blob {
				bx, by := p[0], p[1]
				bIdx := by*width + bx

				netFlux := pixels[bIdx] - median
				if netFlux < 0 {
					netFlux = 0
				}

				// Sum(x * I) and Sum(y * I)
				centerX += float64(bx) * netFlux
				centerY += float64(by) * netFlux
			}

			if fluxSum > 0 {
				centerX /= fluxSum
				centerY /= fluxSum
				stars = append(stars, Star{
					X:    centerX,
					Y:    centerY,
					Flux: fluxSum,
					Area: len(blob),
				})
			}
		}
	}

	// Sort stars by flux descending, ensuring the brightest are matched first
	sort.Slice(stars, func(i, j int) bool {
		return stars[i].Flux > stars[j].Flux
	})

	return stars
}

// EstimateBackground calculates the median and robust standard deviation.
func EstimateBackground(pixels []float64) (float64, float64) {
	// Sample approximately 100,000 pixels to prevent massive memory allocation
	step := len(pixels) / 100000
	if step < 1 {
		step = 1
	}

	var sample []float64
	for i := 0; i < len(pixels); i += step {
		if !math.IsNaN(pixels[i]) {
			sample = append(sample, pixels[i])
		}
	}

	if len(sample) == 0 {
		return 0, 1
	}

	sort.Float64s(sample)
	median := sample[len(sample)/2]

	// Calculate Median Absolute Deviation (MAD)
	var absoluteDeviations []float64
	for _, v := range sample {
		absoluteDeviations = append(absoluteDeviations, math.Abs(v-median))
	}
	sort.Float64s(absoluteDeviations)
	mad := absoluteDeviations[len(absoluteDeviations)/2]

	// 1.4826 converts MAD to standard deviation for a normal distribution
	sigma := mad * 1.4826
	if sigma <= 0 {
		sigma = 1
	}

	return median, sigma
}
