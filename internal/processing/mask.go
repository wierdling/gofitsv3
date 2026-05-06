package processing

import "math"

func BuildMasterMask(channels [][]float32, width, height int, sigmas []float64) []bool {
	layerMasks := BuildLayerStarMasks(channels, width, height, sigmas)
	channelCount, totalPixels, _, _, _ := effectiveMaskGeometry(channels, width, height, sigmas)
	mask := make([]bool, totalPixels)
	for chIdx := 0; chIdx < channelCount; chIdx++ {
		for i := 0; i < totalPixels; i++ {
			if layerMasks[chIdx][i] {
				mask[i] = true
			}
		}
	}
	return mask
}

func BuildLayerStarMasks(channels [][]float32, width, height int, sigmas []float64) [][]bool {
	channelCount, totalPixels, height, bgs, ok := effectiveMaskGeometry(channels, width, height, sigmas)
	if !ok {
		return make([][]bool, channelCount)
	}

	sharedSeeds := make([]bool, totalPixels)
	for i := 0; i < totalPixels; i++ {
		signalCount := 0
		for chIdx := 0; chIdx < channelCount; chIdx++ {
			val := float64(channels[chIdx][i])
			if math.IsNaN(val) {
				continue
			}
			if val > bgs[chIdx]+(sigmas[chIdx]*2.0) {
				signalCount++
			}
		}
		if signalCount >= 2 {
			sharedSeeds[i] = true
		}
	}

	dirs := [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
	masks := make([][]bool, channelCount)
	for chIdx := 0; chIdx < channelCount; chIdx++ {
		mask := make([]bool, totalPixels)
		threshold := bgs[chIdx] + (sigmas[chIdx] * 1.0)
		for idx, shared := range sharedSeeds {
			if !shared || mask[idx] {
				continue
			}
			seedVal := float64(channels[chIdx][idx])
			if math.IsNaN(seedVal) || seedVal <= threshold {
				continue
			}

			q := []int{idx}
			mask[idx] = true
			for len(q) > 0 {
				currIdx := q[0]
				q = q[1:]
				cx := currIdx % width
				cy := currIdx / width
				for _, d := range dirs {
					nx, ny := cx+d[0], cy+d[1]
					if nx < 0 || nx >= width || ny < 0 || ny >= height {
						continue
					}
					nIdx := ny*width + nx
					if mask[nIdx] {
						continue
					}
					val := float64(channels[chIdx][nIdx])
					if math.IsNaN(val) || val <= threshold {
						continue
					}
					mask[nIdx] = true
					q = append(q, nIdx)
				}
			}
		}
		masks[chIdx] = dilateMask(mask, width, height, 1)
	}

	return masks
}

func effectiveMaskGeometry(channels [][]float32, width, height int, sigmas []float64) (int, int, int, []float64, bool) {
	channelCount := len(channels)
	if len(sigmas) < channelCount {
		channelCount = len(sigmas)
	}
	if channelCount < 2 || width <= 0 || height <= 0 {
		return channelCount, 0, 0, nil, false
	}

	totalPixels := width * height
	for i := 0; i < channelCount; i++ {
		if len(channels[i]) < totalPixels {
			totalPixels = len(channels[i])
		}
	}
	if totalPixels == 0 {
		return channelCount, 0, 0, nil, false
	}

	maxHeight := totalPixels / width
	if maxHeight == 0 {
		return channelCount, totalPixels, 0, nil, false
	}
	if height > maxHeight {
		height = maxHeight
	}

	bgs := make([]float64, channelCount)
	for i := 0; i < channelCount; i++ {
		bg, _ := EstimateBackground(channels[i])
		bgs[i] = bg
	}
	return channelCount, totalPixels, height, bgs, true
}

func dilateMask(mask []bool, width, height, passes int) []bool {
	if len(mask) == 0 || passes <= 0 {
		return mask
	}
	dirs := [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
	current := make([]bool, len(mask))
	copy(current, mask)
	for pass := 0; pass < passes; pass++ {
		nextMask := make([]bool, len(mask))
		copy(nextMask, current)
		for y := 1; y < height-1; y++ {
			for x := 1; x < width-1; x++ {
				idx := y*width + x
				if !current[idx] {
					continue
				}
				for _, d := range dirs {
					nIdx := (y+d[1])*width + (x + d[0])
					nextMask[nIdx] = true
				}
			}
		}
		current = nextMask
	}
	return current
}
