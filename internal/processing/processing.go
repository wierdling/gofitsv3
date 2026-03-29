package processing

import (
	"gofitsv3/internal/models"
	"image"
	"math"
	"runtime"
	"sync"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/render"
	"gofitsv3/internal/stretch"
	"gofitsv3/internal/utils"
)

func ApplyStretchParallel(img *models.LoadedImage) (fitsio.ImageData, []byte) {
	data := img.HDU.Data
	numPixels := len(data.Pixels)
	pixels := make([]float64, numPixels)
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
				v := data.Pixels[i]

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

				var result float64
				switch img.Mode {
				case stretch.Log:
					result = math.Log1p(val) * invLogPeak
				case stretch.Asinh:
					result = math.Asinh(val) * invAsinhPeak
				case stretch.Sqrt:
					result = math.Sqrt(val) * invSqrtPeak
				default:
					result = val * invLinearPeak
				}

				if result > 1.0 {
					result = 1.0
				}
				pixels[i] = result
			}
		}(start, end)
	}

	wg.Wait()

	if img.Mode == stretch.HistEq {
		pixels = stretch.Apply(pixels, img.Mode)
	}

	return fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: pixels}, mask
}

func ComposeRGB(imgs []*models.LoadedImage) ([]byte, int, int, [3][256]int) {
	if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
		return nil, 0, 0, [3][256]int{}
	}
	w := imgs[2].HDU.Data.Width
	h := imgs[2].HDU.Data.Height
	rData, _ := ApplyStretchParallel(imgs[2])
	gData, _ := ApplyStretchParallel(imgs[1])
	bData, _ := ApplyStretchParallel(imgs[0])
	buf := render.ComposeRGB(rData.Pixels, gData.Pixels, bData.Pixels, w, h, imgs[2].Mode, imgs[1].Mode, imgs[0].Mode)
	return buf, w, h, HistogramRGB(buf)
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

func HistogramRGB(buf []byte) [3][256]int {
	var bins [3][256]int
	if len(buf) == 0 {
		return bins
	}
	for i := 0; i+3 < len(buf); i += 4 {
		bins[0][buf[i]]++
		bins[1][buf[i+1]]++
		bins[2][buf[i+2]]++
	}
	return bins
}

func Histogram(pixels []float64) ([256]int, float64, float64) {
	var bins [256]int
	if len(pixels) == 0 {
		return bins, 0, 0
	}
	min, max := pixels[0], pixels[0]
	for _, v := range pixels {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		bin := int(utils.Clamp01(v) * 255)
		bins[bin]++
	}
	return bins, min, max
}

func AutoLevels(pixels []float64) (float64, float64) {
	if len(pixels) == 0 {
		return 0, 1
	}
	min, max := pixels[0], pixels[0]
	for _, v := range pixels {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
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
			b := byte(utils.Clamp01(v) * 255)
			img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2] = b, b, b
		}
		img.Pix[idx+3] = 255
	}
	return img
}

func FlipImageData(data fitsio.ImageData) fitsio.ImageData {
	w, h := data.Width, data.Height
	out := make([]float64, len(data.Pixels))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			srcIdx := (h-1-y)*w + x
			dstIdx := y*w + x
			out[dstIdx] = data.Pixels[srcIdx]
		}
	}
	return fitsio.ImageData{Width: w, Height: h, Pixels: out}
}

func FlipMask(mask []byte, w, h int) []byte {
	out := make([]byte, len(mask))
	for y := 0; y < h; y++ {
		copy(out[y*w:(y+1)*w], mask[(h-1-y)*w:(h-y)*w])
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

func clampByte(v float64) byte {
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return byte(math.Round(v))
}
