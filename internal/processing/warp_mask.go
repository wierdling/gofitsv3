package processing

import "math"

func WarpImageToSizeWithMask(targetPixels []float32, srcWidth, srcHeight, outWidth, outHeight int, t AffineTransform) ([]float32, []bool) {
	out := make([]float32, outWidth*outHeight)
	valid := make([]bool, outWidth*outHeight)
	for y := 0; y < outHeight; y++ {
		for x := 0; x < outWidth; x++ {
			srcX := t.A*float64(x) + t.B*float64(y) + t.C
			srcY := t.D*float64(x) + t.E*float64(y) + t.F
			outIdx := y*outWidth + x
			x0 := int(math.Floor(srcX))
			y0 := int(math.Floor(srcY))
			x1 := x0 + 1
			y1 := y0 + 1
			if x0 < 0 || x1 >= srcWidth || y0 < 0 || y1 >= srcHeight {
				out[outIdx] = float32(math.NaN())
				continue
			}
			wx := srcX - float64(x0)
			wy := srcY - float64(y0)
			p00 := float64(targetPixels[y0*srcWidth+x0])
			p10 := float64(targetPixels[y0*srcWidth+x1])
			p01 := float64(targetPixels[y1*srcWidth+x0])
			p11 := float64(targetPixels[y1*srcWidth+x1])
			if math.IsNaN(p00) || math.IsNaN(p10) || math.IsNaN(p01) || math.IsNaN(p11) {
				out[outIdx] = float32(math.NaN())
				continue
			}
			val := p00*(1-wx)*(1-wy) + p10*wx*(1-wy) + p01*(1-wx)*wy + p11*wx*wy
			out[outIdx] = float32(val)
			valid[outIdx] = true
		}
	}
	return out, valid
}
