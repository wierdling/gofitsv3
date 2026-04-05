package processing

import "math"

func BuildMasterMask(channels [][]float32, width, height int, sigmas []float64) []bool {
	totalPixels := width * height
	mask := make([]bool, totalPixels)
	if len(channels) < 2 {
		return mask
	}

	bgs := make([]float64, len(channels))
	for i, ch := range channels {
		bg, _ := EstimateBackground(ch)
		bgs[i] = bg
	}

	for i := 0; i < totalPixels; i++ {
		signalCount := 0
		for chIdx, ch := range channels {
			val := float64(ch[i])
			if math.IsNaN(val) {
				continue
			}
			if val > bgs[chIdx]+(sigmas[chIdx]*3.0) {
				signalCount++
			}
		}
		if signalCount >= 2 {
			mask[i] = true
		}
	}

	dilatedMask := make([]bool, totalPixels)
	copy(dilatedMask, mask)
	dirs := [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			idx := y*width + x
			if mask[idx] {
				for _, d := range dirs {
					nIdx := (y+d[1])*width + (x + d[0])
					dilatedMask[nIdx] = true
				}
			}
		}
	}
	return dilatedMask
}
