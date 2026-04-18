package stretch

import (
	"math"
	"runtime"
	"sync"
)

type Mode int

const (
	Linear Mode = iota
	Log
	Asinh
	Sqrt
	HistEq
)

func parallel(pixels, out []float32, fn func(i int, v float32) float32) {
	n := runtime.NumCPU()
	chunk := (len(pixels) + n - 1) / n
	var wg sync.WaitGroup
	for g := 0; g < n; g++ {
		start := g * chunk
		if start >= len(pixels) {
			break
		}
		end := start + chunk
		if end > len(pixels) {
			end = len(pixels)
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				out[i] = fn(i, pixels[i])
			}
		}(start, end)
	}
	wg.Wait()
}

func Apply(pixels []float32, mode Mode) []float32 {
	out := make([]float32, len(pixels))
	switch mode {
	case Linear:
		copy(out, pixels)
	case Log:
		scale := math.Log1p(9)
		parallel(pixels, out, func(_ int, v float32) float32 {
			return float32(math.Log1p(9*float64(v)) / scale)
		})
	case Asinh:
		scale := math.Asinh(10)
		parallel(pixels, out, func(_ int, v float32) float32 {
			return float32(math.Asinh(10*float64(v)) / scale)
		})
	case Sqrt:
		parallel(pixels, out, func(_ int, v float32) float32 {
			return float32(math.Sqrt(float64(v)))
		})
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
