package badpix

import (
	"fmt"

	"gofitsv3/internal/fitsio"
)

func MaskFromDQ(sci fitsio.HDU, dq fitsio.HDU, badBits uint16) ([]bool, error) {
	if sci.Data.Width != dq.Data.Width || sci.Data.Height != dq.Data.Height {
		return nil, fmt.Errorf("dimension mismatch: sci %dx%d vs dq %dx%d", sci.Data.Width, sci.Data.Height, dq.Data.Width, dq.Data.Height)
	}
	total := len(sci.Data.Pixels)
	mask := make([]bool, total)
	useBits := badBits != 0
	for i := 0; i < total; i++ {
		v := dq.Data.Pixels[i]
		bits := uint32(int64(v))
		if (!useBits && bits != 0) || (useBits && (bits&uint32(badBits)) != 0) {
			mask[i] = true
		}
	}
	return mask, nil
}

func InterpolateBicubic(img fitsio.ImageData, mask []bool) fitsio.ImageData {
	if len(mask) != len(img.Pixels) {
		return img
	}
	w, h := img.Width, img.Height
	out := make([]float32, len(img.Pixels))
	copy(out, img.Pixels)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			if !mask[idx] {
				continue
			}
			out[idx] = bicubicAt(img, mask, x, y)
		}
	}
	return fitsio.ImageData{Width: w, Height: h, Pixels: out}
}

func bicubicAt(img fitsio.ImageData, mask []bool, x, y int) float32 {
	fx, fy := 0.5, 0.5
	row := [4]float64{}
	for j := -1; j <= 2; j++ {
		p0 := sample(img, mask, x-1, y+j)
		p1 := sample(img, mask, x, y+j)
		p2 := sample(img, mask, x+1, y+j)
		p3 := sample(img, mask, x+2, y+j)
		row[j+1] = cubic(p0, p1, p2, p3, fx)
	}
	return float32(cubic(row[0], row[1], row[2], row[3], fy))
}

func sample(img fitsio.ImageData, mask []bool, x, y int) float64 {
	w, h := img.Width, img.Height
	x = mirror(x, w)
	y = mirror(y, h)
	idx := y*w + x
	if !mask[idx] {
		return float64(img.Pixels[idx])
	}
	for r := 1; r <= 5; r++ {
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				nx := mirror(x+dx, w)
				ny := mirror(y+dy, h)
				nidx := ny*w + nx
				if !mask[nidx] {
					return float64(img.Pixels[nidx])
				}
			}
		}
	}
	return float64(img.Pixels[idx])
}

func mirror(v, max int) int {
	if max == 0 {
		return 0
	}
	for v < 0 || v >= max {
		if v < 0 {
			v = -v - 1
		} else {
			v = 2*max - v - 1
		}
	}
	return v
}

func cubic(p0, p1, p2, p3, t float64) float64 {
	a := -0.5*p0 + 1.5*p1 - 1.5*p2 + 0.5*p3
	b := p0 - 2.5*p1 + 2*p2 - 0.5*p3
	c := -0.5*p0 + 0.5*p2
	d := p1
	return ((a*t+b)*t+c)*t + d
}
