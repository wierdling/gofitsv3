package processing

import "math"

func Downsample(pixels []float32, width, height, factor int) ([]float32, int, int) {
	if factor <= 1 {
		return pixels, width, height
	}

	newWidth := width / factor
	newHeight := height / factor
	newPixels := make([]float32, newWidth*newHeight)

	for ny := 0; ny < newHeight; ny++ {
		for nx := 0; nx < newWidth; nx++ {
			var sum float64
			var count int
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					origX := (nx * factor) + dx
					origY := (ny * factor) + dy
					if origX >= width || origY >= height {
						continue
					}
					idx := origY*width + origX
					val := float64(pixels[idx])
					if !math.IsNaN(val) {
						sum += val
						count++
					}
				}
			}
			newIdx := ny*newWidth + nx
			if count > 0 {
				newPixels[newIdx] = float32(sum / float64(count))
			} else {
				newPixels[newIdx] = float32(math.NaN())
			}
		}
	}
	return newPixels, newWidth, newHeight
}
