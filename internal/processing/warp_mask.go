package processing

import "math"

func WarpImageToSizeWithMask(targetPixels []float32, srcWidth, srcHeight, outWidth, outHeight int, t AffineTransform) ([]float32, []bool) {
	n, ok := rasterSize(outWidth, outHeight)
	if !ok {
		return []float32{}, []bool{}
	}
	out := make([]float32, n)
	valid := make([]bool, n)
	if srcWidth <= 0 || srcHeight <= 0 || len(targetPixels) == 0 {
		return out, valid
	}
	maxHeight := len(targetPixels) / srcWidth
	if maxHeight <= 0 {
		return out, valid
	}
	if srcHeight > maxHeight {
		srcHeight = maxHeight
	}
	for y := 0; y < outHeight; y++ {
		for x := 0; x < outWidth; x++ {
			srcX := t.A*float64(x) + t.B*float64(y) + t.C
			srcY := t.D*float64(x) + t.E*float64(y) + t.F
			outIdx := y*outWidth + x
			if !isFinite64(srcX) || !isFinite64(srcY) || srcX < 0 || srcY < 0 || srcX > float64(srcWidth-1) || srcY > float64(srcHeight-1) {
				out[outIdx] = float32(math.NaN())
				continue
			}
			x0 := int(math.Floor(srcX))
			y0 := int(math.Floor(srcY))
			x1 := x0 + 1
			y1 := y0 + 1
			if x1 >= srcWidth {
				x1 = srcWidth - 1
			}
			if y1 >= srcHeight {
				y1 = srcHeight - 1
			}
			wx := srcX - float64(x0)
			wy := srcY - float64(y0)
			p00 := float64(targetPixels[y0*srcWidth+x0])
			p10 := float64(targetPixels[y0*srcWidth+x1])
			p01 := float64(targetPixels[y1*srcWidth+x0])
			p11 := float64(targetPixels[y1*srcWidth+x1])
			if !isFinite64(p00) || !isFinite64(p10) || !isFinite64(p01) || !isFinite64(p11) {
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
