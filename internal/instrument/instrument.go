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
}

var detectors = map[key]Info{
	{"WFC3", "IR"}:   {PixelScale: 0.128, Chips: 1, ChipInnerTrim: 10, HasSIP: true},
	{"WFC3", "UVIS"}: {PixelScale: 0.0396, Chips: 2, ChipInnerTrim: 10, HasSIP: true},
	{"ACS", "WFC"}:   {PixelScale: 0.05, Chips: 2, ChipInnerTrim: 25, HasSIP: true},
	{"ACS", "HRC"}:   {PixelScale: 0.027, Chips: 1, ChipInnerTrim: 10, HasSIP: true},
	{"ACS", "SBC"}:   {PixelScale: 0.034, Chips: 1, ChipInnerTrim: 10, HasSIP: false},
}

type key struct {
	instrume string
	detector string
}

// Default is used when the instrument is not recognised.
var Default = Info{
	PixelScale:    0.05,
	Chips:         1,
	ChipInnerTrim: 10,
	HasSIP:        false,
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
