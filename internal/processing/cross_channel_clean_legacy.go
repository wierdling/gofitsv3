package processing

import (
	"math"
	"sort"
)

// CrossChannelClean is the original in-memory two-channel cleaner retained for
// normal Compose mode. Disk-backed Compose uses CrossChannelCleanTiled.
func CrossChannelClean(targetPixels, refPixels []float32, width, height int, targetSigma, refSigma float64) []float32 {
	out := append([]float32(nil), targetPixels...)
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
			v := float64(targetPixels[idx])
			if math.IsNaN(v) || v <= 0 {
				continue
			}
			up := float64(targetPixels[(y-1)*width+x])
			down := float64(targetPixels[(y+1)*width+x])
			left := float64(targetPixels[y*width+x-1])
			right := float64(targetPixels[y*width+x+1])
			if math.IsNaN(up) {
				up = v
			}
			if math.IsNaN(down) {
				down = v
			}
			if math.IsNaN(left) {
				left = v
			}
			if math.IsNaN(right) {
				right = v
			}
			if 4*v-(up+down+left+right) <= laplacianThreshold {
				continue
			}
			if rv := float64(refPixels[idx]); !math.IsNaN(rv) && rv >= estimateLocalBackground(refPixels, width, height, x, y)+refConfirmThreshold {
				continue
			}
			bg := estimateLocalBackground(targetPixels, width, height, x, y)
			q := []int{idx}
			seen := map[int]bool{idx: true}
			candidates := make([]int, 0, 8)
			minX, maxX, minY, maxY := x, x, y, y
			for len(q) > 0 {
				ci := q[0]
				q = q[1:]
				candidates = append(candidates, ci)
				cx, cy := ci%width, ci/width
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
					ni := ny*width + nx
					if seen[ni] || cleaned[ni] || math.IsNaN(float64(targetPixels[ni])) {
						continue
					}
					if float64(targetPixels[ni]) > bg+growThreshold {
						seen[ni] = true
						q = append(q, ni)
					}
				}
			}
			area := len(candidates)
			box := (maxX - minX + 1) * (maxY - minY + 1)
			star := area > 45 || (area > 8 && float64(area)/float64(box) > 0.65 && maxX-minX+1 > 2 && maxY-minY+1 > 2)
			for _, ci := range candidates {
				cleaned[ci] = true
				if !star {
					out[ci] = float32(bg)
				}
			}
		}
	}
	return out
}

func estimateLocalBackground(pixels []float32, width, height, cx, cy int) float64 {
	bg := make([]float64, 0, 49)
	for dy := -3; dy <= 3; dy++ {
		for dx := -3; dx <= 3; dx++ {
			nx, ny := cx+dx, cy+dy
			if nx >= 0 && nx < width && ny >= 0 && ny < height {
				v := float64(pixels[ny*width+nx])
				if !math.IsNaN(v) {
					bg = append(bg, v)
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
