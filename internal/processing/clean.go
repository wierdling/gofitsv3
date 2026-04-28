package processing

import (
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

	currentPixels := pixels
	for p := 0; p < passes; p++ {
		out := make([]float32, len(currentPixels))
		copy(out, currentPixels)

		cleaned := make([]bool, len(currentPixels))
		laplacianThreshold := globalSigma * 10.0
		growThreshold := globalSigma * 2.0
		dirs := [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}

		for y := 2; y < height-2; y++ {
			for x := 2; x < width-2; x++ {
				idx := y*width + x
				if masterMask != nil && masterMask[idx] {
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

				replacementVal := estimateWideBackground(currentPixels, width, height, x, y)
				var candidates []int
				q := []int{idx}
				localVisited := make([]bool, len(currentPixels))
				localVisited[idx] = true
				minX, maxX, minY, maxY := x, x, y, y

				for len(q) > 0 {
					currIdx := q[0]
					q = q[1:]
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
						if nx < 0 || nx >= width || ny < 0 || ny >= height {
							continue
						}
						nIdx := ny*width + nx
						if cleaned[nIdx] || localVisited[nIdx] {
							continue
						}
						nVal := float64(currentPixels[nIdx])
						if math.IsNaN(nVal) {
							continue
						}
						if nVal > replacementVal+growThreshold {
							localVisited[nIdx] = true
							q = append(q, nIdx)
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
					cleaned[idx] = true
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
	var bg []float64
	for dy := -5; dy <= 5; dy++ {
		for dx := -5; dx <= 5; dx++ {
			nx, ny := cx+dx, cy+dy
			if nx >= 0 && nx < width && ny >= 0 && ny < height {
				val := float64(pixels[ny*width+nx])
				if !math.IsNaN(val) {
					bg = append(bg, val)
				}
			}
		}
	}
	if len(bg) == 0 {
		return 0
	}
	sort.Float64s(bg)
	return bg[len(bg)/2]
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
	// MapFunc, if non-nil, maps a source pixel (x,y) to reference space.
	// BuildCRMasksFromModel uses it instead of SourceToRef for blotting,
	// allowing a full WCS mapper to be passed in for per-pixel accuracy.
	// The returned coordinates must already include any offset adjustment
	// (i.e. they are the same reference-space coords used during drizzle).
	MapFunc func(x, y float64) (float64, float64)
}

// BuildCosmicRayMasks detects cosmic rays by comparing each pixel across
// multiple aligned frames. A pixel that is significantly brighter than the
// corresponding position in other frames is flagged as a seed, then grown
// into adjacent above-threshold pixels. Returns one boolean mask per frame
// (true = cosmic ray, skip during drizzle).
func BuildCosmicRayMasks(frames []FrameInfo, seedMultiplier, growMultiplier float64) [][]bool {
	debuglog.Log("BuildCosmicRayMasks: starting")
	defer debuglog.Log("BuildCosmicRayMasks: finished")
	n := len(frames)
	masks := make([][]bool, n)

	for i := range frames {
		f := &frames[i]
		npix := f.Width * f.Height

		if f.Sigma <= 0 {
			masks[i] = make([]bool, npix)
			continue
		}

		excess := make([]float64, npix)
		compared := make([]bool, npix)
		samples := make([]float64, 0, n-1)

		for y := 0; y < f.Height; y++ {
			for x := 0; x < f.Width; x++ {
				idx := y*f.Width + x
				val := float64(f.Pixels[idx])
				if math.IsNaN(val) || val <= 0 {
					continue
				}

				// Transform to reference space
				refX, refY := ApplyAffineTransform(f.SourceToRef, float64(x), float64(y))
				refX += f.OffsetX
				refY += f.OffsetY

				// Sample corresponding position in other frames
				samples = samples[:0]
				for j := 0; j < n; j++ {
					if j == i {
						continue
					}
					g := &frames[j]
					sx, sy := ApplyAffineTransform(g.RefToSource, refX-g.OffsetX, refY-g.OffsetY)
					s := bilinearSample(g.Pixels, g.Width, g.Height, sx, sy)
					if !math.IsNaN(s) && s >= 0 {
						samples = append(samples, s)
					}
				}

				if len(samples) == 0 {
					continue
				}

				sort.Float64s(samples)
				median := samples[len(samples)/2]
				excess[idx] = val - median
				compared[idx] = true
			}
		}

		// Identify seeds: pixels far brighter than corresponding pixels in other frames.
		seedThreshold := seedMultiplier * f.Sigma
		growThreshold := growMultiplier * f.Sigma
		mask := make([]bool, npix)
		q := make([]int, 0, 256)

		for idx := 0; idx < npix; idx++ {
			if compared[idx] && excess[idx] > seedThreshold {
				mask[idx] = true
				q = append(q, idx)
			}
		}

		// Flood-fill from seeds into neighbors that also show excess.
		dirs := [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
		for len(q) > 0 {
			currIdx := q[0]
			q = q[1:]
			cx := currIdx % f.Width
			cy := currIdx / f.Width

			for _, d := range dirs {
				nx, ny := cx+d[0], cy+d[1]
				if nx < 0 || nx >= f.Width || ny < 0 || ny >= f.Height {
					continue
				}
				nIdx := ny*f.Width + nx
				if mask[nIdx] {
					continue
				}
				if compared[nIdx] && excess[nIdx] > growThreshold {
					mask[nIdx] = true
					q = append(q, nIdx)
				}
			}
		}

		masks[i] = mask
	}

	return masks
}

// DrizzleStyleCROptions controls the AstroDrizzle-like multi-frame CR detector.
type DrizzleStyleCROptions struct {
	// SeedSNR is the signal-to-noise ratio above which a pixel is seeded as a
	// cosmic-ray candidate.  AstroDrizzle default is ~4.
	SeedSNR float64
	// DerivScale weights the 4-neighbor derivative contribution to the
	// per-pixel threshold.  AstroDrizzle default is ~1.2.
	DerivScale float64
}

// BuildDrizzleStyleCRMasks implements an AstroDrizzle-like cosmic-ray detector
// that operates across multiple aligned exposures:
//
//  1. Builds a clean model image in the output (drizzle) space using minmed
//     (min of mean and median) for small stacks (n ≤ 3) or median for larger
//     stacks.  Each output pixel is sampled from every frame via inverse blot.
//  2. Blots the model back to each input frame's pixel space.
//  3. Computes a 4-neighbor max-absolute derivative at each frame pixel.
//  4. Seeds pixels whose excess over the blotted model exceeds
//     SeedSNR*sigma + DerivScale*derivative.
//  5. Grows flags to 4-connected neighbors that also show positive excess.
//
// The returned per-frame masks are intended to be used during the final drizzle
// combine so that flagged pixels are skipped rather than replaced in-place.
//
// outW/outH are the drizzle output canvas dimensions.
// minX/minY are the output origin in reference pixel space.
// scale is the drizzle scale factor (output pixels per reference pixel).
func BuildDrizzleStyleCRMasks(frames []FrameInfo, outW, outH int, minX, minY, scale float64, opts DrizzleStyleCROptions) [][]bool {
	debuglog.Log("BuildDrizzleStyleCRMasks: starting")
	defer debuglog.Log("BuildDrizzleStyleCRMasks: finished")
	if len(frames) == 0 {
		return nil
	}
	model := buildInverseBlotModel(frames, outW, outH, minX, minY, scale)
	return BuildCRMasksFromModel(frames, model, outW, outH, minX, minY, scale, opts)
}

// buildInverseBlotModel builds a clean model image in output space by sampling
// each input frame at the corresponding source position (inverse blot) and
// computing a median or minmed across all frames at each output pixel.
func buildInverseBlotModel(frames []FrameInfo, outW, outH int, minX, minY, scale float64) []float32 {
	n := len(frames)
	model := make([]float32, outW*outH)
	// Reuse a single scratch buffer across all output pixels instead of
	// allocating one []float64 per pixel (which would be O(outW*outH) allocs).
	vals := make([]float64, 0, n)

	for oy := 0; oy < outH; oy++ {
		refY := float64(oy)/scale + minY
		for ox := 0; ox < outW; ox++ {
			refX := float64(ox)/scale + minX
			vals = vals[:0]
			for i := range frames {
				f := &frames[i]
				sx, sy := ApplyAffineTransform(f.RefToSource, refX-f.OffsetX, refY-f.OffsetY)
				v := bilinearSample(f.Pixels, f.Width, f.Height, sx, sy)
				if !math.IsNaN(v) && v >= 0 {
					vals = append(vals, v)
				}
			}
			outIdx := oy*outW + ox
			if len(vals) == 0 {
				model[outIdx] = float32(math.NaN())
				continue
			}
			sort.Float64s(vals)
			median := vals[(len(vals)-1)/2]
			if n <= 3 {
				var sum float64
				for _, v := range vals {
					sum += v
				}
				mean := sum / float64(len(vals))
				if mean < median {
					model[outIdx] = float32(mean)
				} else {
					model[outIdx] = float32(median)
				}
			} else {
				model[outIdx] = float32(median)
			}
		}
	}
	return model
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
	dirs8 := [8][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
	masks := make([][]bool, n)

	for fi := range frames {
		f := &frames[fi]
		if f.Sigma <= 0 {
			masks[fi] = make([]bool, f.Width*f.Height)
			continue
		}

		npix := f.Width * f.Height
		mask := make([]bool, npix)
		excesses := make([]float64, npix)

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

		noiseFloor := opts.SeedSNR * f.Sigma

		// Seed pass: flag pixels above derivative-scaled noise threshold.
		for y := 1; y < f.Height-1; y++ {
			for x := 1; x < f.Width-1; x++ {
				idx := y*f.Width + x
				val := float64(f.Pixels[idx])
				if math.IsNaN(val) || val <= 0 {
					continue
				}
				bv := float64(blotted[idx])
				if math.IsNaN(bv) {
					continue
				}
				excess := val - bv
				excesses[idx] = excess
				if excess <= 0 {
					continue
				}
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
				if excess > noiseFloor+opts.DerivScale*maxDeriv {
					mask[idx] = true
				}
			}
		}

		// Growth pass: expand flags to 8-connected neighbors with positive excess.
		growFloor := noiseFloor * 0.5
		q := make([]int, 0, 256)
		for idx, flagged := range mask {
			if flagged {
				q = append(q, idx)
			}
		}
		for len(q) > 0 {
			currIdx := q[0]
			q = q[1:]
			cx := currIdx % f.Width
			cy := currIdx / f.Width
			for _, d := range dirs8 {
				nx, ny := cx+d[0], cy+d[1]
				if nx < 0 || nx >= f.Width || ny < 0 || ny >= f.Height {
					continue
				}
				nIdx := ny*f.Width + nx
				if mask[nIdx] {
					continue
				}
				nv := float64(f.Pixels[nIdx])
				if math.IsNaN(nv) {
					continue
				}
				nbv := float64(blotted[nIdx])
				if math.IsNaN(nbv) {
					continue
				}
				if nv-nbv > growFloor {
					mask[nIdx] = true
					q = append(q, nIdx)
				}
			}
		}

		recoverCosmicRayHalo(mask, excesses, f.Width, f.Height, f.Sigma)

		masks[fi] = mask
	}

	return masks
}

func recoverCosmicRayHalo(mask []bool, excesses []float64, width, height int, sigma float64) {
	if len(mask) == 0 || sigma <= 0 {
		return
	}

	haloFloor := sigma * 0.5
	if haloFloor <= 0 {
		return
	}

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
					if excesses[nIdx] <= haloFloor {
						continue
					}
					neighbors := countMaskedNeighbors(mask, width, height, nx, ny)
					if neighbors >= 2 || (neighbors >= 1 && excesses[nIdx] > sigma) {
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

// bilinearSample returns the bilinear-interpolated value at (x, y).
// Returns NaN if the position is out of bounds or touches a NaN pixel.
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
	p00 := float64(pixels[y0*width+x0])
	p10 := float64(pixels[y0*width+x1])
	p01 := float64(pixels[y1*width+x0])
	p11 := float64(pixels[y1*width+x1])
	if math.IsNaN(p00) || math.IsNaN(p10) || math.IsNaN(p01) || math.IsNaN(p11) {
		return math.NaN()
	}
	return p00*(1-wx)*(1-wy) + p10*wx*(1-wy) + p01*(1-wx)*wy + p11*wx*wy
}
