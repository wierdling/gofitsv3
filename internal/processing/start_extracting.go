package processing

import (
	"math"
	"sort"
)

type Star struct {
	X    float64
	Y    float64
	Flux float64
	Area int
}

func ExtractStars(pixels []float32, width, height int, thresholdSigma float64, minArea int) []Star {
	median, sigma := EstimateBackground(pixels)
	threshold := median + (thresholdSigma * sigma)

	visited := make([]bool, len(pixels))
	var stars []Star
	dirs := [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			idx := y*width + x
			pix := float64(pixels[idx])
			if visited[idx] || pix < threshold || math.IsNaN(pix) {
				visited[idx] = true
				continue
			}

			q := [][2]int{{x, y}}
			visited[idx] = true
			var blob [][2]int
			var fluxSum float64

			for len(q) > 0 {
				curr := q[0]
				q = q[1:]
				blob = append(blob, curr)
				cx, cy := curr[0], curr[1]
				cIdx := cy*width + cx
				netFlux := float64(pixels[cIdx]) - median
				if netFlux < 0 {
					netFlux = 0
				}
				fluxSum += netFlux
				for _, d := range dirs {
					nx, ny := cx+d[0], cy+d[1]
					if nx >= 0 && nx < width && ny >= 0 && ny < height {
						nIdx := ny*width + nx
						if !visited[nIdx] && float64(pixels[nIdx]) >= threshold {
							visited[nIdx] = true
							q = append(q, [2]int{nx, ny})
						}
					}
				}
			}

			if len(blob) < minArea {
				continue
			}

			var centerX, centerY float64
			for _, p := range blob {
				bx, by := p[0], p[1]
				bIdx := by*width + bx
				netFlux := float64(pixels[bIdx]) - median
				if netFlux < 0 {
					netFlux = 0
				}
				centerX += float64(bx) * netFlux
				centerY += float64(by) * netFlux
			}

			if fluxSum > 0 {
				centerX /= fluxSum
				centerY /= fluxSum
				stars = append(stars, Star{X: centerX, Y: centerY, Flux: fluxSum, Area: len(blob)})
			}
		}
	}

	sort.Slice(stars, func(i, j int) bool { return stars[i].Flux > stars[j].Flux })
	return stars
}

func EstimateBackground(pixels []float32) (float64, float64) {
	step := len(pixels) / 100000
	if step < 1 {
		step = 1
	}

	var sample []float64
	for i := 0; i < len(pixels); i += step {
		v := float64(pixels[i])
		if !math.IsNaN(v) {
			sample = append(sample, v)
		}
	}
	if len(sample) == 0 {
		return 0, 1
	}

	sort.Float64s(sample)
	median := sample[len(sample)/2]
	absoluteDeviations := make([]float64, 0, len(sample))
	for _, v := range sample {
		absoluteDeviations = append(absoluteDeviations, math.Abs(v-median))
	}
	sort.Float64s(absoluteDeviations)
	mad := absoluteDeviations[len(absoluteDeviations)/2]
	sigma := mad * 1.4826
	if sigma <= 0 {
		sigma = 1
	}
	return median, sigma
}

// CentroidNear returns the flux-weighted centroid of the star nearest to (x, y).
// It searches for the brightest pixel within searchRadius, then computes a
// flux-weighted centroid in a 7-pixel window around that peak.
// Returns the refined position and true on success; (x, y, false) if nothing
// bright enough is found.
func CentroidNear(pixels []float32, width, height int, x, y float64, searchRadius int) (float64, float64, bool) {
	cx := int(math.Round(x))
	cy := int(math.Round(y))

	// Clamp search box.
	sx0 := cx - searchRadius
	sx1 := cx + searchRadius
	sy0 := cy - searchRadius
	sy1 := cy + searchRadius
	if sx0 < 0 {
		sx0 = 0
	}
	if sx1 >= width {
		sx1 = width - 1
	}
	if sy0 < 0 {
		sy0 = 0
	}
	if sy1 >= height {
		sy1 = height - 1
	}

	// Find the peak pixel in the search box.
	peakX, peakY := cx, cy
	peakVal := math.Inf(-1)
	for py := sy0; py <= sy1; py++ {
		for px := sx0; px <= sx1; px++ {
			v := float64(pixels[py*width+px])
			if math.IsNaN(v) {
				continue
			}
			if v > peakVal {
				peakVal = v
				peakX, peakY = px, py
			}
		}
	}
	if math.IsInf(peakVal, -1) {
		return x, y, false
	}

	// Compute local background as the median of pixels in the centroid window.
	const centRadius = 7
	c0x := peakX - centRadius
	c1x := peakX + centRadius
	c0y := peakY - centRadius
	c1y := peakY + centRadius
	if c0x < 0 {
		c0x = 0
	}
	if c1x >= width {
		c1x = width - 1
	}
	if c0y < 0 {
		c0y = 0
	}
	if c1y >= height {
		c1y = height - 1
	}

	var localSample []float64
	for py := c0y; py <= c1y; py++ {
		for px := c0x; px <= c1x; px++ {
			v := float64(pixels[py*width+px])
			if !math.IsNaN(v) {
				localSample = append(localSample, v)
			}
		}
	}
	var bg float64
	if len(localSample) > 0 {
		sorted := make([]float64, len(localSample))
		copy(sorted, localSample)
		sort.Float64s(sorted)
		bg = sorted[len(sorted)/4] // lower quartile as background estimate
	}

	// Flux-weighted centroid around peak.
	var sumX, sumY, sumF float64
	for py := c0y; py <= c1y; py++ {
		for px := c0x; px <= c1x; px++ {
			v := float64(pixels[py*width+px]) - bg
			if v <= 0 || math.IsNaN(v) {
				continue
			}
			sumX += float64(px) * v
			sumY += float64(py) * v
			sumF += v
		}
	}
	if sumF <= 0 {
		return x, y, false
	}
	return sumX / sumF, sumY / sumF, true
}
