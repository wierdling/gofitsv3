package processing

import (
	"math"
	"sort"

	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

// MagicPreset selects how the stretch levels are estimated. Galaxy uses a
// slightly raised black-point percentile for a darker sky; presets otherwise
// differ in their white-sample population and percentile.
type MagicPreset int

const (
	// MagicBalanced is the general-purpose default used when no target type is
	// specified. White comes from a high percentile of all non-star pixels.
	MagicBalanced MagicPreset = iota
	// MagicNebula protects diffuse nebulosity: white comes from the bright end
	// of non-star pixels that are above the diffuse-content threshold, so stars
	// (which are masked) are allowed to clip while nebulosity is not.
	MagicNebula
	// MagicGalaxy protects bright cores: white comes from a very high percentile
	// of non-star pixels so the galaxy core is not blown out.
	MagicGalaxy
)

func (p MagicPreset) String() string {
	switch p {
	case MagicNebula:
		return "Nebula"
	case MagicGalaxy:
		return "Galaxy"
	default:
		return "Balanced"
	}
}

// ParseMagicPreset maps a UI label to a preset, defaulting to Balanced.
func ParseMagicPreset(label string) MagicPreset {
	switch label {
	case "Nebula":
		return MagicNebula
	case "Galaxy":
		return MagicGalaxy
	default:
		return MagicBalanced
	}
}

// Tunable constants for the Magic auto-level estimator. These are deliberately
// kept here as named constants so they are easy to find and adjust.
const (
	// targetBlackClipPercent is the fraction of valid pixels (in percent) that
	// are allowed to clip to black. The black point is set to this low
	// percentile so a tiny amount of low-end clipping just begins, rather than
	// using the true minimum (which is driven by cold pixels and noise).
	targetBlackClipPercent = 0.10

	// nebulaDiffuseSigmaThreshold defines diffuse content: pixels above
	// background + this*sigma (and not part of a star) are treated as real
	// extended signal for the white estimate.
	nebulaDiffuseSigmaThreshold = 2.5

	// starSigmaThreshold is how far above background a local peak must rise to
	// be treated as a compact bright source (star).
	starSigmaThreshold = 6.0

	// starMaskDilationPixels grows each detected star peak by this radius so the
	// star's halo is excluded from the white estimate too.
	starMaskDilationPixels = 4

	// starCompactnessRadius is the distance (px) at which a real star's profile
	// must have fallen off. It distinguishes compact stars from extended bright
	// nebulosity, whose plateau stays high out to this radius.
	starCompactnessRadius = 4

	// starCompactnessDropFactor: a local peak counts as a compact star only if
	// the brightness at starCompactnessRadius has dropped below this fraction of
	// the peak's excess over background. Extended nebulosity fails this test.
	starCompactnessDropFactor = 0.5

	// White percentiles per preset (percent of the chosen sample population).
	// The nebula percentile is deliberately lower than galaxy/balanced: it puts
	// the white point near the top of the bulk nebulosity (so the nebula stretches
	// up toward white and just begins to clip) rather than at the brightest knots
	// or residual star-halo pixels, which would leave the nebula under-stretched.
	nebulaWhitePercentile   = 98.5
	galaxyWhitePercentile   = 99.99
	balancedWhitePercentile = 99.9

	// minimumWhiteSamplePercent is the minimum size (percent of valid pixels)
	// the preferred white-sample population must have before it is trusted;
	// otherwise the estimator falls back to a broader population.
	minimumWhiteSamplePercent = 0.5

	// whiteSafetyMargin backs the white point off slightly by stretching the
	// black->white distance, so the estimated bright content does not sit right
	// at the clip point. Used by the galaxy/balanced presets.
	whiteSafetyMargin = 1.05

	// galaxyBlackClipPercent raises the galaxy floor modestly above the generic
	// 0.1th-percentile floor. This clips only a small robust noise tail while
	// making the sky render darker; other presets retain their existing floor.
	galaxyBlackClipPercent = 1.0

	// nebulaWhiteSafetyMargin is intentionally <= 1: for nebulae we want the
	// bright nebulosity to reach white and just begin to clip, so we do not push
	// the white point up above the estimated bright content the way the other
	// presets do.
	nebulaWhiteSafetyMargin = 1.0

	// magicMaxSamples caps how many valid pixels are sampled for the percentile
	// and noise statistics, keeping the estimator fast on large mosaics. Star
	// detection still runs over the full-resolution grid.
	magicMaxSamples = 2_000_000
)

// MagicLevelsResult reports the chosen levels plus diagnostics so the caller can
// display or debug the outcome.
type MagicLevelsResult struct {
	Black              float64
	White              float64
	ClipLowPercent     float64 // percent of valid pixels below Black
	ClipHighPercent    float64 // percent of valid pixels above White
	StarsExcluded      bool    // whether any star pixels were masked
	StarPixelPercent   float64 // percent of valid pixels masked as stars
	WhiteSampleCount   int     // number of pixels used for the white estimate
	WhiteSamplePercent float64 // those as a percent of valid pixels
	WhiteSampleSource  string  // "diffuse", "non-star", or "all-valid"
	Preset             MagicPreset
	Background         float64 // estimated sky level
	Sigma              float64 // robust noise sigma
	ValidPixels        int
}

// ApplyMagicLevels computes Magic black/white levels for img and writes them to
// img.Background/img.Peak (the stretch input levels) and img.Black/img.White
// (the clip-overlay markers). It does not change the selected stretch mode or
// any stretch-shaping parameter. The returned result carries diagnostics.
func ApplyMagicLevels(img *models.LoadedImage, preset MagicPreset) MagicLevelsResult {
	if img == nil {
		return MagicLevelsResult{White: 1, Preset: preset}
	}
	res := MagicLevels(img.HDU.Data.Pixels, img.HDU.Data.Width, img.HDU.Data.Height, nil, preset)
	img.Background = res.Black
	img.Peak = res.White
	img.Black = res.Black
	img.White = res.White
	return res
}

// ApplyMagicLevelsAndMTF applies the Combine Magic operation as one coherent
// stretch. The MTF is derived from the same robust background and sigma used
// to choose Magic's levels, rather than estimating them again from the raw
// image. This keeps the auto-STF 0.25 sky target stable for heavily padded or
// unevenly sampled images.
func ApplyMagicLevelsAndMTF(img *models.LoadedImage, preset MagicPreset) MagicLevelsResult {
	res := ApplyMagicLevels(img, preset)
	if img == nil || res.ValidPixels == 0 {
		return res
	}

	black, white := res.Black, res.White
	if !finite(black) {
		black = 0
	}
	if !finite(white) || white <= black {
		white = black + 1
	}
	target := res.Background + 2.8*res.Sigma
	if !finite(target) {
		target = res.Background
	}
	if !finite(target) {
		target = black
	}
	xRef := (target - black) / (white - black)
	if !finite(xRef) {
		xRef = 0.25
	}
	if xRef < 1e-5 {
		xRef = 1e-5
	}
	if xRef > 1 {
		xRef = 1
	}
	m := 3 * xRef / (2*xRef + 1)
	if !finite(m) || m < 0.001 {
		m = 0.001
	}
	if m > 0.5 {
		m = 0.5
	}
	img.MTFMidtone = m
	img.Mode = stretch.MTF
	return res
}

// MagicLevels estimates black and white stretch levels from linear image data.
//
// valid is an optional per-pixel validity mask (len == len(pixels)); when nil
// every finite, non-padding pixel is considered. NaN, Inf and exact-zero
// drizzle padding are always ignored.
func MagicLevels(pixels []float32, width, height int, valid []bool, preset MagicPreset) MagicLevelsResult {
	res := MagicLevelsResult{White: 1, Preset: preset, WhiteSampleSource: "all-valid"}
	n := len(pixels)
	if n == 0 {
		return res
	}
	if len(valid) != n {
		valid = nil
	}

	isValid := func(i int) bool {
		if valid != nil && !valid[i] {
			return false
		}
		v := pixels[i]
		if v == 0 { // drizzle padding / empty border
			return false
		}
		f := float64(v)
		return !math.IsNaN(f) && !math.IsInf(f, 0)
	}

	// Sampled valid values for robust statistics and percentiles.
	stride := 1
	if n > magicMaxSamples {
		stride = (n + magicMaxSamples - 1) / magicMaxSamples
	}
	sample := make([]float64, 0, magicMaxSamples)
	for i := 0; i < n; i += stride {
		if isValid(i) {
			sample = append(sample, float64(pixels[i]))
		}
	}
	if len(sample) == 0 {
		return res
	}
	sort.Float64s(sample)
	res.ValidPixels = len(sample)

	// Robust sky and noise from a sigma-clipped sample (around the median, so
	// stars and nebula peaks do not inflate the noise estimate).
	bg, sigma := robustSkyAndSigma(sample)
	res.Background = bg
	res.Sigma = sigma

	// Black point: a low percentile of valid pixels so only a tiny fraction
	// clips low, rather than the true minimum.
	black := percentileSorted(sample, targetBlackClipPercent)
	if preset == MagicGalaxy {
		black = percentileSorted(sample, galaxyBlackClipPercent)
	}
	res.Black = black

	// Detect compact bright sources (stars) on the full-resolution grid.
	starMask, starPixels := detectStarMask(pixels, width, height, valid, bg, sigma)
	res.StarsExcluded = starPixels > 0

	// Build the white-sample populations from the (strided) valid pixels:
	//   diffuse  = non-star pixels above the diffuse-content threshold
	//   nonStar  = all non-star valid pixels
	diffuseThreshold := bg + nebulaDiffuseSigmaThreshold*sigma
	diffuse := make([]float64, 0, len(sample))
	nonStar := make([]float64, 0, len(sample))
	var starSampled int
	for i := 0; i < n; i += stride {
		if !isValid(i) {
			continue
		}
		if starMask != nil && starMask[i] {
			starSampled++
			continue
		}
		v := float64(pixels[i])
		nonStar = append(nonStar, v)
		if v >= diffuseThreshold {
			diffuse = append(diffuse, v)
		}
	}
	if len(sample) > 0 {
		res.StarPixelPercent = 100 * float64(starSampled) / float64(len(sample))
	}

	// Choose the white-sample population and percentile for the preset, with a
	// safe fallback chain when there are not enough pixels in the preferred set.
	minSamples := int(minimumWhiteSamplePercent / 100 * float64(len(sample)))
	if minSamples < 1 {
		minSamples = 1
	}
	pct := balancedWhitePercentile
	switch preset {
	case MagicNebula:
		pct = nebulaWhitePercentile
	case MagicGalaxy:
		pct = galaxyWhitePercentile
	}

	var pop []float64
	switch preset {
	case MagicGalaxy:
		// Galaxies: high percentile of all non-star pixels (preserve the core).
		if len(nonStar) >= minSamples {
			pop, res.WhiteSampleSource = nonStar, "non-star"
		}
	default:
		// Nebula and Balanced: prefer the bright end of diffuse content.
		if len(diffuse) >= minSamples {
			pop, res.WhiteSampleSource = diffuse, "diffuse"
		} else if len(nonStar) >= minSamples {
			pop, res.WhiteSampleSource = nonStar, "non-star"
		}
	}
	if pop == nil { // final fallback: all valid pixels
		pop, res.WhiteSampleSource = sample, "all-valid"
	}

	sort.Float64s(pop)
	estWhite := percentileSorted(pop, pct)
	res.WhiteSampleCount = len(pop)
	res.WhiteSamplePercent = 100 * float64(len(pop)) / float64(len(sample))

	// Back the white point off slightly so the brightest kept content does not
	// sit right at the clip point. Nebula uses a smaller margin so the bright
	// nebulosity actually reaches white and just begins to clip.
	margin := whiteSafetyMargin
	if preset == MagicNebula {
		margin = nebulaWhiteSafetyMargin
	}
	white := black + (estWhite-black)*margin
	if white <= black {
		// Degenerate distribution: fall back to the bright end of all pixels.
		white = percentileSorted(sample, 99.9)
	}
	if white <= black {
		white = black + 1
	}
	res.White = white

	// Clip diagnostics over the sampled valid pixels (sample is sorted).
	res.ClipLowPercent = 100 * float64(countBelow(sample, black)) / float64(len(sample))
	res.ClipHighPercent = 100 * float64(len(sample)-countAtMost(sample, white)) / float64(len(sample))

	return res
}

// robustSkyAndSigma returns a sigma-clipped median and a MAD-based sigma from a
// sorted sample of valid values.
func robustSkyAndSigma(sorted []float64) (float64, float64) {
	if len(sorted) == 0 {
		return 0, 1
	}
	work := sorted
	median := percentileSorted(work, 50.0)
	sigma := robustSigma(work, median)
	for iter := 0; iter < 3; iter++ {
		if sigma <= 0 {
			break
		}
		lo := median - 3*sigma
		hi := median + 3*sigma
		// work is sorted, so the in-range slice is contiguous.
		start := sort.SearchFloat64s(work, lo)
		end := sort.SearchFloat64s(work, hi)
		if end <= start {
			break
		}
		clipped := work[start:end]
		if len(clipped) == len(work) {
			break
		}
		work = clipped
		median = percentileSorted(work, 50.0)
		sigma = robustSigma(work, median)
	}
	return median, sigma
}

// detectStarMask flags compact bright sources. A pixel is a star peak when it is
// a local maximum that rises above background + starSigmaThreshold*sigma; each
// peak is then dilated by starMaskDilationPixels to cover its halo. Returns nil
// when there is nothing to mask. The mask is full resolution (len == w*h).
func detectStarMask(pixels []float32, width, height int, valid []bool, bg, sigma float64) ([]bool, int) {
	if width < 3 || height < 3 || width*height != len(pixels) || sigma <= 0 {
		return nil, 0
	}
	threshold := bg + starSigmaThreshold*sigma
	pixOK := func(i int) bool {
		if valid != nil && !valid[i] {
			return false
		}
		v := pixels[i]
		if v == 0 {
			return false
		}
		f := float64(v)
		return !math.IsNaN(f) && !math.IsInf(f, 0)
	}

	type pt struct{ x, y int }
	var peaks []pt
	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			i := y*width + x
			if !pixOK(i) {
				continue
			}
			v := float64(pixels[i])
			if v <= threshold {
				continue
			}
			isPeak := true
			for dy := -1; dy <= 1 && isPeak; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					if !pixOK(i + dy*width + dx) {
						continue
					}
					if float64(pixels[i+dy*width+dx]) > v {
						isPeak = false
						break
					}
				}
			}
			if isPeak && isCompactPeak(pixels, width, height, x, y, v, bg, valid) {
				peaks = append(peaks, pt{x, y})
			}
		}
	}
	if len(peaks) == 0 {
		return nil, 0
	}

	mask := make([]bool, len(pixels))
	r := starMaskDilationPixels
	count := 0
	for _, p := range peaks {
		y0, y1 := p.y-r, p.y+r
		x0, x1 := p.x-r, p.x+r
		if y0 < 0 {
			y0 = 0
		}
		if x0 < 0 {
			x0 = 0
		}
		if y1 >= height {
			y1 = height - 1
		}
		if x1 >= width {
			x1 = width - 1
		}
		for y := y0; y <= y1; y++ {
			row := y * width
			for x := x0; x <= x1; x++ {
				i := row + x
				if !mask[i] {
					mask[i] = true
					count++
				}
			}
		}
	}
	return mask, count
}

// isCompactPeak reports whether the peak at (x,y) with value v falls off toward
// background within starCompactnessRadius. It samples 8 points on a ring at that
// radius; a compact star drops sharply, extended nebulosity stays bright.
func isCompactPeak(pixels []float32, width, height, x, y int, v, bg float64, valid []bool) bool {
	r := starCompactnessRadius
	offsets := [8][2]int{
		{r, 0}, {-r, 0}, {0, r}, {0, -r},
		{r, r}, {r, -r}, {-r, r}, {-r, -r},
	}
	var sum float64
	var cnt int
	for _, o := range offsets {
		nx, ny := x+o[0], y+o[1]
		if nx < 0 || nx >= width || ny < 0 || ny >= height {
			continue
		}
		rv := float64(pixels[ny*width+nx])
		ri := ny*width + nx
		if (valid != nil && !valid[ri]) || pixels[ri] == 0 || math.IsNaN(rv) || math.IsInf(rv, 0) {
			continue
		}
		sum += rv
		cnt++
	}
	if cnt < 4 {
		return false // too close to the border to judge compactness
	}
	ringMean := sum / float64(cnt)
	excess := v - bg
	if excess <= 0 {
		return false
	}
	return (ringMean - bg) < starCompactnessDropFactor*excess
}

// countBelow returns the number of values in a sorted slice strictly below v.
func countBelow(sorted []float64, v float64) int {
	return sort.SearchFloat64s(sorted, v)
}

func countAtMost(sorted []float64, v float64) int {
	return sort.Search(len(sorted), func(i int) bool { return sorted[i] > v })
}
