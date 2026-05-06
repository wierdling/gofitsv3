package processing

import (
	"math"
	"runtime"
	"sort"
	"sync"
)

type Star struct {
	X    float64
	Y    float64
	Flux float64
	Area int
}

// ExtractAndLimitStars extracts stars and caps the result at maxStars brightest.
// Use this instead of ExtractStars when you want to reuse the catalog across multiple calls.
func ExtractAndLimitStars(pixels []float32, width, height int, thresholdSigma float64, minArea, maxStars int) []Star {
	stars := ExtractStars(pixels, width, height, thresholdSigma, minArea)
	if maxStars > 0 && len(stars) > maxStars {
		stars = stars[:maxStars]
	}
	return stars
}

func ExtractStars(pixels []float32, width, height int, thresholdSigma float64, minArea int) []Star {
	median, sigma := EstimateBackground(pixels)
	threshold := median + (thresholdSigma * sigma)

	// Smooth for detection only; centroids are computed on the original pixels.
	// This matches TweakReg's Gaussian pre-filter approach: noise peaks that survive
	// thresholding on the raw image are suppressed on the smoothed image.
	smoothed := gaussianConvolve(pixels, width, height, 1.5)

	visited := make([]bool, len(pixels))
	var stars []Star
	dirs := [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			idx := y*width + x
			pix := float64(smoothed[idx])
			if visited[idx] || pix < threshold || math.IsNaN(pix) {
				visited[idx] = true
				continue
			}

			q := [][2]int{{x, y}}
			visited[idx] = true
			var blob [][2]int
			var fluxSum, peakNetFlux float64

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
				if netFlux > peakNetFlux {
					peakNetFlux = netFlux
				}
				fluxSum += netFlux
				for _, d := range dirs {
					nx, ny := cx+d[0], cy+d[1]
					if nx >= 0 && nx < width && ny >= 0 && ny < height {
						nIdx := ny*width + nx
						if !visited[nIdx] && float64(smoothed[nIdx]) >= threshold {
							visited[nIdx] = true
							q = append(q, [2]int{nx, ny})
						}
					}
				}
			}

			if len(blob) < minArea {
				continue
			}

			// Compute bounding box to reject highly elongated sources
			// (CCD bleed columns, cosmic-ray streaks, diffraction spikes).
			// A real star PSF is roughly circular; bleeds have extreme aspect ratios.
			minBX, minBY := blob[0][0], blob[0][1]
			maxBX, maxBY := minBX, minBY
			for _, p := range blob {
				if p[0] < minBX {
					minBX = p[0]
				}
				if p[0] > maxBX {
					maxBX = p[0]
				}
				if p[1] < minBY {
					minBY = p[1]
				}
				if p[1] > maxBY {
					maxBY = p[1]
				}
			}
			bboxW := maxBX - minBX + 1
			bboxH := maxBY - minBY + 1
			longer := bboxW
			if bboxH > longer {
				longer = bboxH
			}
			shorter := bboxW
			if bboxH < shorter {
				shorter = bboxH
			}
			// Reject blobs whose bounding box is more than 4:1 elongated.
			if shorter == 0 || longer/shorter > 4 {
				continue
			}

			// Reject compact spikes where a single pixel holds most of the flux.
			// Cosmic rays deposit nearly all energy in 1-2 pixels; a real stellar
			// PSF spreads flux across multiple pixels after the Gaussian pre-filter.
			if fluxSum > 0 && peakNetFlux/fluxSum > 0.75 {
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

	// Iterative 3-sigma clipping (3 passes), matching TweakReg sky estimation.
	for range 3 {
		var sum float64
		for _, v := range sample {
			sum += v
		}
		mean := sum / float64(len(sample))
		var varSum float64
		for _, v := range sample {
			d := v - mean
			varSum += d * d
		}
		sigma := math.Sqrt(varSum / float64(len(sample)))
		if sigma <= 0 {
			return mean, 1
		}
		lo := mean - 3*sigma
		hi := mean + 3*sigma
		clipped := sample[:0]
		for _, v := range sample {
			if v >= lo && v <= hi {
				clipped = append(clipped, v)
			}
		}
		if len(clipped) == len(sample) {
			return mean, sigma
		}
		sample = clipped
	}

	var sum float64
	for _, v := range sample {
		sum += v
	}
	mean := sum / float64(len(sample))
	var varSum float64
	for _, v := range sample {
		d := v - mean
		varSum += d * d
	}
	sigma := math.Sqrt(varSum / float64(len(sample)))
	if sigma <= 0 {
		sigma = 1
	}
	return mean, sigma
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

// gaussianConvolve applies a separable Gaussian blur (two 1-D passes).
// NaN pixels are excluded from kernel sums so chip gaps don't bleed.
// Both passes are parallelized across available CPUs.
func gaussianConvolve(pixels []float32, width, height int, sigma float64) []float32 {
	radius := int(math.Ceil(3 * sigma))
	size := 2*radius + 1
	kernel := make([]float64, size)
	var ksum float64
	for i := range kernel {
		x := float64(i - radius)
		kernel[i] = math.Exp(-0.5 * x * x / (sigma * sigma))
		ksum += kernel[i]
	}
	for i := range kernel {
		kernel[i] /= ksum
	}

	nWorkers := runtime.NumCPU()

	// Horizontal pass: each row is independent.
	tmp := make([]float32, width*height)
	var wg sync.WaitGroup
	rowsPerWorker := (height + nWorkers - 1) / nWorkers
	for w := 0; w < nWorkers; w++ {
		y0 := w * rowsPerWorker
		y1 := y0 + rowsPerWorker
		if y1 > height {
			y1 = height
		}
		if y0 >= height {
			break
		}
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for y := y0; y < y1; y++ {
				for x := 0; x < width; x++ {
					var val, wsum float64
					for ki, w := range kernel {
						sx := x + ki - radius
						if sx < 0 || sx >= width {
							continue
						}
						v := float64(pixels[y*width+sx])
						if math.IsNaN(v) {
							continue
						}
						val += w * v
						wsum += w
					}
					if wsum > 0 {
						tmp[y*width+x] = float32(val / wsum)
					}
				}
			}
		}(y0, y1)
	}
	wg.Wait()

	// Vertical pass: each row of the output is independent.
	out := make([]float32, width*height)
	for w := 0; w < nWorkers; w++ {
		y0 := w * rowsPerWorker
		y1 := y0 + rowsPerWorker
		if y1 > height {
			y1 = height
		}
		if y0 >= height {
			break
		}
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for y := y0; y < y1; y++ {
				for x := 0; x < width; x++ {
					var val, wsum float64
					for ki, w := range kernel {
						sy := y + ki - radius
						if sy < 0 || sy >= height {
							continue
						}
						v := float64(tmp[sy*width+x])
						if math.IsNaN(v) {
							continue
						}
						val += w * v
						wsum += w
					}
					if wsum > 0 {
						out[y*width+x] = float32(val / wsum)
					}
				}
			}
		}(y0, y1)
	}
	wg.Wait()
	return out
}
