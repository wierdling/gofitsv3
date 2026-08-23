package badpix

import (
	"fmt"
	"gofitsv3/internal/fitsio"
	"sort"
)

func MaskFromDQ(sci fitsio.HDU, dq fitsio.HDU, badBits uint32) ([]bool, error) {
	if sci.Data.Width != dq.Data.Width || sci.Data.Height != dq.Data.Height {
		return nil, fmt.Errorf("dimension mismatch: sci %dx%d vs dq %dx%d", sci.Data.Width, sci.Data.Height, dq.Data.Width, dq.Data.Height)
	}
	if sci.Data.Width <= 0 || sci.Data.Height <= 0 {
		return nil, fmt.Errorf("invalid image dimensions: %dx%d", sci.Data.Width, sci.Data.Height)
	}
	if sci.Data.Width > int(^uint(0)>>1)/sci.Data.Height {
		return nil, fmt.Errorf("image dimensions overflow: %dx%d", sci.Data.Width, sci.Data.Height)
	}
	total := sci.Data.Width * sci.Data.Height
	if len(sci.Data.Pixels) != total {
		return nil, fmt.Errorf("malformed SCI pixel count %d, want %d", len(sci.Data.Pixels), total)
	}
	var dqValues []int32
	if len(dq.Data.Int32Pixels) == total {
		dqValues = dq.Data.Int32Pixels
	} else if len(dq.Data.Pixels) == total {
		dqValues = make([]int32, total)
		for i, v := range dq.Data.Pixels {
			dqValues[i] = int32(v)
		}
	} else {
		return nil, fmt.Errorf("malformed DQ pixel count: int32=%d float=%d, want %d", len(dq.Data.Int32Pixels), len(dq.Data.Pixels), total)
	}
	mask := make([]bool, total)
	useBits := badBits != 0
	for i := 0; i < total; i++ {
		bits := uint32(dqValues[i])
		if (!useBits && bits != 0) || (useBits && (bits&badBits) != 0) {
			mask[i] = true
		}
	}
	return mask, nil
}

func RepairMaskedPixels(img fitsio.ImageData, mask []bool) fitsio.ImageData {
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
	w, h := img.Width, img.Height
	total := w * h

	if w <= 0 || h <= 0 || len(img.Pixels) < total {
		return 0
	}

	x = mirror(x, w)
	y = mirror(y, h)

	idx := y*w + x

	// If the mask is missing or malformed, fall back to the original pixel.
	if len(mask) < total {
		return img.Pixels[idx]
	}

	// Convention: mask[idx] == true means bad pixel.
	// If the pixel is not masked, keep it.
	if !mask[idx] {
		return img.Pixels[idx]
	}

	// First try a local bicubic surface fit.
	if v, ok := localBicubicFit(img, mask, x, y, 4); ok {
		return float32(v)
	}

	// If there are not enough valid pixels for the bicubic fit,
	// fall back to a robust weighted median.
	if v, ok := weightedMedianFill(img, mask, x, y, 8); ok {
		return float32(v)
	}

	// Last resort: leave the original value.
	return img.Pixels[idx]
}

func localBicubicFit(img fitsio.ImageData, mask []bool, x, y, radius int) (float64, bool) {
	const terms = 16

	w, h := img.Width, img.Height
	total := w * h

	var ata [terms][terms]float64
	var atb [terms]float64

	sampleCount := 0
	haveRange := false

	var minVal float64
	var maxVal float64

	radiusF := float64(radius)

	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			nx := mirror(x+dx, w)
			ny := mirror(y+dy, h)
			nidx := ny*w + nx

			if nidx < 0 || nidx >= total {
				continue
			}

			// Never use masked pixels as input to the fit.
			if mask[nidx] {
				continue
			}

			v := float64(img.Pixels[nidx])

			if !haveRange {
				minVal = v
				maxVal = v
				haveRange = true
			} else {
				if v < minVal {
					minVal = v
				}
				if v > maxVal {
					maxVal = v
				}
			}

			// Normalize local coordinates to roughly [-1, 1].
			// This keeps the least-squares system better conditioned.
			lx := float64(dx) / radiusF
			ly := float64(dy) / radiusF

			basis := bicubicBasis(lx, ly)

			// Weight nearby pixels more heavily than distant pixels.
			d2 := float64(dx*dx + dy*dy)
			weight := 1.0 / (1.0 + d2)

			for i := 0; i < terms; i++ {
				atb[i] += weight * basis[i] * v

				for j := 0; j < terms; j++ {
					ata[i][j] += weight * basis[i] * basis[j]
				}
			}

			sampleCount++
		}
	}

	// A bicubic surface has 16 terms.
	// Require more than 16 samples so the fit is not too fragile.
	if sampleCount < 24 || !haveRange {
		return 0, false
	}

	// Small ridge term to reduce instability when the local data is nearly flat
	// or poorly distributed.
	for i := 0; i < terms; i++ {
		ata[i][i] += 1e-8
	}

	coeff, ok := solve16(ata, atb)
	if !ok {
		return 0, false
	}

	// At the target pixel, local x = 0 and local y = 0.
	// For this basis, coeff[0] is the fitted value at the center.
	result := coeff[0]

	// Avoid NaN.
	if result != result {
		return 0, false
	}

	// Prevent cubic overshoot/ringing.
	if result < minVal {
		result = minVal
	}
	if result > maxVal {
		result = maxVal
	}

	return result, true
}

func bicubicBasis(x, y float64) [16]float64 {
	x2 := x * x
	x3 := x2 * x

	y2 := y * y
	y3 := y2 * y

	return [16]float64{
		1,
		x,
		x2,
		x3,

		y,
		x * y,
		x2 * y,
		x3 * y,

		y2,
		x * y2,
		x2 * y2,
		x3 * y2,

		y3,
		x * y3,
		x2 * y3,
		x3 * y3,
	}
}

func weightedMedianFill(img fitsio.ImageData, mask []bool, x, y, radius int) (float64, bool) {
	w, h := img.Width, img.Height
	total := w * h

	type weightedValue struct {
		value  float64
		weight float64
	}

	values := make([]weightedValue, 0, (2*radius+1)*(2*radius+1))

	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}

			nx := mirror(x+dx, w)
			ny := mirror(y+dy, h)
			nidx := ny*w + nx

			if nidx < 0 || nidx >= total {
				continue
			}

			if mask[nidx] {
				continue
			}

			d2 := float64(dx*dx + dy*dy)
			weight := 1.0 / (1.0 + d2)

			values = append(values, weightedValue{
				value:  float64(img.Pixels[nidx]),
				weight: weight,
			})
		}
	}

	if len(values) == 0 {
		return 0, false
	}

	sort.Slice(values, func(i, j int) bool {
		return values[i].value < values[j].value
	})

	totalWeight := 0.0
	for _, v := range values {
		totalWeight += v.weight
	}

	half := totalWeight * 0.5
	accum := 0.0

	for _, v := range values {
		accum += v.weight
		if accum >= half {
			return v.value, true
		}
	}

	return values[len(values)-1].value, true
}

func solve16(a [16][16]float64, b [16]float64) ([16]float64, bool) {
	const n = 16

	var m [n][n + 1]float64
	var result [n]float64

	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			m[r][c] = a[r][c]
		}
		m[r][n] = b[r]
	}

	for col := 0; col < n; col++ {
		pivot := col
		best := absFloat(m[col][col])

		for r := col + 1; r < n; r++ {
			v := absFloat(m[r][col])
			if v > best {
				best = v
				pivot = r
			}
		}

		if best < 1e-12 {
			return result, false
		}

		if pivot != col {
			m[col], m[pivot] = m[pivot], m[col]
		}

		pivotValue := m[col][col]

		for c := col; c <= n; c++ {
			m[col][c] /= pivotValue
		}

		for r := 0; r < n; r++ {
			if r == col {
				continue
			}

			factor := m[r][col]
			if factor == 0 {
				continue
			}

			for c := col; c <= n; c++ {
				m[r][c] -= factor * m[col][c]
			}
		}
	}

	for i := 0; i < n; i++ {
		result[i] = m[i][n]
	}

	return result, true
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
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
