// Package instrument provides metadata for HST science instruments.
// It maps INSTRUME+DETECTOR header values to properties used by the
// mosaic and processing pipelines.
package instrument

import (
	"gofitsv3/internal/fitsio"
)

// Info holds pipeline-relevant properties for a specific detector.
type Info struct {
	// PixelScale is the native pixel scale in arcseconds per pixel.
	PixelScale float64
	// Chips is the number of science chips (e.g. 1 for WFC3/IR, 2 for ACS/WFC).
	Chips int
	// ChipInnerTrim is the number of pixels to exclude from each edge of a chip
	// before merging multi-chip exposures. A larger value is needed for detectors
	// with wide inter-chip gaps or noisy boundary rows.
	ChipInnerTrim int
	// HasSIP indicates whether exposures from this detector typically carry SIP
	// distortion coefficients in their headers.
	HasSIP bool
	// BadDQBits is the bitmask of DQ flag values that mark a pixel as bad and
	// should be interpolated over before processing. A value of 0 means any
	// non-zero DQ flag is considered bad (the most conservative default).
	//
	// Bit definitions (HST convention):
	//   4    = permanent bad detector pixel
	//   16   = hot pixel (>0.1 e/s dark current above local mean)
	//   32   = unstable pixel / CTE-tail artifact
	//   64   = warm pixel (0.02–0.1 e/s above local mean)
	//   256  = full-well saturation
	//   512  = bad pixel-to-pixel flat
	//   1024 = charge trap / sink pixel (UVIS) or IR CR spike (IR)
	//   8192 = rejected during image combination / CR rejection
	BadDQBits uint32
}

// dqBits is a convenience shorthand used in the detector table below.
const (
	dqBadDetector uint32 = 4
	dqHot         uint32 = 16
	dqUnstable    uint32 = 32
	dqWarm        uint32 = 64
	dqSaturated   uint32 = 256
	dqBadFlat     uint32 = 512
	dqChargeTrap  uint32 = 1024
	dqCRRejected  uint32 = 8192
)

// wfc3BadDQ is the shared bad-pixel bitmask for WFC3/IR and WFC3/UVIS.
// Warm pixels (64) are omitted: they are mild and the ERR-weighted drizzle
// naturally down-weights them without discarding the flux entirely.
const wfc3BadDQ = dqBadDetector | dqHot | dqUnstable | dqSaturated | dqBadFlat | dqChargeTrap | dqCRRejected

// acsBadDQ is the shared bad-pixel bitmask for ACS detectors.
const acsBadDQ = dqBadDetector | dqHot | dqUnstable | dqSaturated | dqBadFlat | dqChargeTrap | dqCRRejected

// JWST DQ flags use a completely different bit convention from HST (see
// jwst.datamodels.dqflags.pixel). DO_NOT_USE is the master "this pixel is not
// usable" flag the pipeline sets whenever a pixel should be discarded, so it is
// the correct gate for masking/interpolation. Masking on every non-zero DQ bit
// (the HST-style default) would wrongly reject the many usable pixels that
// carry only informational flags.
const (
	jwstDoNotUse uint32 = 1
)

// jwstBadDQ is the bad-pixel bitmask for JWST detectors: only DO_NOT_USE.
const jwstBadDQ = jwstDoNotUse

var detectors = map[key]Info{
	{"WFC3", "IR"}:   {PixelScale: 0.128, Chips: 1, ChipInnerTrim: 10, HasSIP: true, BadDQBits: wfc3BadDQ},
	{"WFC3", "UVIS"}: {PixelScale: 0.0396, Chips: 2, ChipInnerTrim: 10, HasSIP: true, BadDQBits: wfc3BadDQ},
	{"ACS", "WFC"}:   {PixelScale: 0.05, Chips: 2, ChipInnerTrim: 25, HasSIP: true, BadDQBits: acsBadDQ},
	{"ACS", "HRC"}:   {PixelScale: 0.027, Chips: 1, ChipInnerTrim: 10, HasSIP: true, BadDQBits: acsBadDQ},
	{"ACS", "SBC"}:   {PixelScale: 0.034, Chips: 1, ChipInnerTrim: 10, HasSIP: false, BadDQBits: acsBadDQ},
	// WFPC2 FLT files contain four SCI chips. The PC and WF chips have different
	// native scales, so drizzle should prefer per-chip WCS; this fallback scale is
	// only used when WCS scale cannot be read.
	{"WFPC2", "PC"}: {PixelScale: 0.0996, Chips: 4, ChipInnerTrim: 0, HasSIP: false, BadDQBits: 0},
	// JWST MIRI imager (_cal Stage-2 products). Single SCI extension, no
	// inter-chip gap; the cal-file SCI header carries an approximate SIP WCS.
	{"MIRI", "MIRIMAGE"}: {PixelScale: 0.11, Chips: 1, ChipInnerTrim: 0, HasSIP: true, BadDQBits: jwstBadDQ},
	// JWST NIRCam detectors are registered in init() below (one _cal per
	// detector). Short-wave modules (NRCA1-4, NRCB1-4) sample at ~0.031"/px and
	// long-wave (NRCALONG, NRCBLONG) at ~0.063"/px. Native scale is read from the
	// WCS at drizzle time; these entries mainly carry the JWST DQ mask and SIP.
}

func init() {
	sw := Info{PixelScale: 0.031, Chips: 1, ChipInnerTrim: 0, HasSIP: true, BadDQBits: jwstBadDQ}
	lw := Info{PixelScale: 0.063, Chips: 1, ChipInnerTrim: 0, HasSIP: true, BadDQBits: jwstBadDQ}
	for _, d := range []string{"NRCA1", "NRCA2", "NRCA3", "NRCA4", "NRCB1", "NRCB2", "NRCB3", "NRCB4"} {
		detectors[key{"NIRCAM", d}] = sw
	}
	detectors[key{"NIRCAM", "NRCALONG"}] = lw
	detectors[key{"NIRCAM", "NRCBLONG"}] = lw
}

type key struct {
	instrume string
	detector string
}

// Default is used when the instrument is not recognised.
// BadDQBits=0 preserves the original conservative behaviour: any non-zero DQ
// value is treated as bad.
var Default = Info{
	PixelScale:    0.05,
	Chips:         1,
	ChipInnerTrim: 10,
	HasSIP:        false,
	BadDQBits:     0,
}

// ScalePreset names a common instrument configuration and its native plate
// scale, for populating UI menus. PixelScale is in arcseconds per pixel.
type ScalePreset struct {
	Name       string
	PixelScale float64
}

// ScalePresets returns the common instrument plate-scale presets used by the
// drizzle Output Scale menu, ordered finest to coarsest. Values are read from
// the detector table so it stays the single source of truth.
func ScalePresets() []ScalePreset {
	return []ScalePreset{
		{"NIRCam Short", detectors[key{"NIRCAM", "NRCA1"}].PixelScale},
		{"ACS/HRC", detectors[key{"ACS", "HRC"}].PixelScale},
		{"WFC3/UVIS", detectors[key{"WFC3", "UVIS"}].PixelScale},
		{"ACS/WFC", detectors[key{"ACS", "WFC"}].PixelScale},
		{"NIRCam Long", detectors[key{"NIRCAM", "NRCALONG"}].PixelScale},
		{"MIRI", detectors[key{"MIRI", "MIRIMAGE"}].PixelScale},
		{"WFC3/IR", detectors[key{"WFC3", "IR"}].PixelScale},
	}
}

// FromHeader reads INSTRUME and DETECTOR from the provided FITS header and
// returns the matching Info. If the combination is not recognised, Default
// is returned along with ok=false.
func FromHeader(h fitsio.Header) (Info, bool) {
	instrume := fitsio.HeaderString(h, "INSTRUME")
	detector := fitsio.HeaderString(h, "DETECTOR")
	info, ok := detectors[key{instrume, detector}]
	if !ok {
		return Default, false
	}
	return info, true
}
