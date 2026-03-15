package render

import "gofitsv3/internal/stretch"

// ComposeRGB combines three normalized grayscale images into RGBA buffer.
func ComposeRGB(r, g, b []float64, width, height int, modeR, modeG, modeB stretch.Mode) []byte {
	total := width * height
	buf := make([]byte, total*4)
	for i := 0; i < total; i++ {
		pr := apply(r, i, modeR)
		pg := apply(g, i, modeG)
		pb := apply(b, i, modeB)
		buf[i*4] = toByte(pr)
		buf[i*4+1] = toByte(pg)
		buf[i*4+2] = toByte(pb)
		buf[i*4+3] = 255
	}
	return buf
}

func apply(arr []float64, idx int, mode stretch.Mode) float64 {
	if idx >= len(arr) {
		return 0
	}
	// mode applied beforehand; just clamp
	v := arr[idx]
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func toByte(v float64) byte {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return byte(v * 255)
}
