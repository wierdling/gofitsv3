package processing

import (
	"context"
	"image"
	"math"
	"runtime"
	"sort"
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

const fitsLiberatorAutoScaledPeak = 10.0

func AutoScaleLikeFitsLiberator(img *models.LoadedImage) {
	if img == nil {
		return
	}

	if img.White <= img.Black {
		black, white, _, _ := SmartLevels(img.HDU.Data.Pixels)
		img.Black = black
		img.White = white
	}

	img.Background = img.Black
	img.Peak = img.White
	img.ScaledPeak = fitsLiberatorAutoScaledPeak

	if img.Peak <= img.Background {
		img.Peak = img.Background + 1
	}
}

// AutoMTFMidtone selects an MTF midtone for the image using a PixInsight-style
// auto-STF heuristic: it maps the level ~2.8 noise-sigma above the black point
// to a 0.25 target background. It respects the current Background/Peak levels
// (running Auto scaling first only if they look unset) and sets Mode to MTF.
func AutoMTFMidtone(img *models.LoadedImage) {
	if img == nil || len(img.HDU.Data.Pixels) == 0 {
		return
	}
	if img.Peak <= img.Background {
		AutoScaleLikeFitsLiberator(img)
	}
	_, sigma := EstimateBackground(img.HDU.Data.Pixels)
	denom := img.Peak - img.Background
	if denom <= 0 {
		denom = 1
	}
	// Normalised position of the 2.8-sigma reference level above the black point.
	xRef := 2.8 * sigma / denom
	if xRef < 1e-5 {
		xRef = 1e-5
	}
	if xRef > 1 {
		xRef = 1
	}
	// m solving Mtf(m, xRef) == 0.25 (the auto-STF target background).
	m := 3 * xRef / (2*xRef + 1)
	if m < 0.001 {
		m = 0.001
	}
	if m > 0.5 {
		m = 0.5
	}
	img.MTFMidtone = m
	img.Mode = stretch.MTF
}

func ApplyStretchParallel(img *models.LoadedImage) (fitsio.ImageData, []byte) {
	data := img.HDU.Data
	numPixels := len(data.Pixels)

	pixels := make([]float32, numPixels)

	var mask []byte
	if img.ShowClip {
		mask = make([]byte, numPixels)
	}

	background := img.Background
	peak := img.Peak
	scaledPeak := img.ScaledPeak

	if math.IsNaN(background) || math.IsInf(background, 0) {
		background = 0
	}

	if math.IsNaN(peak) || math.IsInf(peak, 0) || peak <= background {
		peak = background + 1
	}

	if math.IsNaN(scaledPeak) || math.IsInf(scaledPeak, 0) || scaledPeak <= 0 {
		// 1 is almost linear. For astronomy images, 100 is a better default.
		scaledPeak = 100
	}

	denom := peak - background
	stretchMul := scaledPeak / denom

	invLogPeak := 1.0 / math.Log1p(scaledPeak)
	invSqrtPeak := 1.0 / math.Sqrt(scaledPeak)
	invLinearPeak := 1.0 / scaledPeak

	// Resolve stretch-specific parameters, substituting defaults for unset
	// (zero) values so older projects and freshly loaded images stay sensible.
	asinhScale := img.AsinhScale
	if asinhScale <= 0 {
		asinhScale = stretch.DefaultAsinhScale
	}
	// Tunable Asinh: result = asinh(val/beta) / asinh(scaledPeak/beta).
	invAsinhPeak := 1.0 / math.Asinh(scaledPeak/asinhScale)

	mtfMidtone := img.MTFMidtone
	if mtfMidtone <= 0 || mtfMidtone >= 1 {
		mtfMidtone = stretch.DefaultMTFMidtone
	}

	ghsD := img.GHSStretch
	if ghsD <= 0 {
		ghsD = stretch.DefaultGHSStretch
	}
	ghsSP := img.GHSSymmetry
	if ghsSP <= 0 || ghsSP >= 1 {
		ghsSP = stretch.DefaultGHSSymmetry
	}
	ghs := stretch.NewGHS(ghsD, img.GHSLocal, ghsSP, 0, 1)

	mode := img.Mode
	black := img.Black
	white := img.White
	useRawClipOverlay := mask != nil && white > black

	numWorkers := runtime.NumCPU()
	if numWorkers < 1 {
		numWorkers = 1
	}

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

				if math.IsNaN(v) || math.IsInf(v, 0) {
					if mask != nil {
						mask[i] = 3
					}
					pixels[i] = 0
					continue
				}

				// Black/White are now used only as a clip overlay.
				// They do not change the value being stretched.
				if useRawClipOverlay {
					if v < black {
						mask[i] = 1
					} else if v > white {
						mask[i] = 2
					}
				}

				val := (v - background) * stretchMul

				if val < 0 {
					val = 0
				}

				var result float64

				switch mode {
				case stretch.Log:
					result = math.Log1p(val) * invLogPeak

				case stretch.Asinh:
					result = math.Asinh(val/asinhScale) * invAsinhPeak

				case stretch.Sqrt:
					result = math.Sqrt(val) * invSqrtPeak

				case stretch.MTF:
					result = stretch.Mtf(mtfMidtone, val*invLinearPeak)

				case stretch.GHS:
					result = ghs.Eval(val * invLinearPeak)

				default:
					result = val * invLinearPeak
				}

				if math.IsNaN(result) || math.IsInf(result, 0) || result < 0 {
					result = 0
				}

				if result > 1.0 {
					result = 1.0
					if mask != nil && mask[i] == 0 {
						mask[i] = 2
					}
				}

				pixels[i] = float32(result)
			}
		}(start, end)
	}

	wg.Wait()

	if mode == stretch.HistEq {
		pixels = stretch.Apply(pixels, stretch.HistEq)
	}

	return fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: pixels}, mask
}

func ComposeRGB(ctx context.Context, imgs []*models.LoadedImage) ([]byte, int, int, [3]histogram.Stats) {
	if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
		return nil, 0, 0, [3]histogram.Stats{}
	}

	ref := imgs[1]
	w := ref.HDU.Data.Width
	h := ref.HDU.Data.Height

	rData := stretchForReferenceGrid(ctx, imgs[2], ref)
	if ctx.Err() != nil {
		return nil, 0, 0, [3]histogram.Stats{}
	}
	gData := stretchForReferenceGrid(ctx, ref, ref)
	if ctx.Err() != nil {
		return nil, 0, 0, [3]histogram.Stats{}
	}
	bData := stretchForReferenceGrid(ctx, imgs[0], ref)
	if ctx.Err() != nil {
		return nil, 0, 0, [3]histogram.Stats{}
	}

	buf := render.ComposeRGB(rData.Pixels, gData.Pixels, bData.Pixels, w, h, imgs[2].Mode, imgs[1].Mode, imgs[0].Mode)
	return buf, w, h, HistogramRGB(buf)
}

// OverlayLayer is a tinted image (e.g. orange, yellow) screen-blended onto the
// base RGB composite.
type OverlayLayer struct {
	Image    *models.LoadedImage
	Settings models.OrangeLayerState
}

func ComposeRGBWithOrange(ctx context.Context, imgs []*models.LoadedImage, orange *models.LoadedImage, settings models.OrangeLayerState) ([]byte, int, int, [3]histogram.Stats) {
	return ComposeRGBWithOverlays(ctx, imgs, []OverlayLayer{{Image: orange, Settings: settings}})
}

// ComposeRGBWithOverlays composes the base RGB image, then screen-blends each
// tinted overlay layer on top, in order.
func ComposeRGBWithOverlays(ctx context.Context, imgs []*models.LoadedImage, overlays []OverlayLayer) ([]byte, int, int, [3]histogram.Stats) {
	if len(imgs) < 3 {
		return nil, 0, 0, [3]histogram.Stats{}
	}
	buf, w, h, _ := ComposeRGB(ctx, imgs)
	if buf == nil || imgs[1] == nil || ctx.Err() != nil {
		return buf, w, h, HistogramRGB(buf)
	}
	ref := imgs[1]
	for _, ov := range overlays {
		if ctx.Err() != nil {
			return nil, 0, 0, [3]histogram.Stats{}
		}
		blendOverlayCtx(ctx, buf, ov.Image, ref, ov.Settings)
	}
	return buf, w, h, HistogramRGB(buf)
}

// blendOverlay combines a single tinted overlay image onto buf in place,
// aligned/stretched to the reference grid, using a highlight-protected additive
// blend: out = base + layer - k*base*layer, where k = settings.HighlightProtect.
// k=1 is a screen blend (soft, never clips); k=0 is pure additive (keeps the
// most overlay detail but can clip in bright regions).
func blendOverlay(buf []byte, overlay, ref *models.LoadedImage, settings models.OrangeLayerState) {
	blendOverlayCtx(context.Background(), buf, overlay, ref, settings)
}

func blendOverlayCtx(ctx context.Context, buf []byte, overlay, ref *models.LoadedImage, settings models.OrangeLayerState) {
	if overlay == nil || ref == nil {
		return
	}
	opacity := settings.Opacity
	if opacity < 0 {
		opacity = 0
	} else if opacity > 1 {
		opacity = 1
	}
	if opacity == 0 {
		return
	}
	k := settings.HighlightProtect
	if k < 0 {
		k = 0
	} else if k > 1 {
		k = 1
	}

	overlayData := stretchForReferenceGrid(ctx, overlay, ref)
	rTint := float64(settings.ColorR) / 255
	gTint := float64(settings.ColorG) / 255
	bTint := float64(settings.ColorB) / 255

	for i, v := range overlayData.Pixels {
		idx := i * 4
		if idx+2 >= len(buf) {
			break
		}
		strength := utils.Clamp01(float64(v)) * opacity
		blendChannel := func(base byte, tint float64) byte {
			baseF := float64(base) / 255
			layerF := strength * tint
			out := baseF + layerF - k*baseF*layerF
			return byte(utils.Clamp01(out)*255 + 0.5)
		}
		buf[idx] = blendChannel(buf[idx], rTint)
		buf[idx+1] = blendChannel(buf[idx+1], gTint)
		buf[idx+2] = blendChannel(buf[idx+2], bTint)
	}
}

// ComposeRGBFloat32 returns per-channel float32 pixels (values in [0,1]) for
// the three loaded images, aligned and stretched to the green reference grid.
// Channel order: r, g, b matching imgs[2], imgs[1], imgs[0].
func ComposeRGBFloat32(imgs []*models.LoadedImage) (r, g, b []float32, width, height int) {
	if imgs[0] == nil || imgs[1] == nil || imgs[2] == nil {
		return nil, nil, nil, 0, 0
	}
	ref := imgs[1]
	rData := stretchForReferenceGrid(context.Background(), imgs[2], ref)
	gData := stretchForReferenceGrid(context.Background(), ref, ref)
	bData := stretchForReferenceGrid(context.Background(), imgs[0], ref)
	return rData.Pixels, gData.Pixels, bData.Pixels, ref.HDU.Data.Width, ref.HDU.Data.Height
}

// ApplyRGBLevelsFloat32 applies RGB level adjustments to float32 channel data.
// Input pixels are expected in [0,1]; levels.Min/Max are in the 0-255 scale
// used by the 8-bit pipeline. Returns new slices with values clamped to [0,1].
func ApplyRGBLevelsFloat32(r, g, b []float32, levels *models.RgbLevels) ([]float32, []float32, []float32) {
	channels := [3][]float32{r, g, b}
	out := [3][]float32{}
	for c := 0; c < 3; c++ {
		src := channels[c]
		dst := make([]float32, len(src))
		minF := float32(levels.Min[c]) / 255
		maxF := float32(levels.Max[c]) / 255
		rng := maxF - minF
		if rng <= 0 {
			out[c] = dst // zero-filled
			continue
		}
		for i, v := range src {
			scaled := (v - minF) / rng
			if scaled < 0 {
				scaled = 0
			} else if scaled > 1 {
				scaled = 1
			}
			dst[i] = scaled
		}
		out[c] = dst
	}
	return out[0], out[1], out[2]
}

func ImageDataForReferenceGrid(img, ref *models.LoadedImage) fitsio.ImageData {
	return ImageDataForReferenceGridCtx(context.Background(), img, ref)
}

func ImageDataForReferenceGridCtx(ctx context.Context, img, ref *models.LoadedImage) fitsio.ImageData {
	if img == nil || ref == nil {
		return fitsio.ImageData{}
	}
	if img == ref || sharedDrizzleGrid(img, ref) {
		return fitsio.ImageData{
			Width:  img.HDU.Data.Width,
			Height: img.HDU.Data.Height,
			Pixels: append([]float32(nil), img.HDU.Data.Pixels...),
		}
	}

	alignedPixels, _, err := AlignChannelUsingWCSCtx(
		ctx,
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
		return fitsio.ImageData{Width: ref.HDU.Data.Width, Height: ref.HDU.Data.Height, Pixels: alignedPixels}
	}

	if img.HDU.Data.Width != ref.HDU.Data.Width || img.HDU.Data.Height != ref.HDU.Data.Height {
		resized := ResizeChannel(img.HDU.Data.Pixels, img.HDU.Data.Width, img.HDU.Data.Height, ref.HDU.Data.Width, ref.HDU.Data.Height)
		return fitsio.ImageData{Width: ref.HDU.Data.Width, Height: ref.HDU.Data.Height, Pixels: resized}
	}

	return fitsio.ImageData{
		Width:  img.HDU.Data.Width,
		Height: img.HDU.Data.Height,
		Pixels: append([]float32(nil), img.HDU.Data.Pixels...),
	}
}

func StretchedImageDataForReferenceGrid(img, ref *models.LoadedImage) fitsio.ImageData {
	return stretchForReferenceGrid(context.Background(), img, ref)
}

func stretchForReferenceGrid(ctx context.Context, img, ref *models.LoadedImage) fitsio.ImageData {
	if img == nil || ref == nil {
		return fitsio.ImageData{}
	}
	if img == ref || sharedDrizzleGrid(img, ref) {
		data, _ := ApplyStretchParallel(img)
		return data
	}
	raw := ImageDataForReferenceGridCtx(ctx, img, ref)
	if ctx.Err() != nil {
		return fitsio.ImageData{}
	}
	clone := *img
	clone.HDU = img.HDU
	clone.HDU.Data = raw
	data, _ := ApplyStretchParallel(&clone)
	return data
}

func sharedDrizzleGrid(img, ref *models.LoadedImage) bool {
	if img == nil || ref == nil {
		return false
	}
	if img.HDU.Data.Width != ref.HDU.Data.Width || img.HDU.Data.Height != ref.HDU.Data.Height {
		return false
	}
	imgScale, ok1 := fitsio.HeaderFloat(img.HDU.Header, "DRIZSCAL")
	refScale, ok2 := fitsio.HeaderFloat(ref.HDU.Header, "DRIZSCAL")
	imgOffX, ok3 := fitsio.HeaderFloat(img.HDU.Header, "ORIGOFFX")
	refOffX, ok4 := fitsio.HeaderFloat(ref.HDU.Header, "ORIGOFFX")
	imgOffY, ok5 := fitsio.HeaderFloat(img.HDU.Header, "ORIGOFFY")
	refOffY, ok6 := fitsio.HeaderFloat(ref.HDU.Header, "ORIGOFFY")
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) {
		return false
	}
	const tol = 1e-6
	return math.Abs(imgScale-refScale) <= tol &&
		math.Abs(imgOffX-refOffX) <= tol &&
		math.Abs(imgOffY-refOffY) <= tol
}

func ApplyRGBLevels(buf []byte, levels *models.RgbLevels) []byte {
	if buf == nil || levels == nil {
		return buf
	}
	for i := 0; i+3 < len(buf); i += 4 {
		for c := 0; c < 3; c++ {
			val := float64(buf[i+c])
			minV := levels.Min[c]
			maxV := levels.Max[c]
			if maxV <= minV {
				buf[i+c] = clampByte(maxV)
				continue
			}
			if val < minV {
				val = minV
			}
			if val > maxV {
				val = maxV
			}
			scaled := (val - minV) / (maxV - minV) * 255
			buf[i+c] = clampByte(scaled)
		}
		buf[i+3] = 255
	}
	return buf
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

// SmartLevels returns initial stretch values for astronomical FITS data.
//
// For the current ApplyStretchParallel implementation, keep these paired:
//
//	black      == background
//	white      == peak
//
// That avoids fighting between the pre-clamp step and the stretch step.
func SmartLevels(pixels []float32) (black, white, background, peak float64) {
	if len(pixels) == 0 {
		return 0, 1, 0, 1
	}

	values := finiteSample(pixels, 1_000_000)
	if len(values) == 0 {
		return 0, 1, 0, 1
	}

	sort.Float64s(values)

	q001 := percentileSorted(values, 0.01)
	q01 := percentileSorted(values, 0.1)
	q50 := percentileSorted(values, 50.0)
	q995 := percentileSorted(values, 99.5)
	q998 := percentileSorted(values, 99.8)
	q999 := percentileSorted(values, 99.9)

	bg, _ := EstimateBackground(pixels)
	if math.IsNaN(bg) || math.IsInf(bg, 0) {
		bg = q50
	}

	sigma := robustSigma(values, bg)

	// Put the background/black point just below the estimated sky.
	// This preserves faint signal above the background without letting
	// extreme low outliers define the black point.
	background = bg - 0.5*sigma

	// Do not let a few extreme negative pixels pull the black point too far down.
	if background < q01 {
		background = q01
	}

	// But if the image has an unusually tight distribution, avoid collapse.
	if background >= bg {
		background = q001
	}

	// Peak controls where the stretch reaches white.
	// 99.8 is usually a good initial compromise:
	//   lower  = brighter faint nebulosity, more saturated stars
	//   higher = less saturation, dimmer faint structure
	peak = q998

	// If 99.8 is too close to the background, fall back to 99.9 or 99.5.
	if peak <= background {
		peak = q999
	}
	if peak <= background {
		peak = q995
	}
	if peak <= background {
		peak = background + 1
	}

	black = background
	white = peak

	return black, white, background, peak
}

func finiteSample(pixels []float32, maxSamples int) []float64 {
	if maxSamples <= 0 {
		maxSamples = 1_000_000
	}

	stride := 1
	if len(pixels) > maxSamples {
		stride = (len(pixels) + maxSamples - 1) / maxSamples
	}

	values := make([]float64, 0, minInt(len(pixels)/stride+1, maxSamples))

	for i := 0; i < len(pixels); i += stride {
		v := float64(pixels[i])
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		values = append(values, v)
	}

	return values
}

func percentileSorted(sorted []float64, pct float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}

	if pct <= 0 {
		return sorted[0]
	}
	if pct >= 100 {
		return sorted[n-1]
	}

	pos := (pct / 100.0) * float64(n-1)
	i := int(math.Floor(pos))
	f := pos - float64(i)

	if i >= n-1 {
		return sorted[n-1]
	}

	return sorted[i]*(1.0-f) + sorted[i+1]*f
}

func robustSigma(sortedValues []float64, center float64) float64 {
	if len(sortedValues) == 0 {
		return 1
	}

	deviations := make([]float64, len(sortedValues))
	for i, v := range sortedValues {
		deviations[i] = math.Abs(v - center)
	}

	sort.Float64s(deviations)

	medianAbsDeviation := percentileSorted(deviations, 50.0)
	sigma := 1.4826 * medianAbsDeviation

	if sigma > 0 && !math.IsNaN(sigma) && !math.IsInf(sigma, 0) {
		return sigma
	}

	// Fallback if the image is very flat.
	q16 := percentileSorted(sortedValues, 16.0)
	q84 := percentileSorted(sortedValues, 84.0)
	sigma = (q84 - q16) / 2.0

	if sigma > 0 && !math.IsNaN(sigma) && !math.IsInf(sigma, 0) {
		return sigma
	}

	return 1
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
		var maskVal byte
		if mask != nil && i < len(mask) {
			maskVal = mask[i]
		}
		switch maskVal {
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
	temp := make([]float32, w)
	for y := 0; y < h/2; y++ {
		copy(temp, data.Pixels[y*w:(y+1)*w])
		copy(data.Pixels[y*w:(y+1)*w], data.Pixels[(h-1-y)*w:(h-y)*w])
		copy(data.Pixels[(h-1-y)*w:(h-y)*w], temp)
	}
	return data
}

func FlipMask(mask []byte, w, h int) []byte {
	if mask == nil {
		return nil
	}
	temp := make([]byte, w)
	for y := 0; y < h/2; y++ {
		copy(temp, mask[y*w:(y+1)*w])
		copy(mask[y*w:(y+1)*w], mask[(h-1-y)*w:(h-y)*w])
		copy(mask[(h-1-y)*w:(h-y)*w], temp)
	}
	return mask
}

func FlipRGBA(buf []byte, w, h int) []byte {
	row := w * 4
	temp := make([]byte, row)
	for y := 0; y < h/2; y++ {
		copy(temp, buf[y*row:(y+1)*row])
		copy(buf[y*row:(y+1)*row], buf[(h-1-y)*row:(h-y)*row])
		copy(buf[(h-1-y)*row:(h-y)*row], temp)
	}
	return buf
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
