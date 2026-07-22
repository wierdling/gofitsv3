package mosaic

import (
	"fmt"
	"math"
	"strings"

	"gofitsv3/internal/debuglog"
)

// NormalizationMode selects how exposure-time normalization is decided for a set
// of inputs. It is independent of Options.WeightingMode: normalization rescales
// each frame's pixels to a per-second rate before drizzle, whereas weighting
// controls how frames are combined.
type NormalizationMode int

const (
	// NormOff disables exposure normalization. Existing behavior; pixels are
	// drizzled in their native units.
	NormOff NormalizationMode = iota
	// NormAuto normalizes only when BUNIT indicates total counts (ELECTRONS,
	// COUNTS, DN, ADU, ...). Rate units (ELECTRONS/S, ...) and unknown/missing
	// BUNIT are left unnormalized.
	NormAuto
	// NormOn normalizes every frame with a valid exposure time regardless of
	// BUNIT.
	NormOn
)

// bunitIsRate reports whether a BUNIT value denotes a per-time rate (e.g.
// "ELECTRONS/S", "COUNTS/SEC", "DN s-1"). known is false when bunit is empty.
func bunitIsRate(bunit string) (rate, known bool) {
	u := strings.ToUpper(strings.TrimSpace(bunit))
	if u == "" {
		return false, false
	}
	// Collapse whitespace so "DN / S" and "COUNTS PER SEC" are caught too.
	u = strings.ReplaceAll(u, " ", "")
	switch {
	case strings.Contains(u, "/S"),
		strings.Contains(u, "S-1"),
		strings.Contains(u, "SEC-1"),
		strings.Contains(u, "PERS"),
		strings.Contains(u, "PERSEC"):
		return true, true
	default:
		return false, true
	}
}

// bunitIsCalibratedFlux reports whether a BUNIT denotes a calibrated flux or
// surface-brightness unit (e.g. JWST Stage-2 "MJy/sr", or "MJy"/"Jy"/"uJy").
// These are absolutely calibrated, not total detector counts, so they must
// never be divided by exposure time. This is recognised explicitly rather than
// relying on the per-second rate check incidentally matching "/sr".
func bunitIsCalibratedFlux(bunit string) bool {
	u := strings.ToUpper(strings.TrimSpace(bunit))
	u = strings.ReplaceAll(u, " ", "")
	return strings.Contains(u, "JY") // MJY/SR, MJY, JY, UJY, ...
}

// exposureScaleFor returns the per-frame normalization factor 1/exptime and
// whether it is usable. It rejects non-positive, NaN, and Inf exposure times so
// a bogus scale is never silently applied.
func exposureScaleFor(exptime float64) (float64, bool) {
	if exptime <= 0 || math.IsNaN(exptime) || math.IsInf(exptime, 0) {
		return 0, false
	}
	scale := 1.0 / exptime
	if !isFinite64(scale) || scale <= 0 {
		return 0, false
	}
	return scale, true
}

// resolveNormalization decides, for one input, whether to normalize by exposure
// time under the given mode and the resulting scale factor (1/exptime). It never
// returns normalize=true without a finite, positive scale.
func resolveNormalization(mode NormalizationMode, bunit string, exptime float64) (normalize bool, scale float64) {
	scale, ok := exposureScaleFor(exptime)
	if !ok {
		return false, 0
	}
	switch mode {
	case NormOn:
		return true, scale
	case NormAuto:
		rate, known := bunitIsRate(bunit)
		if !known || rate || bunitIsCalibratedFlux(bunit) {
			// Unknown/missing BUNIT defaults to Off; rate units and calibrated
			// flux/surface-brightness units (e.g. JWST MJy/sr) need no divide.
			return false, scale
		}
		return true, scale
	default: // NormOff
		return false, scale
	}
}

// NormalizationFor reports the default normalize decision and scale factor
// (1/exptime) for a single item under mode. It mirrors what
// ApplyExposureNormalization sets and is intended for UI preview.
func NormalizationFor(mode NormalizationMode, bunit string, exptime float64) (normalize bool, scale float64) {
	return resolveNormalization(mode, bunit, exptime)
}

// ApplyExposureNormalization sets NormalizeExposure and ExposureScale on each
// input according to mode, BUNIT, and the input's exposure time. It does not
// touch the Excluded/selection state. Per-item decisions and warnings are logged
// concisely. After calling this, a caller may still override NormalizeExposure
// for individual inputs (e.g. from the review dialog).
func ApplyExposureNormalization(inputs []Input, mode NormalizationMode) {
	for i := range inputs {
		normalize, scale := resolveNormalization(mode, inputs[i].BUnit, inputs[i].ExposureTime)
		inputs[i].NormalizeExposure = normalize
		inputs[i].ExposureScale = scale
		if mode != NormOff && !normalize && exptimeUnusable(inputs[i].ExposureTime) {
			debuglog.Log(fmt.Sprintf("exposure norm: %s skipped (invalid EXPTIME=%v, BUNIT=%q)",
				InputKey(inputs[i]), inputs[i].ExposureTime, inputs[i].BUnit))
			continue
		}
		debuglog.Log(fmt.Sprintf("exposure norm: %s EXPTIME=%v BUNIT=%q normalize=%t scale=%.6g",
			InputKey(inputs[i]), inputs[i].ExposureTime, inputs[i].BUnit, normalize, scale))
	}
}

func exptimeUnusable(exptime float64) bool {
	_, ok := exposureScaleFor(exptime)
	return !ok
}
