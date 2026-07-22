package mosaic

import (
	"fmt"
	"math"
	"strings"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
)

// NIRCam detectors are always read out through 4 vertical amplifier strips,
// each with its own bias/gain zero-point. See
// docs/sky-background-matching-plan.md, Stage 3: a 100% zoom crop of the
// user's NIRCam nebula mosaic showed flat pedestal steps at these boundaries,
// distinct from (and not fixable by) the overlap-based sky match, since there
// is no overlap between amp strips within a single exposure.
const ampCount = 4

// ampBandWidth is how many columns on each side of an amp boundary are
// sampled to measure the step. Keeping it narrow is what makes this safe on
// nebula fields: a real astrophysical gradient varies negligibly across such
// a short span, so the measured step is attributable to the amp boundary
// itself rather than smooth background structure (the same difference-based
// principle used for sky matching in skysub.go, applied within a frame).
//
// Note this narrow-band comparison still picks up any genuine background
// slope times the ~ampBandWidth separation between the two band centers (a
// smooth gradient does not average to exactly the same value on both sides).
// For realistic nebula gradients this leakage is a small fraction of a typical
// amp step; keeping ampBandWidth modest bounds it further.
const ampBandWidth = 16

// ampBandMinSamples rejects a boundary measurement built from too few clipped
// samples to trust (e.g. a heavily masked or very short frame).
const ampBandMinSamples = 200

var ampClipOptions = SkysubOptions{Clip: 5, LSigma: 4.0, USigma: 4.0, Stat: SkyStatMedian}

// isNircamFrame reports whether an input's primary header identifies it as a
// JWST NIRCam exposure.
func isNircamFrame(in Input) bool {
	return strings.EqualFold(fitsio.HeaderString(in.PrimaryHeader, "INSTRUME"), "NIRCAM")
}

// applyAmpPedestalCorrection removes NIRCam's per-amplifier readout pedestal
// from sci in place. It is a no-op for non-NIRCam inputs, reference-only
// inputs, or frames too narrow to hold 4 amp strips.
func applyAmpPedestalCorrection(p plannedInput, sci []float32) {
	if p.input.ReferenceOnly {
		debuglog.Log(fmt.Sprintf("AMPPEDESTAL input=%s skipped: reference-only input", InputKey(p.input)))
		return
	}
	if !isNircamFrame(p.input) {
		debuglog.Log(fmt.Sprintf("AMPPEDESTAL input=%s skipped: non-NIRCam input", InputKey(p.input)))
		return
	}
	width := p.input.HDU.Data.Width
	height := p.input.HDU.Data.Height
	steps, ok := ampBoundarySteps(sci, width, height)
	if !ok {
		debuglog.Log(fmt.Sprintf("AMPPEDESTAL input=%s skipped: insufficient boundary samples width=%d height=%d", InputKey(p.input), width, height))
		return
	}
	offsets := ampOffsetsFromSteps(steps)
	applyAmpOffsetsInPlace(sci, width, height, offsets)
	debuglog.Log(fmt.Sprintf("AMPPEDESTAL input=%s valid=%d offsets=%.6f,%.6f,%.6f,%.6f rms=%.6f max=%.6f",
		InputKey(p.input), countFinitePixels(sci), offsets[0], offsets[1], offsets[2], offsets[3], ampOffsetRMS(offsets), ampOffsetMaxAbs(offsets)))
}

// ampBoundarySteps measures the value step across each of the 3 internal
// amplifier boundaries. steps[k] is the step from amp k to amp k+1, each
// derived from a sigma-clipped median of a narrow column band flanking the
// boundary. ok is false if the frame is too narrow or any boundary lacks
// enough usable samples.
func ampBoundarySteps(sci []float32, width, height int) (steps [ampCount - 1]float64, ok bool) {
	ampWidth := width / ampCount
	if ampWidth <= ampBandWidth {
		return steps, false
	}
	for k := 0; k < ampCount-1; k++ {
		boundary := (k + 1) * ampWidth
		left := ampBandMedian(sci, width, height, boundary-ampBandWidth, boundary)
		right := ampBandMedian(sci, width, height, boundary, boundary+ampBandWidth)
		if math.IsNaN(left) || math.IsNaN(right) {
			return steps, false
		}
		steps[k] = right - left
	}
	return steps, true
}

// ampBandMedian returns the sigma-clipped median of finite pixels in columns
// [x0, x1) across all rows, or NaN if fewer than ampBandMinSamples survive.
func ampBandMedian(sci []float32, width, height, x0, x1 int) float64 {
	if x0 < 0 {
		x0 = 0
	}
	if x1 > width {
		x1 = width
	}
	if x1 <= x0 {
		return math.NaN()
	}
	values := make([]float64, 0, (x1-x0)*height)
	for y := 0; y < height; y++ {
		row := y * width
		for x := x0; x < x1; x++ {
			idx := row + x
			if idx >= len(sci) {
				continue
			}
			v := sci[idx]
			if !isFinite32(v) {
				continue
			}
			values = append(values, float64(v))
		}
	}
	if len(values) < ampBandMinSamples {
		return math.NaN()
	}
	med, err := estimateSkyFromValues(values, ampClipOptions)
	if err != nil {
		return math.NaN()
	}
	return med
}

// ampOffsetsFromSteps converts 3 boundary steps into 4 per-amp offsets,
// integrated from amp 0 and then re-centered to zero mean so the correction
// is purely relative: it does not shift the frame's overall level, which
// remains the responsibility of sky matching (SkyMethodMatch etc.).
func ampOffsetsFromSteps(steps [ampCount - 1]float64) [ampCount]float64 {
	var offsets [ampCount]float64
	cum := 0.0
	for k, step := range steps {
		cum += step
		offsets[k+1] = cum
	}
	var mean float64
	for _, o := range offsets {
		mean += o
	}
	mean /= float64(len(offsets))
	for i := range offsets {
		offsets[i] -= mean
	}
	return offsets
}

// applyAmpOffsetsInPlace subtracts offsets[x/ampWidth] from every finite
// pixel in sci, clamping the last (possibly wider, if width doesn't divide
// evenly by ampCount) strip to the final offset.
func applyAmpOffsetsInPlace(sci []float32, width, height int, offsets [ampCount]float64) {
	ampWidth := width / ampCount
	if ampWidth <= 0 {
		return
	}
	for y := 0; y < height; y++ {
		row := y * width
		for x := 0; x < width; x++ {
			amp := x / ampWidth
			if amp >= ampCount {
				amp = ampCount - 1
			}
			idx := row + x
			if idx >= len(sci) || !isFinite32(sci[idx]) {
				continue
			}
			sci[idx] -= float32(offsets[amp])
		}
	}
}

func ampOffsetRMS(offsets [ampCount]float64) float64 {
	var sumSq float64
	for _, offset := range offsets {
		sumSq += offset * offset
	}
	return math.Sqrt(sumSq / float64(len(offsets)))
}

func ampOffsetMaxAbs(offsets [ampCount]float64) float64 {
	maxAbs := 0.0
	for _, offset := range offsets {
		if a := math.Abs(offset); a > maxAbs {
			maxAbs = a
		}
	}
	return maxAbs
}

func countFinitePixels(pixels []float32) int {
	count := 0
	for _, v := range pixels {
		if isFinite32(v) {
			count++
		}
	}
	return count
}
