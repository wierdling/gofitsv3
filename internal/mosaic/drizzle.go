package mosaic

import "math"

type DrizzleParams struct {
	Scale float64
}

func Drizzle(src []float32, width, height int, params DrizzleParams) ([]float32, int, int) {
	if params.Scale <= 0 {
		params.Scale = 1
	}
	outW := int(math.Ceil(float64(width) * params.Scale))
	outH := int(math.Ceil(float64(height) * params.Scale))
	out := make([]float32, outW*outH)
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
