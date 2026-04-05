package processing

import (
	"math"
	"sort"
)

func RemoveCosmicRays(pixels []float32, width, height int, globalSigma float64, passes int, masterMask []bool) []float32 {
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
