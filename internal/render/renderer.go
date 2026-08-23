package render

import (
	"math"

	"gofitsv3/internal/stretch"
)

func ComposeRGB(r, g, b []float32, width, height int, modeR, modeG, modeB stretch.Mode) []byte {
	if width <= 0 || height <= 0 || width > int(^uint(0)>>1)/height {
		return nil
	}
	total := width * height
	if total > int(^uint(0)>>1)/4 {
		return nil
	}
	buf := make([]byte, total*4)
	for i := 0; i < total; i++ {
		pr := apply(r, i)
		pg := apply(g, i)
		pb := apply(b, i)
		buf[i*4] = toByte(pr)
		buf[i*4+1] = toByte(pg)
		buf[i*4+2] = toByte(pb)
		buf[i*4+3] = 255
	}
	return buf
}

func apply(arr []float32, idx int) float32 {
	if idx < 0 || idx >= len(arr) {
		return 0
	}
	v := arr[idx]
	if math.IsNaN(float64(v)) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func toByte(v float32) byte {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return byte(v * 255)
}
