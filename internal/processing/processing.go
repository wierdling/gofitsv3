package processing

import (
	"image"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/render"
	"gofitsv3/internal/stretch"
	"gofitsv3/internal/utils"
)

func ApplyStretchParallel(img *models.LoadedImage) (fitsio.ImageData, []byte) {
	data := img.HDU.Data
	numPixels := len(data.Pixels)
	pixels := make([]float32, numPixels)
	var mask []byte
	if img.ShowClip {
		mask = make([]byte, numPixels)
	}

	denom := img.Peak - img.Background
	if denom <= 0 {
		denom = 1
	}
	if img.ScaledPeak <= 0 {
		img.ScaledPeak = 1
	}
	stretchMul := img.ScaledPeak / denom
	invLogPeak := 1.0 / math.Log1p(img.ScaledPeak)
	invAsinhPeak := 1.0 / math.Asinh(img.ScaledPeak)
	invSqrtPeak := 1.0 / math.Sqrt(img.ScaledPeak)
	invLinearPeak := 1.0 / img.ScaledPeak

	numWorkers := runtime.NumCPU()
	chunkSize := (numPixels + numWorkers - 1) / numWorkers
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= numPixels {
			break
		}
		if end > numPixels {
			end = numPixels
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				v := float64(data.Pixels[i])
				if math.IsNaN(v) {
					if mask != nil {
						mask[i] = 3
					}
					pixels[i] = 0
					continue
				}
				if v < img.Black {
					if mask != nil {
						mask[i] = 1
					}
					v = img.Black
				} else if v > img.White {
					if mask != nil {
						mask[i] = 2
					}
					v = img.White
				}
				val := (v - img.Background) * stretchMul
				if val < 0 {
					val = 0
				}
				result := val * invLinearPeak
				switch img.Mode {
				case stretch.Log:
					result = math.Log1p(val) * invLogPeak
				case stretch.Asinh:
					result = math.Asinh(val) * invAsinhPeak
				case stretch.Sqrt:
					result = math.Sqrt(val) * invSqrtPeak
				}
				if result > 1.0 {
					result = 1.0
				}
				pixels[i] = float32(result)
			}
		}(start, end)
	}
	wg.Wait()

	if img.Mode == stretch.HistEq {
		pixels = stretch.Apply(pixels, img.Mode)
	}

	return fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: pixels}, mask
}

func ComposeRGB(imgs []*models.LoadedImage) ([]byte, int, int, [3]histogram.Stats) {
	if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
		return nil, 0, 0, [3]histogram.Stats{}
	}

	ref := imgs[1]
	w := ref.HDU.Data.Width
	h := ref.HDU.Data.Height

	rData := stretchForReferenceGrid(imgs[2], ref)
	gData := stretchForReferenceGrid(ref, ref)
	bData := stretchForReferenceGrid(imgs[0], ref)

	buf := render.ComposeRGB(rData.Pixels, gData.Pixels, bData.Pixels, w, h, imgs[2].Mode, imgs[1].Mode, imgs[0].Mode)
	return buf, w, h, HistogramRGB(buf)
}

func stretchForReferenceGrid(img, ref *models.LoadedImage) fitsio.ImageData {
	if img == nil || ref == nil {
		return fitsio.ImageData{}
	}
	if img == ref {
		data, _ := ApplyStretchParallel(img)
		return data
	}

	alignedPixels, _, err := AlignChannelUsingWCS(
		img.HDU.Data.Pixels,
		img.HDU.Data.Width,
		img.HDU.Data.Height,
		img.HDU.Header,
		ref.HDU.Data.Pixels,
		ref.HDU.Data.Width,
		ref.HDU.Data.Height,
		ref.HDU.Header,
	)
	if err == nil {
		clone := *img
		clone.HDU = img.HDU
		clone.HDU.Data = fitsio.ImageData{Width: ref.HDU.Data.Width, Height: ref.HDU.Data.Height, Pixels: alignedPixels}
		data, _ := ApplyStretchParallel(&clone)
		return data
	}

	if img.HDU.Data.Width != ref.HDU.Data.Width || img.HDU.Data.Height != ref.HDU.Data.Height {
		resized := ResizeChannel(img.HDU.Data.Pixels, img.HDU.Data.Width, img.HDU.Data.Height, ref.HDU.Data.Width, ref.HDU.Data.Height)
		clone := *img
		clone.HDU = img.HDU
		clone.HDU.Data = fitsio.ImageData{Width: ref.HDU.Data.Width, Height: ref.HDU.Data.Height, Pixels: resized}
		data, _ := ApplyStretchParallel(&clone)
		return data
	}

	data, _ := ApplyStretchParallel(img)
	return data
}

func ApplyRGBLevels(buf []byte, levels *models.RgbLevels) []byte {
	if buf == nil || levels == nil {
		return buf
	}
	out := make([]byte, len(buf))
	for i := 0; i+3 < len(buf); i += 4 {
		for c := 0; c < 3; c++ {
			val := float64(buf[i+c])
			minV := levels.Min[c]
			maxV := levels.Max[c]
			if maxV <= minV {
				out[i+c] = clampByte(maxV)
				continue
			}
			if val < minV {
				val = minV
			}
			if val > maxV {
				val = maxV
			}
			scaled := (val - minV) / (maxV - minV) * 255
			out[i+c] = clampByte(scaled)
		}
		out[i+3] = 255
	}
	return out
}

func HistogramRGB(buf []byte) [3]histogram.Stats {
	var stats [3]histogram.Stats
	if len(buf) == 0 {
		return stats
	}
	var sums [3]float64
	var counts int
	for i := 0; i+3 < len(buf); i += 4 {
		r := int(buf[i])
		g := int(buf[i+1])
		b := int(buf[i+2])
		stats[0].Hist[r]++
		stats[1].Hist[g]++
		stats[2].Hist[b]++
		sums[0] += float64(r)
		sums[1] += float64(g)
		sums[2] += float64(b)
		counts++
	}
	for c := 0; c < 3; c++ {
		stats[c].Count = counts
		stats[c].Min = 0
		stats[c].Max = 255
		if counts > 0 {
			stats[c].Mean = sums[c] / float64(counts)
		}
	}
	var variances [3]float64
	for i := 0; i+3 < len(buf); i += 4 {
		diffR := float64(buf[i]) - stats[0].Mean
		diffG := float64(buf[i+1]) - stats[1].Mean
		diffB := float64(buf[i+2]) - stats[2].Mean
		variances[0] += diffR * diffR
		variances[1] += diffG * diffG
		variances[2] += diffB * diffB
	}
	for c := 0; c < 3; c++ {
		if counts > 0 {
			stats[c].Std = math.Sqrt(variances[c] / float64(counts))
		}
	}
	return stats
}

func AutoLevels(pixels []float32) (float64, float64) {
	if len(pixels) == 0 {
		return 0, 1
	}
	var min, max float64
	initialized := false
	for _, v := range pixels {
		fv := float64(v)
		if math.IsNaN(fv) || math.IsInf(fv, 0) {
			continue
		}
		if !initialized {
			min, max = fv, fv
			initialized = true
		} else {
			if fv < min {
				min = fv
			}
			if fv > max {
				max = fv
			}
		}
	}
	if !initialized {
		return 0, 1
	}
	if min == max {
		max = min + 1
	}
	return min, max
}

func ToGrayRGBA(data fitsio.ImageData, mask []byte) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, data.Width, data.Height))
	for i, v := range data.Pixels {
		idx := i * 4
		switch mask[i] {
		case 1:
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = 0, 0, 255
		case 2:
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = 0, 255, 0
		case 3:
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = 255, 0, 0
		default:
			b := byte(utils.Clamp01(float64(v)) * 255)
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = b, b, b
		}
		img.Pix[idx+3] = 255
	}
	return img
}

func FlipImageData(data fitsio.ImageData) fitsio.ImageData {
	w, h := data.Width, data.Height
	out := make([]float32, len(data.Pixels))
	maxIdx := len(data.Pixels)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			srcIdx := (h-1-y)*w + x
			dstIdx := y*w + x
			if srcIdx >= 0 && srcIdx < maxIdx && dstIdx >= 0 && dstIdx < maxIdx {
				out[dstIdx] = data.Pixels[srcIdx]
			}
		}
	}
	return fitsio.ImageData{Width: w, Height: h, Pixels: out}
}

func FlipMask(mask []byte, w, h int) []byte {
	out := make([]byte, len(mask))
	maskLen := len(mask)
	for y := 0; y < h; y++ {
		srcStart := (h - 1 - y) * w
		srcEnd := srcStart + w
		dstStart := y * w
		dstEnd := dstStart + w
		if srcStart >= 0 && srcEnd <= maskLen && dstStart >= 0 && dstEnd <= maskLen {
			copy(out[dstStart:dstEnd], mask[srcStart:srcEnd])
		}
	}
	return out
}

func FlipRGBA(buf []byte, w, h int) []byte {
	row := w * 4
	out := make([]byte, len(buf))
	for y := 0; y < h; y++ {
		copy(out[y*row:(y+1)*row], buf[(h-1-y)*row:(h-y)*row])
	}
	return out
}

func ResizeChannel(pixels []float32, oldW, oldH, newW, newH int) []float32 {
	out := make([]float32, newW*newH)
	xRatio := float64(oldW) / float64(newW)
	yRatio := float64(oldH) / float64(newH)
	for y := 0; y < newH; y++ {
		for x := 0; x < newW; x++ {
			px := float64(x) * xRatio
			py := float64(y) * yRatio
			xBase := int(px)
			yBase := int(py)
			xDiff := px - float64(xBase)
			yDiff := py - float64(yBase)
			idx := yBase*oldW + xBase
			if xBase >= oldW-1 || yBase >= oldH-1 {
				out[y*newW+x] = pixels[idx]
				continue
			}
			a := float64(pixels[idx])
			b := float64(pixels[idx+1])
			c := float64(pixels[(yBase+1)*oldW+xBase])
			d := float64(pixels[(yBase+1)*oldW+xBase+1])
			if math.IsNaN(a) || math.IsNaN(b) || math.IsNaN(c) || math.IsNaN(d) {
				out[y*newW+x] = float32(a)
				continue
			}
			out[y*newW+x] = float32(a*(1-xDiff)*(1-yDiff) + b*xDiff*(1-yDiff) + c*(1-xDiff)*yDiff + d*xDiff*yDiff)
		}
	}
	return out
}

func GetPixelScale(headerLines []string) float64 {
	getVal := func(keys ...string) (float64, bool) {
		for _, line := range headerLines {
			for _, key := range keys {
				if len(line) >= len(key) && line[:len(key)] == key {
					parts := strings.SplitN(line, "=", 2)
					if len(parts) == 2 {
						valStr := strings.SplitN(parts[1], "/", 2)[0]
						valStr = strings.TrimSpace(valStr)
						if v, err := strconv.ParseFloat(valStr, 64); err == nil {
							return v, true
						}
					}
				}
			}
		}
		return 0, false
	}
	cd11, ok11 := getVal("CD1_1")
	cd21, ok21 := getVal("CD2_1")
	if ok11 && ok21 {
		return math.Sqrt(cd11*cd11+cd21*cd21) * 3600.0
	}
	cdelt1, okDelt := getVal("CDELT1")
	if okDelt {
		return math.Abs(cdelt1) * 3600.0
	}
	pixscale, okPix := getVal("PIXSCALE")
	if okPix {
		return pixscale
	}
	return 1.0
}

func clampByte(v float64) byte {
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return byte(math.Round(v))
}
