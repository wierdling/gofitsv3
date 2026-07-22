package processing

import (
	"image"
	"math"
)

// ColorSpeckCleanConfig controls post-RGB cleanup of tiny single-color blemishes
// and near-black dropouts (the dark dots a bad detector row leaves behind).
//
// A zero-valued threshold field is treated as "use the default" by
// normalizeColorSpeckCleanConfig. Use DefaultColorSpeckCleanConfig when callers
// need explicit, editable defaults.
type ColorSpeckCleanConfig struct {
	MaxBlobPixels     int
	MinDominanceRatio float64
	MinDominanceDelta uint8
	MinDominantValue  uint8
	MinLocalExcess    uint8
	RingRadius        int

	// Dark-dropout detection: a pixel is treated as a hole when its brightest
	// channel is at or below MaxDarkValue and every channel sits at least
	// MinDarkDeficit below the local median. Requiring both keeps genuinely dark
	// regions (where the local median is also low) from being flagged.
	MaxDarkValue   uint8
	MinDarkDeficit uint8
}

// darkDropoutChannel is a sentinel "channel" used to group dark-hole candidates
// in the same blob flood-fill as the color specks (channels 0/1/2) without ever
// merging the two kinds together.
const darkDropoutChannel uint8 = 3

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
		MaxDarkValue:      64,
		MinDarkDeficit:    50,
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
	// Dark-dropout gates open up the same way: at high intensity we accept
	// less-black dots (higher MaxDarkValue) sitting on a shallower local
	// contrast (lower MinDarkDeficit).
	cfg.MaxDarkValue = uint8(math.Round(48.0 + (56.0 * t)))
	cfg.MinDarkDeficit = uint8(math.Round(70.0 - (40.0 * t)))
	return cfg
}

// CleanColorSpecksRGBA removes tiny dominant-color specks from a composed RGB image.
// It returns a cleaned copy of the source and the number of repaired pixels.
func CleanColorSpecksRGBA(src *image.RGBA, cfg ColorSpeckCleanConfig) (*image.RGBA, int) {
	if src == nil {
		return nil, 0
	}
	cfg = normalizeColorSpeckCleanConfig(cfg)

	dst := cloneRGBA(src)

	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width == 0 || height == 0 {
		return dst, 0
	}

	candidate := make([]bool, width*height)
	localExcess := make([]bool, width*height)
	dominant := make([]uint8, width*height)
	for y := 0; y < height; y++ {
		row := y * src.Stride
		for x := 0; x < width; x++ {
			idx := y*width + x
			pixIdx := row + x*4
			r := src.Pix[pixIdx]
			g := src.Pix[pixIdx+1]
			b := src.Pix[pixIdx+2]
			if ch, ok := isColorBlemish(r, g, b, cfg); ok {
				candidate[idx] = true
				dominant[idx] = ch
				if isLocalExcess(src, x, y, ch, r, g, b, cfg) {
					localExcess[idx] = true
				}
				continue
			}
			if darkDropoutCandidate(src, x, y, r, g, b, cfg) {
				candidate[idx] = true
				dominant[idx] = darkDropoutChannel
				localExcess[idx] = true
			}
		}
	}

	visited := make([]bool, width*height)
	dirs := [...][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
	repaired := 0

	for start := 0; start < len(candidate); start++ {
		if !candidate[start] || visited[start] {
			continue
		}
		blobChannel := dominant[start]
		queue := []int{start}
		visited[start] = true
		blob := make([]int, 0, cfg.MaxBlobPixels)
		blobPixels := 0
		tooLarge := false
		hasLocalExcess := false
		minX, maxX := start%width, start%width
		minY, maxY := start/width, start/width

		for head := 0; head < len(queue); head++ {
			idx := queue[head]
			if localExcess[idx] {
				hasLocalExcess = true
			}
			blobPixels++
			if blobPixels <= cfg.MaxBlobPixels {
				blob = append(blob, idx)
			} else {
				tooLarge = true
			}

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

		if blobPixels == 0 || tooLarge || !hasLocalExcess {
			continue
		}

		ringMinX := maxBlobBound(0, minX-cfg.RingRadius)
		ringMaxX := minBlobBound(width-1, maxX+cfg.RingRadius)
		ringMinY := maxBlobBound(0, minY-cfg.RingRadius)
		ringMaxY := minBlobBound(height-1, maxY+cfg.RingRadius)

		ringCap := (ringMaxX - ringMinX + 1) * (ringMaxY - ringMinY + 1)
		ringR := make([]uint8, 0, ringCap)
		ringG := make([]uint8, 0, ringCap)
		ringB := make([]uint8, 0, ringCap)
		for y := ringMinY; y <= ringMaxY; y++ {
			row := y * src.Stride
			for x := ringMinX; x <= ringMaxX; x++ {
				idx := y*width + x
				if candidate[idx] {
					continue
				}
				pixIdx := row + x*4
				ringR = append(ringR, src.Pix[pixIdx])
				ringG = append(ringG, src.Pix[pixIdx+1])
				ringB = append(ringB, src.Pix[pixIdx+2])
			}
		}
		if len(ringR) == 0 {
			continue
		}

		fillR := medianUint8(ringR)
		fillG := medianUint8(ringG)
		fillB := medianUint8(ringB)
		for _, idx := range blob {
			x := idx % width
			y := idx / width
			dstIdx := y*dst.Stride + x*4
			srcIdx := y*src.Stride + x*4
			dst.Pix[dstIdx] = fillR
			dst.Pix[dstIdx+1] = fillG
			dst.Pix[dstIdx+2] = fillB
			dst.Pix[dstIdx+3] = src.Pix[srcIdx+3]
			repaired++
		}
	}

	return dst, repaired
}

func cloneRGBA(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	widthBytes := src.Bounds().Dx() * 4
	height := src.Bounds().Dy()
	for y := 0; y < height; y++ {
		srcOff := y * src.Stride
		dstOff := y * dst.Stride
		copy(dst.Pix[dstOff:dstOff+widthBytes], src.Pix[srcOff:srcOff+widthBytes])
	}
	return dst
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
	if cfg.MaxDarkValue == 0 {
		cfg.MaxDarkValue = def.MaxDarkValue
	}
	if cfg.MinDarkDeficit == 0 {
		cfg.MinDarkDeficit = def.MinDarkDeficit
	}
	return cfg
}

// isColorBlemish reports whether (r,g,b) satisfies absolute color dominance criteria.
func isColorBlemish(r, g, b uint8, cfg ColorSpeckCleanConfig) (uint8, bool) {
	ch, dom, secondary := dominantChannel(r, g, b)
	if dom < cfg.MinDominantValue {
		return 0, false
	}
	if dom-secondary < cfg.MinDominanceDelta {
		return 0, false
	}
	if dominanceRatio(dom, secondary) < cfg.MinDominanceRatio {
		return 0, false
	}
	return ch, true
}

// isLocalExcess reports whether (x,y)'s dominant channel exceeds its local median by at least MinLocalExcess.
func isLocalExcess(src *image.RGBA, x, y int, ch uint8, r, g, b uint8, cfg ColorSpeckCleanConfig) bool {
	_, dom, _ := dominantChannel(r, g, b)
	local := localChannelMedian(src, x, y, ch)
	return int(dom) >= int(local)+int(cfg.MinLocalExcess)
}

// darkDropoutCandidate reports whether (x,y) is a near-black hole sitting on a
// meaningfully brighter background — the kind of dark dot a bad detector row
// leaves behind after cross-channel cleanup. Every channel must be both dark in
// absolute terms and well below the local median, which keeps genuinely dark
// regions (where the local median is also low) from being flagged.
func darkDropoutCandidate(src *image.RGBA, x, y int, r, g, b uint8, cfg ColorSpeckCleanConfig) bool {
	if maxUint8(maxUint8(r, g), b) > cfg.MaxDarkValue {
		return false
	}
	med := localChannelMedians(src, x, y)
	return channelDeficit(med[0], r) >= cfg.MinDarkDeficit &&
		channelDeficit(med[1], g) >= cfg.MinDarkDeficit &&
		channelDeficit(med[2], b) >= cfg.MinDarkDeficit
}

// channelDeficit is how far val sits below med, clamped at zero.
func channelDeficit(med, val uint8) uint8 {
	if med <= val {
		return 0
	}
	return med - val
}

// localChannelMedians returns the per-channel 5x5 median around (cx,cy) in one
// pass, used by the dark-dropout test.
func localChannelMedians(src *image.RGBA, cx, cy int) [3]uint8 {
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	var rs [25]uint8
	var gs [25]uint8
	var bs [25]uint8
	n := 0
	for dy := -2; dy <= 2; dy++ {
		ny := cy + dy
		if ny < 0 || ny >= height {
			continue
		}
		row := ny * src.Stride
		for dx := -2; dx <= 2; dx++ {
			nx := cx + dx
			if nx < 0 || nx >= width {
				continue
			}
			pixIdx := row + nx*4
			rs[n] = src.Pix[pixIdx]
			gs[n] = src.Pix[pixIdx+1]
			bs[n] = src.Pix[pixIdx+2]
			n++
		}
	}
	return [3]uint8{medianUint8(rs[:n]), medianUint8(gs[:n]), medianUint8(bs[:n])}
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

	var values [25]uint8
	n := 0
	for dy := -2; dy <= 2; dy++ {
		ny := cy + dy
		if ny < 0 || ny >= height {
			continue
		}
		row := ny * src.Stride
		for dx := -2; dx <= 2; dx++ {
			nx := cx + dx
			if nx < 0 || nx >= width {
				continue
			}
			values[n] = src.Pix[row+nx*4+int(ch)]
			n++
		}
	}
	return medianUint8(values[:n])
}

func medianUint8(values []uint8) uint8 {
	if len(values) == 0 {
		return 0
	}
	for i := 1; i < len(values); i++ {
		v := values[i]
		j := i - 1
		for j >= 0 && values[j] > v {
			values[j+1] = values[j]
			j--
		}
		values[j+1] = v
	}
	return values[len(values)/2]
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
