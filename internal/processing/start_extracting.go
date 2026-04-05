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
