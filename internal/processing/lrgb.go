package processing

import (
	"context"
	"fmt"
	"math"
)

// ApplyLRGBToRGBA applies a weighted synthetic luminance to a preview while
// preserving chroma ratios. Dedicated L is handled by ComposeLRGB on planes.
func ApplyLRGBToRGBA(buf []byte, width, height int, cfg LRGBConfig) []byte {
	if cfg.LuminanceWeight <= 0 {
		return buf
	}
	if width <= 0 || height <= 0 || len(buf) < width*height*4 {
		return buf
	}
	pixels := width * height
	r, g, b := make([]float32, pixels), make([]float32, pixels), make([]float32, pixels)
	for i := 0; i < pixels; i++ {
		r[i] = float32(buf[i*4]) / 255
		g[i] = float32(buf[i*4+1]) / 255
		b[i] = float32(buf[i*4+2]) / 255
	}
	planes, err := ComposeLRGB(context.Background(), r, g, b, nil, width, height, cfg)
	if err != nil {
		return buf
	}
	out := append([]byte(nil), buf...)
	for i := 0; i < pixels; i++ {
		out[i*4] = uint8(clampLRGB(float64(planes[i])*255) + .5)
		out[i*4+1] = uint8(clampLRGB(float64(planes[pixels+i])*255) + .5)
		out[i*4+2] = uint8(clampLRGB(float64(planes[2*pixels+i])*255) + .5)
	}
	return out
}

// ApplyDedicatedLToRGBA injects a dedicated luminance plane into an already
// rendered RGB preview. The L plane is resized to the RGB reference grid when
// its source dimensions differ, matching Compose's reference-grid semantics.
func ApplyDedicatedLToRGBA(buf []byte, l []float32, lw, lh, width, height int, cfg LRGBConfig) ([]byte, error) {
	if width <= 0 || height <= 0 || len(buf) < width*height*4 || lw <= 0 || lh <= 0 || len(l) < lw*lh {
		return nil, fmt.Errorf("invalid dedicated luminance dimensions")
	}
	if lw != width || lh != height {
		l = ResizeChannel(l, lw, lh, width, height)
	}
	r := make([]float32, width*height)
	g := make([]float32, width*height)
	b := make([]float32, width*height)
	for i := range r {
		r[i] = float32(buf[i*4]) / 255
		g[i] = float32(buf[i*4+1]) / 255
		b[i] = float32(buf[i*4+2]) / 255
	}
	cfg.UseDedicatedLuminance = true
	planes, err := ComposeLRGB(context.Background(), r, g, b, l, width, height, cfg)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), buf...)
	n := width * height
	for i := 0; i < n; i++ {
		out[i*4] = uint8(clampLRGB(float64(planes[i])*255) + .5)
		out[i*4+1] = uint8(clampLRGB(float64(planes[n+i])*255) + .5)
		out[i*4+2] = uint8(clampLRGB(float64(planes[2*n+i])*255) + .5)
	}
	return out, nil
}

func clampLRGB(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// LRGBConfig controls the optional LRGB combination. Inputs and output are
// normalized float pixels in [0,1]. LuminanceWeight=0 preserves RGB output.
type LRGBConfig struct {
	LuminanceWeight       float64
	ChrominanceSmoothing  float64
	SyntheticWeights      [3]float64
	UseDedicatedLuminance bool
}

// ComposeLRGB combines RGB chrominance with a dedicated or synthetic
// luminance. Chrominance smoothing is performed before luminance injection and
// therefore does not soften the luminance detail.
func ComposeLRGB(ctx context.Context, r, g, b, l []float32, width, height int, cfg LRGBConfig) ([]float32, error) {
	n := width * height
	if width <= 0 || height <= 0 || len(r) < n || len(g) < n || len(b) < n {
		return nil, fmt.Errorf("invalid LRGB dimensions")
	}
	if cfg.LuminanceWeight < 0 {
		cfg.LuminanceWeight = 0
	}
	if cfg.LuminanceWeight > 1 {
		cfg.LuminanceWeight = 1
	}
	if cfg.SyntheticWeights == [3]float64{} {
		cfg.SyntheticWeights = [3]float64{0.2126, 0.7152, 0.0722}
	}
	// Synthetic luminance describes the source filters, not their display
	// smoothing. Keep the original planes for that measurement while allowing
	// the working planes below to provide softened chroma.
	lumR, lumG, lumB := r, g, b
	if cfg.ChrominanceSmoothing > 0 {
		r = blurChroma(r, width, height, cfg.ChrominanceSmoothing)
		g = blurChroma(g, width, height, cfg.ChrominanceSmoothing)
		b = blurChroma(b, width, height, cfg.ChrominanceSmoothing)
	}
	if cfg.LuminanceWeight == 0 {
		out := make([]float32, 3*n)
		copy(out, r)
		copy(out[n:], g)
		copy(out[2*n:], b)
		return out, nil
	}
	if cfg.UseDedicatedLuminance && len(l) < n {
		return nil, fmt.Errorf("dedicated luminance channel is missing")
	}
	out := make([]float32, 3*n)
	for i := 0; i < n; i++ {
		if i&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		lum := float64(lumR[i])*cfg.SyntheticWeights[0] + float64(lumG[i])*cfg.SyntheticWeights[1] + float64(lumB[i])*cfg.SyntheticWeights[2]
		if cfg.UseDedicatedLuminance {
			lum = float64(l[i])
		}
		if lum < 0 {
			lum = 0
		}
		if lum > 1 {
			lum = 1
		}
		// Preserve each channel's chroma ratio relative to synthetic RGB luma.
		// Compare the selected synthetic/dedicated luminance with the RGB
		// luminance of the source. Using the synthetic weights for both sides
		// would make every synthetic recipe a no-op.
		base := .2126*float64(r[i]) + .7152*float64(g[i]) + .0722*float64(b[i])
		if base < 1e-6 {
			base = 1e-6
		}
		factor := 1 + cfg.LuminanceWeight*(lum/base-1)
		for c, v := range []float32{r[i], g[i], b[i]} {
			x := float64(v) * factor
			if x < 0 {
				x = 0
			}
			if x > 1 {
				x = 1
			}
			out[c*n+i] = float32(x)
		}
	}
	return out, nil
}

func blurChroma(src []float32, w, h int, sigma float64) []float32 {
	r := int(sigma*3 + 0.5)
	if r < 1 {
		return src
	}
	out := make([]float32, len(src))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum, ws float64
			for j := maxInt(0, y-r); j <= minInt(h-1, y+r); j++ {
				for i := maxInt(0, x-r); i <= minInt(w-1, x+r); i++ {
					dx, dy := float64(i-x), float64(j-y)
					q := math.Exp(-(dx*dx + dy*dy) / (2 * sigma * sigma))
					sum += q * float64(src[j*w+i])
					ws += q
				}
			}
			if ws > 0 {
				out[y*w+x] = float32(sum / ws)
			}
		}
	}
	return out
}
