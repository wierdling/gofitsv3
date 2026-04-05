package stretch

import "math"

type Mode int

const (
	Linear Mode = iota
	Log
	Asinh
	Sqrt
	HistEq
)

func Apply(pixels []float32, mode Mode) []float32 {
	out := make([]float32, len(pixels))
	switch mode {
	case Linear:
		copy(out, pixels)
	case Log:
		for i, v := range pixels {
			out[i] = float32(math.Log1p(9*float64(v)) / math.Log1p(9))
		}
	case Asinh:
		for i, v := range pixels {
			out[i] = float32(math.Asinh(10*float64(v)) / math.Asinh(10))
		}
	case Sqrt:
		for i, v := range pixels {
			out[i] = float32(math.Sqrt(float64(v)))
		}
	case HistEq:
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
		cdf := make([]float32, 256)
		total := float64(len(pixels))
		sum := 0
		for i, h := range hist {
			sum += h
			cdf[i] = float32(float64(sum) / total)
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
