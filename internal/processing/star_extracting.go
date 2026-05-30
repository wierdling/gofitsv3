package processing

import (
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"

	"gofitsv3/internal/debuglog"
)

type Star struct {
	X    float64
	Y    float64
	Flux float64
	Peak float64 // peak net flux above background in a single pixel
	Area int
}

// ExtractAndLimitStars extracts stars and caps the result at maxStars brightest.
// Use this instead of ExtractStars when you want to reuse the catalog across multiple calls.
func ExtractAndLimitStars(pixels []float32, width, height int, thresholdSigma float64, minArea, maxStars int) []Star {
	stars := ExtractStars(pixels, width, height, thresholdSigma, minArea)
	if maxStars > 0 && len(stars) > maxStars {
		debuglog.Log(fmt.Sprintf("ExtractAndLimitStars: %d sources found, capping at %d", len(stars), maxStars))
		stars = stars[:maxStars]
	} else {
		debuglog.Log(fmt.Sprintf("ExtractAndLimitStars: %d sources found", len(stars)))
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
			var smoothedAtOrigPeak float64
			minOrigInBlob := math.Inf(1)

			for len(q) > 0 {
				curr := q[0]
				q = q[1:]
				blob = append(blob, curr)
				cx, cy := curr[0], curr[1]
				cIdx := cy*width + cx
				origVal := float64(pixels[cIdx])
				if origVal < minOrigInBlob {
					minOrigInBlob = origVal
				}
				netFlux := origVal - median
				if netFlux < 0 {
					netFlux = 0
				}
				if netFlux > peakNetFlux {
					peakNetFlux = netFlux
					smoothedAtOrigPeak = float64(smoothed[cIdx])
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

			// Enforce a strict minimum area to reject 1-4 pixel cosmic ray hits,
			// even if a caller requested a smaller minArea.
			if len(blob) < minArea || len(blob) < 5 {
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
			// Reject blobs whose bounding box is more than 2:1 elongated.
			// Cosmic rays can be streaks, but real stars are round.
			if shorter == 0 || longer/shorter > 2 {
				continue
			}

			// Reject blobs too large to be a stellar PSF.
			// Stars occupy 10–200 pixels at any realistic FWHM; extended nebula
			// knots, galaxies, and partially-merged groups are much larger and
			// produce unreliable centroids. Saturated stars are also excluded —
			// they bleed and their centroids are biased.
			if len(blob) > 400 {
				continue
			}

			// Reject compact spikes where 1–3 pixels hold most of the flux.
			// Single-pixel CRs produce ratio ≈ 0.9; 2-pixel CRs ≈ 0.5.
			// A compact stellar PSF (σ ≈ 0.85 px, HST-like) spreads flux across
			// ~25 pixels in the smoothed blob, giving ratio ≈ 0.20–0.25.
			// Threshold at 0.35 catches thick cosmic rays while keeping all realistic stellar PSFs.
			if fluxSum > 0 && peakNetFlux/fluxSum > 0.35 {
				continue
			}

			// Use the minimum original pixel in the blob as a local background
			// estimate. When a CR or hot pixel sits on bright nebula emission,
			// the global median significantly underestimates the local sky level,
			// which inflates the smoothed/original ratio and defeats the CR check.
			// The blob minimum (the faintest boundary pixel) approximates the local
			// sky level without requiring an expensive spatial background map.
			// Clamp to median: if noise drives one blob pixel below the global
			// median, using that as "local background" would inflate localOrigNet
			// and weaken both filters. median is always a safe lower bound.
			localBg := minOrigInBlob
			if localBg < median {
				localBg = median
			}
			localPeakNet := smoothedAtOrigPeak - localBg   // smoothed excess above local sky
			localOrigNet := peakNetFlux + median - localBg // original excess above local sky

			// Reject cosmic rays via smoothed/original peak ratio relative to local sky.
			// A sub-pixel CR is attenuated to ~14% of its local excess by the σ=1.5
			// Gaussian kernel.  A stellar PSF with σ ≥ 1 px retains ≥ 31%.
			// Computing both quantities relative to localBg removes the nebula-elevation
			// bias that caused CRs on bright backgrounds to pass the global-median version.
			// Increased to 0.25 to aggressively filter thicker cosmic rays.
			if localOrigNet > 0 && localPeakNet/localOrigNet < 0.25 {
				continue
			}

			// Require the source to be significantly above its local background.
			// Sources that are only modest bumps on bright nebula (localOrigNet < 6σ)
			// are not reliable alignment references; real stars must be well above
			// both the local emission and the global noise floor.
			if localOrigNet < 6.0*sigma {
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
				stars = append(stars, Star{X: centerX, Y: centerY, Flux: fluxSum, Peak: peakNetFlux, Area: len(blob)})
			}
		}
	}

	// Sort by peak pixel brightness, not integrated flux.
	// In nebula fields, extended emission knots have high integrated flux but
	// low peak brightness. Real stars are compact and bright per pixel, so
	// peak-sorted lists contain mostly actual stars rather than nebula features.
	sort.Slice(stars, func(i, j int) bool { return stars[i].Peak > stars[j].Peak })
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

	// Iterative sigma-clipping to remove bright sources (stars, nebula peaks).
	// We clip around the mean to converge on the background population.
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
		clipSigma := math.Sqrt(varSum / float64(len(sample)))
		if clipSigma <= 0 {
			break
		}
		lo := mean - 3*clipSigma
		hi := mean + 3*clipSigma
		clipped := sample[:0]
		for _, v := range sample {
			if v >= lo && v <= hi {
				clipped = append(clipped, v)
			}
		}
		if len(clipped) == len(sample) {
			break
		}
		sample = clipped
	}

	// Return median and MAD-based sigma rather than mean and std.
	// Median is robust to residual nebula emission; MAD gives a noise estimate
	// that does not inflate when there is extended background structure.
	// For a pure Gaussian background, MAD-sigma ≈ std, so flat-sky images are unaffected.
	sort.Float64s(sample)
	median := sample[len(sample)/2]
	devs := make([]float64, len(sample))
	for i, v := range sample {
		devs[i] = math.Abs(v - median)
	}
	sort.Float64s(devs)
	mad := devs[len(devs)/2]
	sigma := 1.4826 * mad
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

// gaussianConvolve applies a separable Gaussian blur (two 1-D passes).
// NaN pixels are excluded from kernel sums so chip gaps don't bleed.
// Both passes are parallelized across available CPUs.
// blurBufPool recycles the intermediate buffer used by the separable Gaussian
// convolution. Star extraction calls gaussianConvolve repeatedly (and
// concurrently during alignment), so pooling the scratch buffer avoids a
// full-frame allocation per call. The returned output buffer is caller-owned
// and is never pooled.
var blurBufPool sync.Pool

func getBlurBuf(n int) []float32 {
	if b, ok := blurBufPool.Get().([]float32); ok && cap(b) >= n {
		b = b[:n]
		clear(b) // convolution only writes covered pixels; clear stale values
		return b
	}
	return make([]float32, n)
}

func putBlurBuf(b []float32) {
	if b != nil {
		blurBufPool.Put(b[:0])
	}
}

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
	tmp := getBlurBuf(width * height)
	defer putBlurBuf(tmp)
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
