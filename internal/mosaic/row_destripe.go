package mosaic

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"gofitsv3/internal/debuglog"
)

// Row destriping removes NIRCam 1/f readout noise: a per-row zero-point
// wander, independent per amplifier output, that survives the STScI cal
// pipeline and shows up as horizontal striping in drizzled mosaics. Because
// every exposure carries its own stripe realization, the stripes also shift
// the local mean of each exposure, so at dither coverage boundaries the
// residual appears as flat DC steps between zones (see
// docs/sky-background-matching-plan.md, Stage 3).
//
// Only the high-frequency component of each amp's row-median series is
// subtracted: the series is smoothed with a wide symmetric window and the
// smooth trend is kept, so real horizontal nebular structure (low-frequency
// by nature) passes through untouched.

const (
	// destripeCellSize is the bin size of the coarse background model used to
	// mask sources before measuring row medians. 64 px is large enough that
	// smooth nebulosity stays in the model (and therefore unmasked) while
	// stars and compact knots stand out as residuals.
	destripeCellSize = 64

	// destripeMaskSigma is the positive-residual threshold, in robust sigma,
	// above the coarse background model at which a pixel is excluded from the
	// row statistics.
	destripeMaskSigma = 3.0

	// destripeTrendWindow is the full smoothing window (rows) used to build
	// the low-frequency trend that is preserved. 129 rows ≈ 4 arcsec at NIRCam
	// SW scale: real nebular gradients live below this frequency, 1/f banding
	// above it.
	destripeTrendWindow = 129

	// destripeMinRowSamples rejects a row median built from too few unmasked
	// pixels to be trustworthy (e.g. a row crossing a large masked region).
	destripeMinRowSamples = 64

	// destripeMaxMADSamples bounds the residual sample used to estimate the
	// masking threshold, mirroring skyMaxSamples.
	destripeMaxMADSamples = 2_000_000
)

// applyRowDestripe removes per-amplifier 1/f row banding from sci in place.
// No-op for non-NIRCam inputs, reference-only inputs, or frames too small to
// measure. Must run after applyAmpPedestalCorrection so the row medians are
// not biased by a DC step between amps.
func applyRowDestripe(p plannedInput, sci []float32, options SkysubOptions, userMask []bool) error {
	if p.input.ReferenceOnly {
		debuglog.Log(fmt.Sprintf("DESTRIPE input=%s skipped: reference-only input", InputKey(p.input)))
		return nil
	}
	if !isNircamFrame(p.input) {
		debuglog.Log(fmt.Sprintf("DESTRIPE input=%s skipped: non-NIRCam input", InputKey(p.input)))
		return nil
	}
	if dir := strings.TrimSpace(options.RowDestripeDirection); dir != "" && !strings.EqualFold(dir, "rows") {
		debuglog.Log(fmt.Sprintf("DESTRIPE input=%s skipped: unsupported direction %q", InputKey(p.input), options.RowDestripeDirection))
		return fmt.Errorf("row destripe direction %q is not supported", options.RowDestripeDirection)
	}
	width := p.input.HDU.Data.Width
	height := p.input.HDU.Data.Height
	ampWidth := width / ampCount
	if ampWidth < destripeMinRowSamples || height < destripeCellSize {
		debuglog.Log(fmt.Sprintf("DESTRIPE input=%s skipped: frame too small width=%d height=%d ampWidth=%d", InputKey(p.input), width, height, ampWidth))
		return nil
	}
	settings := normalizeRowDestripeOptions(options)
	masked := buildDestripeMask(sci, width, height, settings.MaskSigma, userMask)
	maskedCount := countMaskedPixels(masked)
	validCount := width*height - maskedCount

	var sumSq, maxAbs float64
	rows := 0
	for amp := 0; amp < ampCount; amp++ {
		x0 := amp * ampWidth
		x1 := x0 + ampWidth
		if amp == ampCount-1 {
			x1 = width
		}
		rowMed := ampRowMedians(sci, masked, width, height, x0, x1)
		trend := smoothValidSeries(rowMed, settings.TrendWindow)
		var ampSumSq, ampMaxAbs float64
		ampRows := 0
		for y := 0; y < height; y++ {
			if math.IsNaN(rowMed[y]) || math.IsNaN(trend[y]) {
				continue
			}
			delta := rowMed[y] - trend[y]
			if delta == 0 {
				continue
			}
			row := y * width
			for x := x0; x < x1; x++ {
				idx := row + x
				if idx >= len(sci) || !isFinite32(sci[idx]) {
					continue
				}
				sci[idx] -= float32(delta)
			}
			sumSq += delta * delta
			rows++
			ampSumSq += delta * delta
			ampRows++
			if a := math.Abs(delta); a > maxAbs {
				maxAbs = a
			}
			if a := math.Abs(delta); a > ampMaxAbs {
				ampMaxAbs = a
			}
		}
		if ampRows > 0 {
			debuglog.Log(fmt.Sprintf("DESTRIPE input=%s amp=%d valid=%d masked=%d rows=%d rms=%.6f max=%.6f",
				InputKey(p.input), amp+1, validCount, maskedCount, ampRows, math.Sqrt(ampSumSq/float64(ampRows)), ampMaxAbs))
		}
	}
	if rows > 0 {
		debuglog.Log(fmt.Sprintf("DESTRIPE input=%s valid=%d masked=%d rows=%d rms=%.6f max=%.6f",
			InputKey(p.input), validCount, maskedCount, rows, math.Sqrt(sumSq/float64(rows)), maxAbs))
	} else {
		debuglog.Log(fmt.Sprintf("DESTRIPE input=%s skipped: no valid row corrections valid=%d masked=%d", InputKey(p.input), validCount, maskedCount))
	}
	return nil
}

type rowDestripeSettings struct {
	MaskSigma   float64
	TrendWindow int
}

func normalizeRowDestripeOptions(options SkysubOptions) rowDestripeSettings {
	settings := rowDestripeSettings{
		MaskSigma:   options.RowDestripeMaskSigma,
		TrendWindow: options.RowDestripeTrendWindow,
	}
	if settings.MaskSigma <= 0 || !isFinite64(settings.MaskSigma) {
		settings.MaskSigma = destripeMaskSigma
	}
	if settings.TrendWindow <= 0 {
		settings.TrendWindow = destripeTrendWindow
	}
	if settings.TrendWindow%2 == 0 {
		settings.TrendWindow++
	}
	return settings
}

// buildDestripeMask combines finite-pixel validity, optional user masking, and
// automatic source masking. A true bit means the pixel is excluded from row
// statistics. userMask true bits are also excluded.
func buildDestripeMask(sci []float32, width, height int, maskSigma float64, userMask []bool) []bool {
	masked := buildDestripeBaseMask(sci, width, height, userMask)
	auto := buildDestripeAutoSourceMask(sci, masked, width, height, maskSigma)
	for i, v := range auto {
		if v {
			masked[i] = true
		}
	}
	return masked
}

func buildDestripeBaseMask(sci []float32, width, height int, userMask []bool) []bool {
	total := width * height
	masked := make([]bool, total)
	for i := 0; i < total; i++ {
		if i >= len(sci) || !isFinite32(sci[i]) || (i < len(userMask) && userMask[i]) {
			masked[i] = true
		}
	}
	return masked
}

func countMaskedPixels(masked []bool) int {
	count := 0
	for _, v := range masked {
		if v {
			count++
		}
	}
	return count
}

// buildDestripeAutoSourceMask flags pixels sitting more than maskSigma robust
// sigma above a coarse (destripeCellSize-binned) median background model.
// Only positive outliers are masked: stars and compact knots bias row medians
// upward, while low outliers are either already NaN (DQ-cleaned) or genuine
// background the median should see.
func buildDestripeAutoSourceMask(sci []float32, baseMask []bool, width, height int, maskSigma float64) []bool {
	masked := make([]bool, width*height)
	cw := (width + destripeCellSize - 1) / destripeCellSize
	ch := (height + destripeCellSize - 1) / destripeCellSize
	cellMed := make([]float64, cw*ch)
	scratch := make([]float64, 0, destripeCellSize*destripeCellSize)
	for cy := 0; cy < ch; cy++ {
		y1 := minInt((cy+1)*destripeCellSize, height)
		for cx := 0; cx < cw; cx++ {
			x1 := minInt((cx+1)*destripeCellSize, width)
			scratch = scratch[:0]
			for y := cy * destripeCellSize; y < y1; y++ {
				row := y * width
				for x := cx * destripeCellSize; x < x1; x++ {
					idx := row + x
					if idx < len(sci) && !baseMask[idx] && isFinite32(sci[idx]) {
						v := sci[idx]
						scratch = append(scratch, float64(v))
					}
				}
			}
			if len(scratch) == 0 {
				cellMed[cy*cw+cx] = math.NaN()
			} else {
				cellMed[cy*cw+cx] = quickSelectMedian(scratch)
			}
		}
	}

	stride := 1
	if total := width * height; total > destripeMaxMADSamples {
		stride = total / destripeMaxMADSamples
	}
	resid := make([]float64, 0, width*height/stride+1)
	for i := 0; i < len(sci); i += stride {
		if i >= len(baseMask) || baseMask[i] {
			continue
		}
		v := sci[i]
		if !isFinite32(v) {
			continue
		}
		m := cellMed[(i/width/destripeCellSize)*cw+(i%width)/destripeCellSize]
		if math.IsNaN(m) {
			continue
		}
		resid = append(resid, math.Abs(float64(v)-m))
	}
	if len(resid) == 0 {
		return masked
	}
	sigma := 1.4826 * quickSelectMedian(resid)
	if sigma <= 0 {
		return masked
	}
	thresh := maskSigma * sigma
	for y := 0; y < height; y++ {
		row := y * width
		mRow := (y / destripeCellSize) * cw
		for x := 0; x < width; x++ {
			idx := row + x
			if idx >= len(baseMask) || baseMask[idx] {
				continue
			}
			v := sci[idx]
			if !isFinite32(v) {
				continue
			}
			m := cellMed[mRow+x/destripeCellSize]
			if !math.IsNaN(m) && float64(v)-m > thresh {
				masked[idx] = true
			}
		}
	}
	return masked
}

func loadRowDestripeUserMask(input Input, options SkysubOptions) ([]bool, error) {
	if input.ReferenceOnly || !isNircamFrame(input) {
		return nil, nil
	}
	path, ok, err := rowDestripeMaskPath(input, options)
	if err != nil || !ok {
		return nil, err
	}
	mask, w, h, err := loadBinaryMaskFITS(path)
	if err != nil {
		return nil, err
	}
	if w != input.HDU.Data.Width || h != input.HDU.Data.Height {
		return nil, fmt.Errorf("row destripe mask dimension mismatch: mask %dx%d vs input %dx%d", w, h, input.HDU.Data.Width, input.HDU.Data.Height)
	}
	return mask, nil
}

func rowDestripeMaskPath(input Input, options SkysubOptions) (string, bool, error) {
	if path := strings.TrimSpace(options.RowDestripeMaskPath); path != "" {
		return path, true, nil
	}
	dir := strings.TrimSpace(options.RowDestripeMaskDir)
	if dir == "" {
		debuglog.Log(fmt.Sprintf("DESTRIPE input=%s skipped: row mask path/directory is blank", InputKey(input)))
		return "", false, nil
	}
	name := rowDestripeMaskName(input)
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return path, true, nil
	} else if os.IsNotExist(err) {
		debuglog.Log(fmt.Sprintf("DESTRIPE input=%s skipped: row mask not found %s", InputKey(input), name))
		return "", false, nil
	} else {
		return path, false, err
	}
}

func rowDestripeMaskName(input Input) string {
	base := filepath.Base(input.Path)
	if input.SourcePath != "" {
		base = filepath.Base(input.SourcePath)
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem = base
	}
	return stem + "_rowmask.fits"
}

// ampRowMedians returns the median of unmasked finite pixels in columns
// [x0, x1) for each row, NaN where fewer than destripeMinRowSamples survive.
func ampRowMedians(sci []float32, masked []bool, width, height, x0, x1 int) []float64 {
	med := make([]float64, height)
	scratch := make([]float64, 0, x1-x0)
	for y := 0; y < height; y++ {
		scratch = scratch[:0]
		row := y * width
		for x := x0; x < x1; x++ {
			idx := row + x
			if idx >= len(sci) || masked[idx] || !isFinite32(sci[idx]) {
				continue
			}
			scratch = append(scratch, float64(sci[idx]))
		}
		if len(scratch) < destripeMinRowSamples {
			med[y] = math.NaN()
		} else {
			med[y] = quickSelectMedian(scratch)
		}
	}
	return med
}

// smoothValidSeries returns the mean of valid (non-NaN) values in a window
// centered on each index. The window shrinks symmetrically near the ends
// (half-width min(window/2, i, n-1-i)), which keeps the smoother exactly
// linear-preserving everywhere: a straight-line trend passes through
// unchanged, so subtracting (value - trend) never tilts a real gradient.
// The first/last rows get a zero-width window (trend == value, correction 0).
func smoothValidSeries(vals []float64, window int) []float64 {
	n := len(vals)
	sum := make([]float64, n+1)
	cnt := make([]int, n+1)
	for i, v := range vals {
		sum[i+1] = sum[i]
		cnt[i+1] = cnt[i]
		if !math.IsNaN(v) {
			sum[i+1] += v
			cnt[i+1]++
		}
	}
	out := make([]float64, n)
	for i := range out {
		half := window / 2
		if i < half {
			half = i
		}
		if n-1-i < half {
			half = n - 1 - i
		}
		lo, hi := i-half, i+half+1
		c := cnt[hi] - cnt[lo]
		if c == 0 {
			out[i] = math.NaN()
			continue
		}
		out[i] = (sum[hi] - sum[lo]) / float64(c)
	}
	return out
}
