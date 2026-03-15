package stretch

import "math"

// Mode defines the stretch function.
type Mode int

const (
	Linear Mode = iota
	Log
	Asinh
	Sqrt
	HistEq
)

// Apply applies the stretch to normalized input [0..1].
func Apply(pixels []float64, mode Mode) []float64 {
	out := make([]float64, len(pixels))
	switch mode {
	case Linear:
		copy(out, pixels)
	case Log:
		for i, v := range pixels {
			out[i] = math.Log1p(9*v) / math.Log1p(9)
		}
	case Asinh:
		for i, v := range pixels {
			out[i] = math.Asinh(10*v) / math.Asinh(10)
		}
	case Sqrt:
		for i, v := range pixels {
			out[i] = math.Sqrt(v)
		}
	case HistEq:
		// Simple cumulative distribution equalization.
		hist := make([]int, 256)
		for _, v := range pixels {
			idx := int(v * 255)
			if idx < 0 {
				idx = 0
			}
			if idx > 255 {
				idx = 255
			}
			hist[idx]++
		}
		cdf := make([]float64, 256)
		total := float64(len(pixels))
		sum := 0
		for i, h := range hist {
			sum += h
			cdf[i] = float64(sum) / total
		}
		for i, v := range pixels {
			idx := int(v * 255)
			if idx < 0 {
				idx = 0
			}
			if idx > 255 {
				idx = 255
			}
			out[i] = cdf[idx]
		}
	}
	return out
}
