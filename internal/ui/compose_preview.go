package ui

import (
	"context"
	"fmt"
	"image"
	"math"
	"sort"
	"time"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

type composeViewportPreview struct {
	Image      *image.RGBA
	Bins       [256]int
	HistMax    int
	StatsText  string
	FilterText string
	Black      float64
	White      float64
	OrigW      int
	OrigH      int
}

type composePreviewData struct {
	Views       [4]composeViewportPreview
	RGBStats    [3]histogram.Stats
	BlinkFrames []composeBlinkFrame
}

func clearComposeOrigPixels(origPixels *[][]float32, idxs ...int) {
	if origPixels == nil {
		return
	}
	for _, idx := range idxs {
		if idx >= 0 && idx < len(*origPixels) {
			(*origPixels)[idx] = nil
		}
	}
}

func buildComposePreviewData(ctx context.Context, imgs []*models.LoadedImage, sharedHistScale bool, buildComposite bool, levels *models.RgbLevels, composeRGB func(context.Context) ([]byte, int, int, [3]histogram.Stats, error)) composePreviewData {
	start := time.Now()
	defer func() {
		debuglog.Log(fmt.Sprintf("buildComposePreviewData: total took %s", time.Since(start)))
	}()
	var out composePreviewData
	var channelPixels [3][]float32
	var channelStats [3]histogram.Stats
	for i := 0; i < 3; i++ {
		if i >= len(imgs) || imgs[i] == nil {
			out.Views[i] = composeViewportPreview{Image: blankImg(), StatsText: "Sky --  μ --  σ --"}
			continue
		}
		channelStart := time.Now()
		stretched, mask := processing.StretchForDisplay(imgs[i])
		stats := histogram.Compute(stretched.Pixels)
		sky, _ := processing.EstimateBackground(stretched.Pixels)
		channelPixels[i] = stretched.Pixels
		channelStats[i] = stats
		out.Views[i] = composeViewportPreview{
			Image:      processing.ToGrayRGBA(stretched, mask),
			Bins:       stats.Hist,
			StatsText:  fmt.Sprintf("Sky %.3f  μ %.3f  σ %.3f", sky, stats.Mean, stats.Std),
			FilterText: fitsio.FilterString(imgs[i].Primary),
			Black:      imgs[i].Black,
			White:      imgs[i].White,
			OrigW:      stretched.Width,
			OrigH:      stretched.Height,
		}
		debuglog.Log(fmt.Sprintf("buildComposePreviewData: channel %d took %s", i+1, time.Since(channelStart)))
	}
	if sharedHistScale {
		sharedBins, sharedMax, ok := buildSharedScaleChannelHistograms(channelPixels, channelStats)
		if ok {
			for i := 0; i < 3; i++ {
				if len(channelPixels[i]) == 0 {
					continue
				}
				out.Views[i].Bins = sharedBins[i]
				out.Views[i].HistMax = sharedMax
			}
		}
	}
	if !buildComposite {
		out.Views[3] = composeViewportPreview{Image: blankImg(), StatsText: "Composite: off"}
		return out
	}

	if ctx.Err() != nil {
		out.Views[3] = composeViewportPreview{Image: blankImg(), StatsText: "Sky --  μ --  σ --"}
		return out
	}
	buf, w, h, rgbStats := processing.ComposeRGB(ctx, imgs)
	if composeRGB != nil {
		if altBuf, altW, altH, altStats, err := composeRGB(ctx); err == nil {
			buf, w, h, rgbStats = altBuf, altW, altH, altStats
		} else {
			debuglog.Log(fmt.Sprintf("buildComposePreviewData: compose failed: %v", err))
		}
	}
	if buf == nil {
		out.Views[3] = composeViewportPreview{Image: blankImg(), StatsText: "Sky --  μ --  σ --"}
		return out
	}
	out.RGBStats = rgbStats
	buf = processing.ApplyRGBLevels(buf, levels)
	lumaStats := histogramRGBLuminance(buf)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	copy(img.Pix, buf)
	out.Views[3] = composeViewportPreview{
		Image:     img,
		Bins:      lumaStats.Hist,
		StatsText: fmt.Sprintf("Luma μ %.1f  σ %.1f", lumaStats.Mean, lumaStats.Std),
		OrigW:     w,
		OrigH:     h,
	}
	return out
}

func histogramRGBLuminance(buf []byte) histogram.Stats {
	var stats histogram.Stats
	if len(buf) == 0 {
		return stats
	}
	var sum float64
	for i := 0; i+3 < len(buf); i += 4 {
		luma := int(math.Round(0.299*float64(buf[i]) + 0.587*float64(buf[i+1]) + 0.114*float64(buf[i+2])))
		if luma < 0 {
			luma = 0
		} else if luma > 255 {
			luma = 255
		}
		stats.Hist[luma]++
		sum += float64(luma)
		stats.Count++
	}
	if stats.Count == 0 {
		return stats
	}
	stats.Min = 0
	stats.Max = 255
	stats.Mean = sum / float64(stats.Count)
	var variance float64
	for i := 0; i+3 < len(buf); i += 4 {
		luma := 0.299*float64(buf[i]) + 0.587*float64(buf[i+1]) + 0.114*float64(buf[i+2])
		diff := luma - stats.Mean
		variance += diff * diff
	}
	stats.Std = math.Sqrt(variance / float64(stats.Count))
	return stats
}

func composePixelValueAt(img *models.LoadedImage, point imagePoint) (float64, bool) {
	if img == nil {
		return 0, false
	}
	data := img.HDU.Data
	if data.Width <= 0 || data.Height <= 0 || point.X < 0 || point.Y < 0 || point.X >= data.Width || point.Y >= data.Height {
		return 0, false
	}
	idx := point.Y*data.Width + point.X
	if idx < 0 || idx >= len(data.Pixels) {
		return 0, false
	}
	return float64(data.Pixels[idx]), true
}

// composePickRadius is the half-width (in pixels) of the box sampled when
// picking a black/white level. A 5x5 region keeps the picked value stable
// against single noisy pixels without averaging over real structure.
const composePickRadius = 2

// composeRegionMedianAt returns the median of the finite pixels in the square
// region of half-width radius centered on point. Using a median (rather than a
// single pixel) makes level picking robust to noise and hot/cold pixels, so the
// committed level no longer depends on exactly which pixel was clicked.
func composeRegionMedianAt(img *models.LoadedImage, point imagePoint, radius int) (float64, bool) {
	if img == nil {
		return 0, false
	}
	data := img.HDU.Data
	if data.Width <= 0 || data.Height <= 0 || point.X < 0 || point.Y < 0 || point.X >= data.Width || point.Y >= data.Height {
		return 0, false
	}
	if radius < 0 {
		radius = 0
	}
	vals := make([]float64, 0, (2*radius+1)*(2*radius+1))
	for dy := -radius; dy <= radius; dy++ {
		y := point.Y + dy
		if y < 0 || y >= data.Height {
			continue
		}
		row := y * data.Width
		for dx := -radius; dx <= radius; dx++ {
			x := point.X + dx
			if x < 0 || x >= data.Width {
				continue
			}
			idx := row + x
			if idx < 0 || idx >= len(data.Pixels) {
				continue
			}
			v := float64(data.Pixels[idx])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return 0, false
	}
	sort.Float64s(vals)
	n := len(vals)
	if n%2 == 1 {
		return vals[n/2], true
	}
	return (vals[n/2-1] + vals[n/2]) / 2, true
}

func buildSharedScaleChannelHistograms(channelPixels [3][]float32, channelStats [3]histogram.Stats) ([3][256]int, int, bool) {
	var bins [3][256]int
	var sharedMin, sharedMax float64
	haveRange := false

	for i := 0; i < 3; i++ {
		stats := channelStats[i]
		if stats.Count == 0 || len(channelPixels[i]) == 0 {
			continue
		}
		low, high := histogram.PercentileClip(stats, 0.1, 99.9)
		if high <= low {
			low, high = stats.Min, stats.Max
		}
		if !haveRange {
			sharedMin, sharedMax = low, high
			haveRange = true
			continue
		}
		if low < sharedMin {
			sharedMin = low
		}
		if high > sharedMax {
			sharedMax = high
		}
	}
	if !haveRange {
		return bins, 0, false
	}

	sharedRange := sharedMax - sharedMin
	sharedMaxCount := 0
	for i := 0; i < 3; i++ {
		if len(channelPixels[i]) == 0 {
			continue
		}
		for _, v := range channelPixels[i] {
			fv := float64(v)
			if math.IsNaN(fv) || math.IsInf(fv, 0) {
				continue
			}
			idx := 0
			if sharedRange > 0 {
				idx = int((fv - sharedMin) / sharedRange * 255.0)
			}
			if idx < 0 {
				idx = 0
			} else if idx > 255 {
				idx = 255
			}
			bins[i][idx]++
			if bins[i][idx] > sharedMaxCount {
				sharedMaxCount = bins[i][idx]
			}
		}
	}
	if sharedMaxCount == 0 {
		return bins, 0, false
	}
	return bins, sharedMaxCount, true
}

func applyComposePreviewData(data composePreviewData, views []*viewport, pushHist func([3]histogram.Stats)) {
	for i := 0; i < 4 && i < len(views); i++ {
		if views[i] == nil {
			continue
		}
		item := data.Views[i]
		if item.Image == nil {
			item.Image = blankImg()
		}
		views[i].image.Image = item.Image
		views[i].origW, views[i].origH = item.OrigW, item.OrigH
		views[i].bins = item.Bins
		views[i].histMax = item.HistMax
		views[i].blackBox.SetValue(item.Black)
		views[i].whiteBox.SetValue(item.White)
		if views[i].StatsLabel != nil {
			if item.StatsText == "" {
				item.StatsText = "Sky --  μ --  σ --"
			}
			views[i].StatsLabel.SetText(item.StatsText)
		}
		views[i].SetFilterText(item.FilterText)
		views[i].histogram.Refresh()
		if views[i].zoomLabel.Selected == "fit" {
			views[i].zoom = views[i].fitZoom()
		}
		views[i].applyZoom()
		views[i].image.Refresh()
	}
	if pushHist != nil {
		pushHist(data.RGBStats)
	}
}

// Updated signature to expect an array of histogram.Stats structs
func updatePreviews(imgs []*models.LoadedImage, views []*viewport, levels *models.RgbLevels, pushHist func([3]histogram.Stats), composeRGB func(context.Context) ([]byte, int, int, [3]histogram.Stats, error)) {
	start := time.Now()
	defer func() {
		debuglog.Log(fmt.Sprintf("updatePreviews: total took %s", time.Since(start)))
	}()
	for i := 0; i < 3; i++ {
		channelStart := time.Now()
		if imgs[i] == nil {
			views[i].image.Image = blankImg()
			views[i].bins = [256]int{}
			views[i].histMax = 0
			views[i].blackBox.SetValue(0)
			views[i].whiteBox.SetValue(0)

			if views[i].StatsLabel != nil {
				views[i].StatsLabel.SetText("Sky --  μ --  σ --")
			}

			views[i].histogram.Refresh()
			views[i].image.Refresh()
			continue
		}

		stretchStart := time.Now()
		stretched, mask := processing.ApplyStretchParallel(imgs[i])
		debuglog.Log(fmt.Sprintf("updatePreviews: channel %d stretch took %s", i+1, time.Since(stretchStart)))

		rgbaStart := time.Now()
		views[i].image.Image = processing.ToGrayRGBA(stretched, mask)
		debuglog.Log(fmt.Sprintf("updatePreviews: channel %d gray RGBA took %s", i+1, time.Since(rgbaStart)))
		views[i].origW, views[i].origH = stretched.Width, stretched.Height

		// Use the new struct to compute data
		histStart := time.Now()
		stats := histogram.Compute(stretched.Pixels)
		sky, _ := processing.EstimateBackground(stretched.Pixels)
		debuglog.Log(fmt.Sprintf("updatePreviews: channel %d histogram took %s", i+1, time.Since(histStart)))
		views[i].bins = stats.Hist
		views[i].histMax = 0

		if views[i].StatsLabel != nil {
			views[i].StatsLabel.SetText(fmt.Sprintf("Sky %.3f  μ %.3f  σ %.3f", sky, stats.Mean, stats.Std))
		}

		views[i].blackBox.SetValue(imgs[i].Black)
		views[i].whiteBox.SetValue(imgs[i].White)

		views[i].histogram.Refresh()
		if views[i].zoomLabel.Selected == "fit" {
			views[i].zoom = views[i].fitZoom()
		}
		views[i].applyZoom()
		views[i].image.Refresh()
		debuglog.Log(fmt.Sprintf("updatePreviews: channel %d total took %s", i+1, time.Since(channelStart)))
	}

	// NOTE: processing.ComposeRGB must be updated to return [3]histogram.Stats instead of [3][256]int
	composeStart := time.Now()
	buf, w, h, rgbStats := processing.ComposeRGB(context.Background(), imgs)
	debuglog.Log(fmt.Sprintf("updatePreviews: ComposeRGB took %s", time.Since(composeStart)))
	if composeRGB != nil {
		if altBuf, altW, altH, altStats, err := composeRGB(context.Background()); err == nil {
			buf, w, h, rgbStats = altBuf, altW, altH, altStats
		} else {
			debuglog.Log(fmt.Sprintf("updatePreviews: compose failed: %v", err))
		}
	}

	if buf == nil {
		if pushHist != nil {
			pushHist([3]histogram.Stats{})
		}
		views[3].image.Image = blankImg()
		views[3].bins = [256]int{}
		views[3].histMax = 0
		views[3].blackBox.SetValue(0)
		views[3].whiteBox.SetValue(0)
		views[3].histogram.Refresh()
		views[3].image.Refresh()
		return
	}

	if pushHist != nil {
		pushHist(rgbStats)
	}

	rgbLevelsStart := time.Now()
	buf = processing.ApplyRGBLevels(buf, levels)
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	copy(img.Pix, buf)
	debuglog.Log(fmt.Sprintf("updatePreviews: RGB levels/final image took %s", time.Since(rgbLevelsStart)))

	views[3].image.Image = img
	views[3].origW, views[3].origH = w, h
	views[3].bins = [256]int{}
	views[3].histMax = 0
	views[3].blackBox.SetValue(0)
	views[3].whiteBox.SetValue(0)
	views[3].histogram.Refresh()

	if views[3].zoomLabel.Selected == "fit" {
		views[3].zoom = views[3].fitZoom()
	}

	views[3].applyZoom()
	views[3].image.Refresh()
}

func composeHasAllBaseChannels(imgs []*models.LoadedImage) bool {
	return len(imgs) >= 3 && imgs[0] != nil && imgs[1] != nil && imgs[2] != nil
}

func composeCompositeDisabledStatus(buildComposite bool, imgs []*models.LoadedImage) string {
	if !buildComposite {
		return "Composite disabled — enable Build color composite"
	}
	if !composeHasAllBaseChannels(imgs) {
		return "Composite disabled until all channels are loaded"
	}
	return "Composite disabled"
}

// composeRenderCache memoises a channel's offset-applied pixels so an unchanged
// Manual Offset isn't re-warped on every render. src is the original pixel slice
// the warp was based on; if the channel is reloaded (new slice) the cache misses.
type composeRenderCache struct {
	dx, dy, rot float64
	hasAlign    bool
	align       processing.AffineTransform
	src         []float32
	pixels      []float32
	treatment   models.StretchTreatment // pixels hold a treated, already stretched warp when set
}

// sameFloatSlice reports whether a and b share the same backing array (and
// length) — used to detect that a channel's original pixels were replaced.
func sameFloatSlice(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return &a[0] == &b[0]
}
