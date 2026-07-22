package processing

import (
	"fmt"
	"math"
	"sort"

	"gofitsv3/internal/debuglog"
)

func RemoveCosmicRays(pixels []float32, width, height int, globalSigma float64, passes int, masterMask []bool) []float32 {
	debuglog.Log("RemoveCosmicRays: starting")
	defer debuglog.Log("RemoveCosmicRays: finished")
	if passes <= 0 {
		out := make([]float32, len(pixels))
		copy(out, pixels)
		return out
	}
	if width <= 0 || height <= 0 || len(pixels) == 0 {
		out := make([]float32, len(pixels))
		copy(out, pixels)
		return out
	}

	limitPixels := len(pixels)
	if masterMask != nil && len(masterMask) < limitPixels {
		limitPixels = len(masterMask)
	}
	effectiveHeight := height
	maxHeight := limitPixels / width
	if maxHeight <= 0 {
		out := make([]float32, len(pixels))
		copy(out, pixels)
		return out
	}
	if effectiveHeight > maxHeight {
		effectiveHeight = maxHeight
	}

	currentPixels := pixels
	for p := 0; p < passes; p++ {
		out := make([]float32, len(currentPixels))
		copy(out, currentPixels)

		cleaned := make([]bool, len(currentPixels))
		visitMarks := make([]uint32, len(currentPixels))
		var visitGen uint32
		laplacianThreshold := globalSigma * 10.0
		growThreshold := globalSigma * 2.0
		dirs := [8][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
		// Reused across seeds within the pass to avoid per-seed allocations.
		var candidates []int
		var queue []int

		for y := 2; y < effectiveHeight-2; y++ {
			for x := 2; x < width-2; x++ {
				idx := y*width + x
				if masterMask != nil && idx < len(masterMask) && masterMask[idx] {
					continue
				}
				if cleaned[idx] {
					continue
				}

				val := float64(currentPixels[idx])
				if math.IsNaN(val) || val <= 0 {
					continue
				}

				up := float64(currentPixels[(y-1)*width+x])
				down := float64(currentPixels[(y+1)*width+x])
				left := float64(currentPixels[y*width+(x-1)])
				right := float64(currentPixels[y*width+(x+1)])
				if math.IsNaN(up) {
					up = val
				}
				if math.IsNaN(down) {
					down = val
				}
				if math.IsNaN(left) {
					left = val
				}
				if math.IsNaN(right) {
					right = val
				}

				laplacian := (4.0 * val) - (up + down + left + right)
				if laplacian <= laplacianThreshold {
					continue
				}

				replacementVal := estimateWideBackground(currentPixels, width, effectiveHeight, x, y)
				candidates = candidates[:0]
				queue = queue[:0]
				queue = append(queue, idx)
				visitGen++
				if visitGen == 0 {
					for i := range visitMarks {
						visitMarks[i] = 0
					}
					visitGen = 1
				}
				visitMarks[idx] = visitGen
				minX, maxX, minY, maxY := x, x, y, y

				for head := 0; head < len(queue); head++ {
					currIdx := queue[head]
					candidates = append(candidates, currIdx)

					cx := currIdx % width
					cy := currIdx / width
					if cx < minX {
						minX = cx
					}
					if cx > maxX {
						maxX = cx
					}
					if cy < minY {
						minY = cy
					}
					if cy > maxY {
						maxY = cy
					}

					for _, d := range dirs {
						nx, ny := cx+d[0], cy+d[1]
						if nx < 0 || nx >= width || ny < 0 || ny >= effectiveHeight {
							continue
						}
						nIdx := ny*width + nx
						if cleaned[nIdx] || visitMarks[nIdx] == visitGen {
							continue
						}
						nVal := float64(currentPixels[nIdx])
						if math.IsNaN(nVal) {
							continue
						}
						if nVal > replacementVal+growThreshold {
							visitMarks[nIdx] = visitGen
							queue = append(queue, nIdx)
						}
					}
				}

				area := len(candidates)
				boxWidth := (maxX - minX) + 1
				boxHeight := (maxY - minY) + 1
				boxArea := boxWidth * boxHeight
				isStar := false
				if area > 60 {
					isStar = true
				} else if area > 12 {
					fillFactor := float64(area) / float64(boxArea)
					if fillFactor > 0.65 && boxWidth > 3 && boxHeight > 3 {
						isStar = true
					}
				}

				if isStar {
					for _, cIdx := range candidates {
						cleaned[cIdx] = true
					}
				} else {
					for _, cIdx := range candidates {
						out[cIdx] = float32(replacementVal)
						cleaned[cIdx] = true
					}
				}
			}
		}
		currentPixels = out
	}

	return currentPixels
}

func estimateWideBackground(pixels []float32, width, height int, cx, cy int) float64 {
	// The 11x11 window holds at most 121 samples, so keep it on the stack to
	// avoid a heap allocation on every seed pixel.
	var bg [121]float64
	n := 0
	for dy := -5; dy <= 5; dy++ {
		ny := cy + dy
		if ny < 0 || ny >= height {
			continue
		}
		base := ny * width
		for dx := -5; dx <= 5; dx++ {
			nx := cx + dx
			if nx < 0 || nx >= width {
				continue
			}
			val := float64(pixels[base+nx])
			if !math.IsNaN(val) {
				bg[n] = val
				n++
			}
		}
	}
	if n == 0 {
		return 0
	}
	sort.Float64s(bg[:n])
	return bg[n/2]
}

// FrameInfo holds the data needed for multi-frame cosmic ray detection.
type FrameInfo struct {
	Pixels      []float32
	Width       int
	Height      int
	SourceToRef AffineTransform
	RefToSource AffineTransform
	OffsetX     float64
	OffsetY     float64
	Sigma       float64
	// Noise, if non-nil and the same length as Pixels, is the per-pixel 1-sigma
	// uncertainty in the same units as Pixels (e.g. the calibration ERR plane).
	// It is used in place of the scalar Sigma so that the seed/grow thresholds
	// scale with the local signal (bright regions are noisier), which prevents a
	// flat global sigma from over-flagging real structure in bright areas. Where
	// a Noise sample is missing or non-positive, Sigma is used for that pixel.
	Noise []float32
	// MapFunc, if non-nil, maps a source pixel (x,y) to reference space.
	// BuildCRMasksFromModel uses it instead of SourceToRef for blotting,
	// allowing a full WCS mapper to be passed in for per-pixel accuracy.
	// The returned coordinates must already include any offset adjustment
	// (i.e. they are the same reference-space coords used during drizzle).
	MapFunc func(x, y float64) (float64, float64)
}

// DrizzleStyleCROptions controls the AstroDrizzle-like multi-frame CR detector.
type DrizzleStyleCROptions struct {
	// SeedSNR is the signal-to-noise ratio above which a pixel is seeded as a
	// cosmic-ray candidate. AstroDrizzle default is about 4.
	SeedSNR float64
	// DerivScale weights the 4-neighbor derivative contribution to the
	// per-pixel threshold. AstroDrizzle default is about 1.2.
	DerivScale float64
}

// BuildCRMasksFromModel flags cosmic rays by blotting a pre-built output-space
// model back to each input frame and comparing. This is steps 2-5 of the
// AstroDrizzle pipeline. Use this when the model has been built externally
// (e.g. from per-frame drizzled images) rather than via inverse blot.
func BuildCRMasksFromModel(frames []FrameInfo, model []float32, outW, outH int, minX, minY, scale float64, opts DrizzleStyleCROptions) [][]bool {
	debuglog.Log("BuildCRMasksFromModel: starting")
	defer debuglog.Log("BuildCRMasksFromModel: finished")
	n := len(frames)
	dirs4 := [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}}
	masks := make([][]bool, n)

	for fi := range frames {
		f := &frames[fi]
		debuglog.Log(fmt.Sprintf("BuildCRMasksFromModel: frame %d/%d (%dx%d)", fi+1, n, f.Width, f.Height))
		if f.Sigma <= 0 {
			masks[fi] = make([]bool, f.Width*f.Height)
			continue
		}

		npix := f.Width * f.Height
		mask := make([]bool, npix)

		// noiseAt returns the per-pixel 1-sigma used for thresholding. It prefers
		// the supplied Noise (ERR) plane so thresholds scale with local signal,
		// and falls back to the scalar Sigma where Noise is missing/invalid.
		noiseAt := func(idx int) float64 {
			if f.Noise != nil && idx < len(f.Noise) {
				if e := float64(f.Noise[idx]); e > 0 && !math.IsNaN(e) && !math.IsInf(e, 0) {
					return e
				}
			}
			return f.Sigma
		}

		// Blot the model back to input frame pixel space.
		// Use MapFunc when available (full WCS accuracy); fall back to the
		// affine SourceToRef approximation otherwise.
		blotted := make([]float32, npix)
		for y := 0; y < f.Height; y++ {
			for x := 0; x < f.Width; x++ {
				var refX, refY float64
				if f.MapFunc != nil {
					refX, refY = f.MapFunc(float64(x), float64(y))
					// MapFunc already includes any offset; OffsetX/Y are 0 here.
					refX -= minX
					refY -= minY
				} else {
					refX, refY = ApplyAffineTransform(f.SourceToRef, float64(x), float64(y))
					refX = refX + f.OffsetX - minX
					refY = refY + f.OffsetY - minY
				}
				ox := refX * scale
				oy := refY * scale
				v := bilinearSample(model, outW, outH, ox, oy)
				if math.IsNaN(v) {
					blotted[y*f.Width+x] = float32(math.NaN())
				} else {
					blotted[y*f.Width+x] = float32(v)
				}
			}
		}

		// Excess (data - blotted model) for every comparable pixel; NaN marks
		// pixels with no valid data or no model coverage. Computed over the full
		// frame so the growth stages can also consider border pixels.
		nan32 := float32(math.NaN())
		excesses := make([]float32, npix)
		for idx := range excesses {
			excesses[idx] = nan32
		}
		for idx := 0; idx < npix; idx++ {
			val := float64(f.Pixels[idx])
			if math.IsNaN(val) || math.IsInf(val, 0) || val <= 0 {
				continue
			}
			bv := float64(blotted[idx])
			if math.IsNaN(bv) || math.IsInf(bv, 0) {
				continue
			}
			excesses[idx] = float32(val - bv)
		}

		// Seed pass: flag pixels whose excess clears the per-pixel noise floor
		// plus a derivative-scaled allowance for steep model gradients.
		for y := 1; y < f.Height-1; y++ {
			for x := 1; x < f.Width-1; x++ {
				idx := y*f.Width + x
				excess := float64(excesses[idx])
				if math.IsNaN(excess) || excess <= 0 {
					continue
				}
				bv := float64(blotted[idx])
				// 4-neighbor max-absolute derivative of the blotted model.
				var maxDeriv float64
				for _, d := range dirs4 {
					nbv := float64(blotted[(y+d[1])*f.Width+(x+d[0])])
					if math.IsNaN(nbv) {
						continue
					}
					if diff := math.Abs(bv - nbv); diff > maxDeriv {
						maxDeriv = diff
					}
				}
				if excess > opts.SeedSNR*noiseAt(idx)+opts.DerivScale*maxDeriv {
					mask[idx] = true
				}
			}
		}

		growCRMask(mask, excesses, f.Width, f.Height, noiseAt, opts.SeedSNR)

		masks[fi] = mask
	}

	return masks
}

// growCRMask expands a seeded cosmic-ray mask in two complementary stages, both
// using the same per-pixel noise basis (noiseAt) so thresholds track the local
// signal level:
//
//  1. Connected propagation: flood-fill into 8-connected neighbors whose excess
//     clears half the seed floor. This walks out along the bright body of a CR.
//  2. Halo fill: a few gentle iterations that bridge weaker fringe pixels which
//     are surrounded by enough already-flagged neighbors, recovering the diffuse
//     wings a strict propagation threshold would leave behind.
//
// excesses holds data-minus-model per pixel (NaN where not comparable).
func growCRMask(mask []bool, excesses []float32, width, height int, noiseAt func(idx int) float64, seedSNR float64) {
	dirs8 := [8][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}

	// Stage 1: connected propagation from the seeds.
	q := make([]int, 0, 256)
	for idx, flagged := range mask {
		if flagged {
			q = append(q, idx)
		}
	}
	for len(q) > 0 {
		currIdx := q[0]
		q = q[1:]
		cx := currIdx % width
		cy := currIdx / width
		for _, d := range dirs8 {
			nx, ny := cx+d[0], cy+d[1]
			if nx < 0 || nx >= width || ny < 0 || ny >= height {
				continue
			}
			nIdx := ny*width + nx
			if mask[nIdx] {
				continue
			}
			ex := float64(excesses[nIdx])
			if math.IsNaN(ex) {
				continue
			}
			if ex > 0.5*seedSNR*noiseAt(nIdx) {
				mask[nIdx] = true
				q = append(q, nIdx)
			}
		}
	}

	// Stage 2: halo fill for weaker fringe pixels with enough flagged support.
	for iter := 0; iter < 2; iter++ {
		added := make([]int, 0, 64)
		for idx, flagged := range mask {
			if !flagged {
				continue
			}
			cx := idx % width
			cy := idx / width
			for ny := maxInt(0, cy-1); ny <= minInt(height-1, cy+1); ny++ {
				for nx := maxInt(0, cx-1); nx <= minInt(width-1, cx+1); nx++ {
					nIdx := ny*width + nx
					if nIdx == idx || mask[nIdx] {
						continue
					}
					ex := float64(excesses[nIdx])
					if math.IsNaN(ex) {
						continue
					}
					nse := noiseAt(nIdx)
					if ex <= 0.5*nse {
						continue
					}
					neighbors := countMaskedNeighbors(mask, width, height, nx, ny)
					if neighbors >= 2 || (neighbors >= 1 && ex > nse) {
						added = append(added, nIdx)
					}
				}
			}
		}
		if len(added) == 0 {
			return
		}
		for _, idx := range added {
			mask[idx] = true
		}
	}
}

func countMaskedNeighbors(mask []bool, width, height, x, y int) int {
	count := 0
	for ny := maxInt(0, y-1); ny <= minInt(height-1, y+1); ny++ {
		for nx := maxInt(0, x-1); nx <= minInt(width-1, x+1); nx++ {
			if nx == x && ny == y {
				continue
			}
			if mask[ny*width+nx] {
				count++
			}
		}
	}
	return count
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// bilinearSample returns the bilinear-interpolated value at (x, y). It returns
// NaN only when the sample falls outside the grid or all four surrounding pixels
// are NaN. When some (but not all) corners are NaN — as happens one pixel inside
// a coverage edge — the weights are renormalized over the finite corners so the
// sample degrades gracefully instead of leaving a NaN ring along the boundary
// (which would silently exclude cosmic rays near frame/coverage edges).
func bilinearSample(pixels []float32, width, height int, x, y float64) float64 {
	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	x1 := x0 + 1
	y1 := y0 + 1
	if x0 < 0 || x1 >= width || y0 < 0 || y1 >= height {
		return math.NaN()
	}
	wx := x - float64(x0)
	wy := y - float64(y0)
	corners := [4]struct {
		v float64
		w float64
	}{
		{float64(pixels[y0*width+x0]), (1 - wx) * (1 - wy)},
		{float64(pixels[y0*width+x1]), wx * (1 - wy)},
		{float64(pixels[y1*width+x0]), (1 - wx) * wy},
		{float64(pixels[y1*width+x1]), wx * wy},
	}
	var sum, wsum float64
	for _, c := range corners {
		if math.IsNaN(c.v) {
			continue
		}
		sum += c.v * c.w
		wsum += c.w
	}
	if wsum <= 0 {
		return math.NaN()
	}
	return sum / wsum
}
