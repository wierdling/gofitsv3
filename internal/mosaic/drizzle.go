package mosaic

import "math"

// DrizzleParams holds parameters for drizzle resampling.
type DrizzleParams struct {
	Scale float64 // output pixel scale factor
}

// Drizzle aligns and resamples source pixels into target canvas.
// Stub implementation: nearest-neighbor with scale factor.
func Drizzle(src []float64, width, height int, params DrizzleParams) ([]float64, int, int) {
	if params.Scale <= 0 {
		params.Scale = 1
	}
	outW := int(math.Ceil(float64(width) * params.Scale))
	outH := int(math.Ceil(float64(height) * params.Scale))
	out := make([]float64, outW*outH)
	for y := 0; y < outH; y++ {
		for x := 0; x < outW; x++ {
			sx := int(float64(x) / params.Scale)
			sy := int(float64(y) / params.Scale)
			if sx >= width {
				sx = width - 1
			}
			if sy >= height {
				sy = height - 1
			}
			out[y*outW+x] = src[sy*width+sx]
		}
	}
	return out, outW, outH
}
