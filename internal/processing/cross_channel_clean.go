package processing

import (
	"math"
	"sort"
)

func CrossChannelClean(targetPixels, refPixels []float32, width, height int, targetSigma, refSigma float64) []float32 {
	out := make([]float32, len(targetPixels))
	copy(out, targetPixels)
	cleaned := make([]bool, len(targetPixels))

	laplacianThreshold := targetSigma * 10.0
	growThreshold := targetSigma * 2.0
	refConfirmThreshold := refSigma * 1.0
	dirs := [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}

	for y := 2; y < height-2; y++ {
		for x := 2; x < width-2; x++ {
			idx := y*width + x
			if cleaned[idx] {
				continue
			}

			valT := float64(targetPixels[idx])
			if math.IsNaN(valT) || valT <= 0 {
				continue
			}

			up := float64(targetPixels[(y-1)*width+x])
			down := float64(targetPixels[(y+1)*width+x])
			left := float64(targetPixels[y*width+(x-1)])
			right := float64(targetPixels[y*width+(x+1)])
			if math.IsNaN(up) {
				up = valT
			}
			if math.IsNaN(down) {
				down = valT
			}
			if math.IsNaN(left) {
				left = valT
			}
			if math.IsNaN(right) {
				right = valT
			}

			laplacian := (4.0 * valT) - (up + down + left + right)
			if laplacian <= laplacianThreshold {
				continue
			}

			valR := float64(refPixels[idx])
			refBg := estimateLocalBackground(refPixels, width, height, x, y)
			if !math.IsNaN(valR) && valR >= refBg+refConfirmThreshold {
				continue
			}

			targetBg := estimateLocalBackground(targetPixels, width, height, x, y)
			var candidates []int
			q := []int{idx}
			localVisited := map[int]bool{idx: true}
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
					nVal := float64(targetPixels[nIdx])
					if math.IsNaN(nVal) {
						continue
					}
					if nVal > targetBg+growThreshold {
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
			if area > 45 {
				isStar = true
			} else if area > 8 {
				fillFactor := float64(area) / float64(boxArea)
				if fillFactor > 0.65 && boxWidth > 2 && boxHeight > 2 {
					isStar = true
				}
			}

			if isStar {
				cleaned[idx] = true
			} else {
				for _, cIdx := range candidates {
					out[cIdx] = float32(targetBg)
					cleaned[cIdx] = true
				}
			}
		}
	}

	return out
}

func estimateLocalBackground(pixels []float32, width, height int, cx, cy int) float64 {
	var bg []float64
	for dy := -3; dy <= 3; dy++ {
		for dx := -3; dx <= 3; dx++ {
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
