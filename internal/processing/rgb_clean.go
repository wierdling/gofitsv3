package processing

import (
	"image"
	"math"
	"sort"
)

// ColorSpeckCleanConfig controls post-RGB cleanup of tiny single-color blemishes.
type ColorSpeckCleanConfig struct {
	MaxBlobPixels     int
	MinDominanceRatio float64
	MinDominanceDelta uint8
	MinDominantValue  uint8
	MinLocalExcess    uint8
	RingRadius        int
}

// DefaultColorSpeckCleanConfig targets small pure-color cosmic-ray remnants in
// composed RGB images without touching larger valid structures.
func DefaultColorSpeckCleanConfig() ColorSpeckCleanConfig {
	return ColorSpeckCleanConfig{
		MaxBlobPixels:     25,
		MinDominanceRatio: 2.5,
		MinDominanceDelta: 60,
		MinDominantValue:  96,
		MinLocalExcess:    40,
		RingRadius:        2,
	}
}

// ColorSpeckCleanConfigFromSettings maps simple UI settings onto the cleaner thresholds.
func ColorSpeckCleanConfigFromSettings(maxBlobPixels int, intensity float64) ColorSpeckCleanConfig {
	cfg := DefaultColorSpeckCleanConfig()
	if maxBlobPixels > 0 {
		cfg.MaxBlobPixels = maxBlobPixels
	}
	t := intensity / 100.0
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	// Bias slightly toward the aggressive end so the upper half of the slider
	// opens up more clearly without making the low end too touchy.
	t = math.Pow(t, 0.8)
	cfg.MinDominanceRatio = 4.0 - (2.4 * t)
	cfg.MinDominanceDelta = uint8(math.Round(90.0 - (66.0 * t)))
	cfg.MinDominantValue = uint8(math.Round(140.0 - (84.0 * t)))
	cfg.MinLocalExcess = uint8(math.Round(72.0 - (56.0 * t)))
	return cfg
}

// CleanColorSpecksRGBA removes tiny dominant-color specks from a composed RGB image.
// It returns a cleaned copy of the source and the number of repaired pixels.
func CleanColorSpecksRGBA(src *image.RGBA, cfg ColorSpeckCleanConfig) (*image.RGBA, int) {
	if src == nil {
		return nil, 0
	}
	cfg = normalizeColorSpeckCleanConfig(cfg)

	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)

	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width == 0 || height == 0 {
		return dst, 0
	}

	candidate := make([]bool, width*height)
	dominant := make([]uint8, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			idx := y*width + x
			pixIdx := y*src.Stride + x*4
			r := src.Pix[pixIdx]
			g := src.Pix[pixIdx+1]
			b := src.Pix[pixIdx+2]
			ch, dom, secondary := dominantChannel(r, g, b)
			if dom < cfg.MinDominantValue {
				continue
			}
			if dom-secondary < cfg.MinDominanceDelta {
				continue
			}
			if dominanceRatio(dom, secondary) < cfg.MinDominanceRatio {
				continue
			}
			if dom < localChannelMedian(src, x, y, ch)+cfg.MinLocalExcess {
				continue
			}
			candidate[idx] = true
			dominant[idx] = ch
		}
	}

	visited := make([]bool, width*height)
	dirs := [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
	repaired := 0

	for start := 0; start < len(candidate); start++ {
		if !candidate[start] || visited[start] {
			continue
		}
		blobChannel := dominant[start]
		queue := []int{start}
		visited[start] = true
		blob := make([]int, 0, cfg.MaxBlobPixels)
		minX, maxX := start%width, start%width
		minY, maxY := start/width, start/width

		for len(queue) > 0 {
			idx := queue[0]
			queue = queue[1:]
			blob = append(blob, idx)

			x := idx % width
			y := idx / width
			if x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
			if y < minY {
				minY = y
			}
			if y > maxY {
				maxY = y
			}

			for _, d := range dirs {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || nx >= width || ny < 0 || ny >= height {
					continue
				}
				nIdx := ny*width + nx
				if visited[nIdx] || !candidate[nIdx] || dominant[nIdx] != blobChannel {
					continue
				}
				visited[nIdx] = true
				queue = append(queue, nIdx)
			}
		}

		if len(blob) == 0 || len(blob) > cfg.MaxBlobPixels {
			continue
		}

		blobSet := make(map[int]struct{}, len(blob))
		for _, idx := range blob {
			blobSet[idx] = struct{}{}
		}

		ringMinX := maxBlobBound(0, minX-cfg.RingRadius)
		ringMaxX := minBlobBound(width-1, maxX+cfg.RingRadius)
		ringMinY := maxBlobBound(0, minY-cfg.RingRadius)
		ringMaxY := minBlobBound(height-1, maxY+cfg.RingRadius)

		ringR := make([]int, 0, (ringMaxX-ringMinX+1)*(ringMaxY-ringMinY+1))
		ringG := make([]int, 0, cap(ringR))
		ringB := make([]int, 0, cap(ringR))
		for y := ringMinY; y <= ringMaxY; y++ {
			for x := ringMinX; x <= ringMaxX; x++ {
				idx := y*width + x
				if _, isBlob := blobSet[idx]; isBlob {
					continue
				}
				if candidate[idx] {
					continue
				}
				pixIdx := y*src.Stride + x*4
				ringR = append(ringR, int(src.Pix[pixIdx]))
				ringG = append(ringG, int(src.Pix[pixIdx+1]))
				ringB = append(ringB, int(src.Pix[pixIdx+2]))
			}
		}
		if len(ringR) == 0 {
			continue
		}

		fillR := medianInt(ringR)
		fillG := medianInt(ringG)
		fillB := medianInt(ringB)
		for _, idx := range blob {
			x := idx % width
			y := idx / width
			pixIdx := y*dst.Stride + x*4
			dst.Pix[pixIdx] = byte(fillR)
			dst.Pix[pixIdx+1] = byte(fillG)
			dst.Pix[pixIdx+2] = byte(fillB)
			dst.Pix[pixIdx+3] = src.Pix[pixIdx+3]
			repaired++
		}
	}

	return dst, repaired
}

func normalizeColorSpeckCleanConfig(cfg ColorSpeckCleanConfig) ColorSpeckCleanConfig {
	def := DefaultColorSpeckCleanConfig()
	if cfg.MaxBlobPixels <= 0 {
		cfg.MaxBlobPixels = def.MaxBlobPixels
	}
	if cfg.MinDominanceRatio <= 0 {
		cfg.MinDominanceRatio = def.MinDominanceRatio
	}
	if cfg.MinDominanceDelta == 0 {
		cfg.MinDominanceDelta = def.MinDominanceDelta
	}
	if cfg.MinDominantValue == 0 {
		cfg.MinDominantValue = def.MinDominantValue
	}
	if cfg.MinLocalExcess == 0 {
		cfg.MinLocalExcess = def.MinLocalExcess
	}
	if cfg.RingRadius <= 0 {
		cfg.RingRadius = def.RingRadius
	}
	return cfg
}

func dominantChannel(r, g, b uint8) (uint8, uint8, uint8) {
	ch := uint8(0)
	dom := r
	secondary := maxUint8(g, b)
	if g > dom {
		ch = 1
		dom = g
		secondary = maxUint8(r, b)
	}
	if b > dom {
		ch = 2
		dom = b
		secondary = maxUint8(r, g)
	}
	return ch, dom, secondary
}

func dominanceRatio(dom, secondary uint8) float64 {
	if secondary == 0 {
		return float64(dom)
	}
	return float64(dom) / float64(secondary)
}

func localChannelMedian(src *image.RGBA, cx, cy int, ch uint8) uint8 {
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	values := make([]int, 0, 25)
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			nx, ny := cx+dx, cy+dy
			if nx < 0 || nx >= width || ny < 0 || ny >= height {
				continue
			}
			pixIdx := ny*src.Stride + nx*4 + int(ch)
			values = append(values, int(src.Pix[pixIdx]))
		}
	}
	return byte(medianInt(values))
}

func medianInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	return sorted[len(sorted)/2]
}

func maxUint8(a, b uint8) uint8 {
	if a > b {
		return a
	}
	return b
}

func minBlobBound(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxBlobBound(a, b int) int {
	if a > b {
		return a
	}
	return b
}
