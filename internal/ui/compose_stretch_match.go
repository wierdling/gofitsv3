package ui

import (
	"fmt"
	"math"
	"sort"

	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

func matchComposeChannelStretch(ref, target *models.LoadedImage, matchStarCores bool) error {
	if ref == nil || target == nil {
		return fmt.Errorf("missing channel")
	}
	refPixels := ref.HDU.Data.Pixels
	targetPixels := target.HDU.Data.Pixels
	if len(refPixels) == 0 || len(targetPixels) == 0 {
		return fmt.Errorf("empty image data")
	}

	refLow, ok := composePercentile(refPixels, 50)
	if !ok {
		return fmt.Errorf("reference has no finite pixels")
	}
	targetLow, ok := composePercentile(targetPixels, 50)
	if !ok {
		return fmt.Errorf("target has no finite pixels")
	}

	refHigh, targetHigh, ok := composeStarCoreAnchors(ref, target, matchStarCores)
	if !ok {
		refHigh, ok = composePercentile(refPixels, 99.8)
		if !ok {
			return fmt.Errorf("reference high anchor unavailable")
		}
		targetHigh, ok = composePercentile(targetPixels, 99.8)
		if !ok {
			return fmt.Errorf("target high anchor unavailable")
		}
	}

	refScaledPeak := ref.ScaledPeak
	if math.IsNaN(refScaledPeak) || math.IsInf(refScaledPeak, 0) || refScaledPeak <= 0 {
		refScaledPeak = 10
	}
	target.Mode = ref.Mode
	target.ScaledPeak = refScaledPeak
	target.ShowClip = ref.ShowClip

	zLow := composeScaledInput(refLow, ref.Background, ref.Peak, refScaledPeak)
	zHigh := composeScaledInput(refHigh, ref.Background, ref.Peak, refScaledPeak)
	a := zLow / refScaledPeak
	b := zHigh / refScaledPeak
	if !isFinite64(a) || !isFinite64(b) || math.Abs(b-a) < 1e-9 || targetHigh <= targetLow {
		black, white, background, peak := processing.SmartLevels(targetPixels)
		target.Black = black
		target.White = white
		target.Background = background
		target.Peak = peak
		return nil
	}

	denom := (targetHigh - targetLow) / (b - a)
	background := targetLow - a*denom
	peak := background + denom
	if !isFinite64(background) || !isFinite64(peak) || peak <= background {
		black, white, smartBackground, smartPeak := processing.SmartLevels(targetPixels)
		target.Black = black
		target.White = white
		target.Background = smartBackground
		target.Peak = smartPeak
		return nil
	}

	target.Background = background
	target.Peak = peak
	target.Black = background
	target.White = peak
	return nil
}

func cloneLoadedImageForStretchMatch(img *models.LoadedImage) *models.LoadedImage {
	if img == nil {
		return nil
	}
	clone := *img
	clone.HDU = img.HDU
	clone.HDU.Data = img.HDU.Data
	if img.HDU.Data.Pixels != nil {
		clone.HDU.Data.Pixels = append([]float32(nil), img.HDU.Data.Pixels...)
	}
	return &clone
}

func composeScaledInput(raw, background, peak, scaledPeak float64) float64 {
	if !isFinite64(background) {
		background = 0
	}
	if !isFinite64(peak) || peak <= background {
		peak = background + 1
	}
	if !isFinite64(scaledPeak) || scaledPeak <= 0 {
		scaledPeak = 10
	}
	v := (raw - background) * scaledPeak / (peak - background)
	if v < 0 {
		return 0
	}
	return v
}

func composeStarCoreAnchors(ref, target *models.LoadedImage, enabled bool) (float64, float64, bool) {
	if !enabled || ref.HDU.Data.Width <= 0 || ref.HDU.Data.Height <= 0 {
		return 0, 0, false
	}
	refPixels := ref.HDU.Data.Pixels
	targetPixels := target.HDU.Data.Pixels
	refWhite := ref.White
	if refWhite <= ref.Black {
		refWhite = ref.Peak
	}
	stars := processing.ExtractStars(refPixels, ref.HDU.Data.Width, ref.HDU.Data.Height, 4, 5)
	refCores := make([]float64, 0, 64)
	targetCores := make([]float64, 0, 64)
	for _, star := range stars {
		if len(refCores) >= 64 {
			break
		}
		x := int(math.Round(star.X))
		y := int(math.Round(star.Y))
		if x < 0 || y < 0 || x >= ref.HDU.Data.Width || y >= ref.HDU.Data.Height {
			continue
		}
		refIdx := y*ref.HDU.Data.Width + x
		if refIdx < 0 || refIdx >= len(refPixels) {
			continue
		}
		refCore := float64(refPixels[refIdx])
		if !isFinite64(refCore) || refCore >= refWhite*0.98 {
			continue
		}
		tx := x
		ty := y
		if ref.HDU.Data.Width != target.HDU.Data.Width || ref.HDU.Data.Height != target.HDU.Data.Height {
			tx = int(math.Round(float64(x) * float64(target.HDU.Data.Width) / float64(ref.HDU.Data.Width)))
			ty = int(math.Round(float64(y) * float64(target.HDU.Data.Height) / float64(ref.HDU.Data.Height)))
		}
		if tx < 0 || ty < 0 || tx >= target.HDU.Data.Width || ty >= target.HDU.Data.Height {
			continue
		}
		targetIdx := ty*target.HDU.Data.Width + tx
		if targetIdx < 0 || targetIdx >= len(targetPixels) {
			continue
		}
		targetCore := float64(targetPixels[targetIdx])
		if !isFinite64(targetCore) {
			continue
		}
		refCores = append(refCores, refCore)
		targetCores = append(targetCores, targetCore)
	}
	if len(refCores) < 5 || len(targetCores) < 5 {
		return 0, 0, false
	}
	sort.Float64s(refCores)
	sort.Float64s(targetCores)
	return composePercentileSorted(refCores, 75), composePercentileSorted(targetCores, 75), true
}

func composePercentile(pixels []float32, p float64) (float64, bool) {
	values := make([]float64, 0, len(pixels))
	for _, v := range pixels {
		fv := float64(v)
		if isFinite64(fv) {
			values = append(values, fv)
		}
	}
	if len(values) == 0 {
		return 0, false
	}
	sort.Float64s(values)
	return composePercentileSorted(values, p), true
}

func composePercentileSorted(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if p <= 0 {
		return values[0]
	}
	if p >= 100 {
		return values[len(values)-1]
	}
	pos := (p / 100) * float64(len(values)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return values[lo]
	}
	frac := pos - float64(lo)
	return values[lo]*(1-frac) + values[hi]*frac
}

func isFinite64(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
