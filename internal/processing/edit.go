package processing

import (
	"image"
	"math"
)

// SharpenRGBA applies an unsharp-mask sharpening to an RGBA image.
// strength (0–3) controls how much the detail is amplified.
// radius (0.5–10) is the Gaussian blur sigma; larger values sharpen coarser detail.
func SharpenRGBA(src *image.RGBA, strength, radius float64) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	if strength <= 0 || radius <= 0 {
		return dst
	}

	blurred := gaussianBlurRGBA(src, radius)
	w := src.Bounds().Dx()
	h := src.Bounds().Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				idx := y*src.Stride + x*4 + c
				orig := float64(src.Pix[idx])
				blur := float64(blurred.Pix[idx])
				v := orig + strength*(orig-blur)
				dst.Pix[idx] = clampByte(v)
			}
		}
	}
	return dst
}

// gaussianBlurRGBA applies a separable Gaussian blur with the given sigma.
func gaussianBlurRGBA(src *image.RGBA, sigma float64) *image.RGBA {
	kernel, sum := gaussianKernel(sigma)
	w := src.Bounds().Dx()
	h := src.Bounds().Dy()
	tmp := image.NewRGBA(src.Bounds())
	dst := image.NewRGBA(src.Bounds())

	// Horizontal pass
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				var acc float64
				for ki, kv := range kernel {
					sx := x + ki - len(kernel)/2
					if sx < 0 {
						sx = 0
					} else if sx >= w {
						sx = w - 1
					}
					acc += float64(src.Pix[y*src.Stride+sx*4+c]) * kv
				}
				tmp.Pix[y*tmp.Stride+x*4+c] = clampByte(acc / sum)
			}
			tmp.Pix[y*tmp.Stride+x*4+3] = src.Pix[y*src.Stride+x*4+3]
		}
	}

	// Vertical pass
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				var acc float64
				for ki, kv := range kernel {
					sy := y + ki - len(kernel)/2
					if sy < 0 {
						sy = 0
					} else if sy >= h {
						sy = h - 1
					}
					acc += float64(tmp.Pix[sy*tmp.Stride+x*4+c]) * kv
				}
				dst.Pix[y*dst.Stride+x*4+c] = clampByte(acc / sum)
			}
			dst.Pix[y*dst.Stride+x*4+3] = src.Pix[y*src.Stride+x*4+3]
		}
	}
	return dst
}

// gaussianKernel builds a 1-D Gaussian kernel for the given sigma.
// Returns the kernel values and their sum.
func gaussianKernel(sigma float64) ([]float64, float64) {
	radius := int(math.Ceil(sigma * 3))
	size := 2*radius + 1
	kernel := make([]float64, size)
	var sum float64
	for i := 0; i < size; i++ {
		x := float64(i - radius)
		v := math.Exp(-(x * x) / (2 * sigma * sigma))
		kernel[i] = v
		sum += v
	}
	return kernel, sum
}

// ApplyGammaRGBA applies per-channel gamma (midtone) correction to an RGBA image.
// gamma > 1 brightens midtones; gamma < 1 darkens them.
func ApplyGammaRGBA(src *image.RGBA, rGamma, gGamma, bGamma float64) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)

	safeGamma := func(g float64) float64 {
		if g <= 0 {
			return 0.001
		}
		return g
	}
	gammas := [3]float64{safeGamma(rGamma), safeGamma(gGamma), safeGamma(bGamma)}

	var luts [3][256]byte
	for c := 0; c < 3; c++ {
		for i := 0; i < 256; i++ {
			v := math.Pow(float64(i)/255.0, 1.0/gammas[c]) * 255.0
			luts[c][i] = clampByte(v)
		}
	}
	for i := 0; i+3 < len(src.Pix); i += 4 {
		dst.Pix[i] = luts[0][src.Pix[i]]
		dst.Pix[i+1] = luts[1][src.Pix[i+1]]
		dst.Pix[i+2] = luts[2][src.Pix[i+2]]
	}
	return dst
}

// ApplyCurvesRGBA applies per-channel tone curves via lookup tables.
// Each LUT maps an input byte (0–255) to an output byte.
func ApplyCurvesRGBA(src *image.RGBA, rLUT, gLUT, bLUT [256]byte) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	for i := 0; i+3 < len(src.Pix); i += 4 {
		dst.Pix[i] = rLUT[src.Pix[i]]
		dst.Pix[i+1] = gLUT[src.Pix[i+1]]
		dst.Pix[i+2] = bLUT[src.Pix[i+2]]
	}
	return dst
}

// ApplyEditLevels adjusts per-channel input levels on an RGBA image.
// min/max values are in the 0–255 range.
func ApplyEditLevels(src *image.RGBA, rMin, rMax, gMin, gMax, bMin, bMax float64) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)

	type mm struct{ min, max float64 }
	ch := [3]mm{{rMin, rMax}, {gMin, gMax}, {bMin, bMax}}
	for i := 0; i+3 < len(src.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			rng := ch[c].max - ch[c].min
			if rng <= 0 {
				rng = 1
			}
			v := (float64(src.Pix[i+c]) - ch[c].min) / rng * 255.0
			dst.Pix[i+c] = clampByte(v)
		}
	}
	return dst
}
