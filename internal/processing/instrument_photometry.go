package processing

import (
	"fmt"
	"math"
	"strings"

	"gofitsv3/internal/fitsio"
)

// PhotometricReference identifies the common linear reference unit.
type PhotometricReference string

const (
	ReferenceFlambda PhotometricReference = "flambda"
	ReferenceFnu     PhotometricReference = "fnu"
)

type PhotometricInputKind string

const (
	InputCounts            PhotometricInputKind = "counts"
	InputCountRate         PhotometricInputKind = "count-rate"
	InputFlambda           PhotometricInputKind = "flambda"
	InputFnu               PhotometricInputKind = "fnu"
	InputSurfaceBrightness PhotometricInputKind = "surface-brightness"
)

// InstrumentMetadata contains the identity and headers for one image plane.
// SCI keywords are authoritative; primary keywords are used only as fallback.
type InstrumentMetadata struct {
	Telescope, Instrument, Detector, Filter string
	Primary, SCI                            fitsio.Header
	Reference                               PhotometricReference
}

type InstrumentPhotometry struct {
	Telescope, Instrument, Detector, Filter string
	InputKind                               PhotometricInputKind
	BUnit                                   string
	ExposureSeconds                         float64
	PivotWavelengthAngstrom                 float64
	Gain                                    float64 // multiply input pixels to the requested reference unit
	Reference                               PhotometricReference
	SurfaceBrightness                       bool
}

// UnsupportedPhotometryError is returned when metadata cannot be safely
// converted. Callers should surface Reason rather than guessing a conversion.
type UnsupportedPhotometryError struct{ Reason string }

func (e *UnsupportedPhotometryError) Error() string { return "unsupported photometry: " + e.Reason }

func IsUnsupportedPhotometry(err error) bool {
	_, ok := err.(*UnsupportedPhotometryError)
	return ok
}

type instrumentSpec struct {
	telescope, instrument, detector, filter string
}

// This intentionally small registry contains only filters for which the
// standard header calibration keywords are sufficient for this first phase.
var instrumentRegistry = map[instrumentSpec]struct{}{
	{"HST", "ACS", "WFC", "F435W"}: {}, {"HST", "ACS", "WFC", "F606W"}: {}, {"HST", "ACS", "WFC", "F814W"}: {},
	{"HST", "WFC3", "UVIS", "F275W"}: {}, {"HST", "WFC3", "UVIS", "F336W"}: {}, {"HST", "WFC3", "UVIS", "F438W"}: {}, {"HST", "WFC3", "UVIS", "F555W"}: {}, {"HST", "WFC3", "UVIS", "F606W"}: {}, {"HST", "WFC3", "UVIS", "F814W"}: {},
	{"HST", "WFC3", "IR", "F105W"}: {}, {"HST", "WFC3", "IR", "F125W"}: {}, {"HST", "WFC3", "IR", "F160W"}: {},
	{"JWST", "NIRCAM", "NRC", "F090W"}: {}, {"JWST", "NIRCAM", "NRC", "F150W"}: {}, {"JWST", "NIRCAM", "NRC", "F200W"}: {}, {"JWST", "NIRCAM", "NRC", "F277W"}: {}, {"JWST", "NIRCAM", "NRC", "F356W"}: {}, {"JWST", "NIRCAM", "NRC", "F444W"}: {},
	{"JWST", "MIRI", "MIRIMAGE", "F770W"}: {}, {"JWST", "MIRI", "MIRIMAGE", "F1000W"}: {}, {"JWST", "MIRI", "MIRIMAGE", "F1500W"}: {}, {"JWST", "MIRI", "MIRIMAGE", "F1800W"}: {}, {"JWST", "MIRI", "MIRIMAGE", "F2100W"}: {}, {"JWST", "MIRI", "MIRIMAGE", "F2550W"}: {},
}

func ParseInstrumentPhotometry(m InstrumentMetadata) (InstrumentPhotometry, error) {
	tel, inst, det, filter := norm(m.Telescope), norm(m.Instrument), norm(m.Detector), norm(m.Filter)
	if m.Reference == "" {
		m.Reference = ReferenceFnu
	}
	if m.Reference != ReferenceFnu && m.Reference != ReferenceFlambda {
		return InstrumentPhotometry{}, unsupported("unknown reference %q", m.Reference)
	}
	if !supportedInstrument(tel, inst, det, filter) {
		return InstrumentPhotometry{}, unsupported("unknown instrument/filter %s/%s/%s/%s", tel, inst, det, filter)
	}
	bunit := headerString(m, "BUNIT")
	if bunit == "" {
		return InstrumentPhotometry{}, unsupported("missing BUNIT")
	}
	kind, surface, ok := classifyUnit(bunit)
	if !ok {
		return InstrumentPhotometry{}, unsupported("unsupported BUNIT %q", bunit)
	}
	if surface && tel != "JWST" {
		return InstrumentPhotometry{}, unsupported("surface brightness is supported only for JWST")
	}
	if tel == "JWST" && (kind == InputCounts || kind == InputCountRate) {
		return InstrumentPhotometry{}, unsupported("JWST detector count units are not supported")
	}
	if tel == "JWST" && kind == InputFlambda {
		return InstrumentPhotometry{}, unsupported("JWST Flambda units are not supported")
	}
	pivot, hasPivot := photHeaderFloat(m, "PHOTPLAM", "PIVOTW", "PIVOT")
	if (kind == InputCounts || kind == InputCountRate) && !hasPivot {
		return InstrumentPhotometry{}, unsupported("missing PHOTPLAM/pivot wavelength")
	}
	if hasPivot && (!finitePositive(pivot)) {
		return InstrumentPhotometry{}, unsupported("invalid pivot wavelength")
	}
	photflam, hasFlam := photHeaderFloat(m, "PHOTFLAM")
	photfnu, hasFnu := photHeaderFloat(m, "PHOTFNU")
	photmjsr, hasMJSR := photHeaderFloat(m, "PHOTMJSR")
	if hasFlam && !finitePositive(photflam) || hasFnu && !finitePositive(photfnu) || hasMJSR && !finitePositive(photmjsr) {
		return InstrumentPhotometry{}, unsupported("invalid photometric calibration keyword")
	}
	if kind == InputCounts || kind == InputCountRate {
		if !hasFlam && !hasFnu {
			return InstrumentPhotometry{}, unsupported("missing PHOTFLAM or PHOTFNU")
		}
	}
	exptime := 0.0
	if kind == InputCounts {
		var ok bool
		exptime, ok = photHeaderFloat(m, "EXPTIME")
		if !ok || !finitePositive(exptime) {
			return InstrumentPhotometry{}, unsupported("counts require positive EXPTIME")
		}
	}
	if (kind == InputCounts || kind == InputCountRate) && hasFnu && hasFlam {
		// Both are allowed only when they agree at the declared pivot.
		derived := photflam * pivot * pivot / speedOfLightAngstrom * 1e23
		if math.Abs(derived-photfnu) > math.Max(1e-30, math.Abs(photfnu)*1e-6) {
			return InstrumentPhotometry{}, unsupported("contradictory PHOTFLAM and PHOTFNU")
		}
	}
	gain := 1.0
	fnuInputScaleJy := 1.0
	if kind == InputFnu && strings.HasPrefix(strings.ToUpper(strings.ReplaceAll(bunit, " ", "")), "MJY") {
		fnuInputScaleJy = 1e6
	}
	if kind == InputCounts {
		gain /= exptime
	}
	switch kind {
	case InputCounts, InputCountRate:
		if m.Reference == ReferenceFlambda {
			if !hasFlam {
				return InstrumentPhotometry{}, unsupported("PHOTFLAM required for Flambda")
			}
			gain *= photflam
		} else {
			if hasFnu {
				gain *= photfnu
			} else {
				gain *= photflam * pivot * pivot / speedOfLightAngstrom * 1e23
			}
		}
	case InputFlambda:
		if m.Reference == ReferenceFnu {
			if !hasPivot {
				return InstrumentPhotometry{}, unsupported("Flambda to Fnu requires pivot")
			}
			gain = pivot * pivot / speedOfLightAngstrom * 1e23
		}
	case InputFnu:
		gain *= fnuInputScaleJy
		if m.Reference == ReferenceFlambda {
			if !hasPivot {
				return InstrumentPhotometry{}, unsupported("Fnu to Flambda requires pivot")
			}
			gain *= speedOfLightAngstrom / (pivot * pivot) * 1e-23
		}
	case InputSurfaceBrightness:
		// MJy/sr is already calibrated surface brightness. PHOTMJSR is
		// intentionally not applied to such pixels; it is only relevant when
		// converting detector count rates into MJy/sr (a future input kind).
		pixar, ok := photHeaderFloat(m, "PIXAR_SR")
		if !ok || !finitePositive(pixar) {
			return InstrumentPhotometry{}, unsupported("MJy/sr requires positive PIXAR_SR")
		}
		gain = 1e6 * pixar
		if m.Reference == ReferenceFlambda {
			if !hasPivot {
				return InstrumentPhotometry{}, unsupported("MJy/sr to Flambda requires pivot")
			}
			gain *= speedOfLightAngstrom / (pivot * pivot) * 1e-23
		}
	}
	if !finitePositive(gain) {
		return InstrumentPhotometry{}, unsupported("non-finite conversion gain")
	}
	return InstrumentPhotometry{Telescope: tel, Instrument: inst, Detector: det, Filter: filter, InputKind: kind, BUnit: bunit, ExposureSeconds: exptime, PivotWavelengthAngstrom: pivot, Gain: gain, Reference: m.Reference, SurfaceBrightness: surface}, nil
}

const speedOfLightAngstrom = 2.99792458e18

func supportedInstrument(t, i, d, f string) bool {
	if d == "" {
		return false
	}
	if i == "NIRCAM" && strings.HasPrefix(d, "NRC") {
		return registryHas(t, i, "NRC", f)
	}
	return registryHas(t, i, d, f)
}
func registryHas(t, i, d, f string) bool {
	_, ok := instrumentRegistry[instrumentSpec{t, i, d, f}]
	return ok
}
func norm(s string) string {
	return strings.ToUpper(strings.Join(strings.Fields(strings.TrimSpace(s)), " "))
}
func unsupported(format string, args ...any) error {
	return &UnsupportedPhotometryError{Reason: fmt.Sprintf(format, args...)}
}
func finitePositive(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

func headerString(m InstrumentMetadata, key string) string {
	if v := fitsio.HeaderString(m.SCI, key); v != "" {
		if raw, ok := m.SCI.Cards[key]; ok && !strings.HasPrefix(strings.TrimSpace(raw), "'") && strings.Contains(raw, "/") {
			return strings.TrimSpace(raw)
		}
		return strings.TrimSpace(v)
	}
	if v := fitsio.HeaderString(m.Primary, key); v != "" {
		if raw, ok := m.Primary.Cards[key]; ok && !strings.HasPrefix(strings.TrimSpace(raw), "'") && strings.Contains(raw, "/") {
			return strings.TrimSpace(raw)
		}
		return strings.TrimSpace(v)
	}
	return ""
}
func photHeaderFloat(m InstrumentMetadata, keys ...string) (float64, bool) {
	for _, key := range keys {
		if v, ok := fitsio.HeaderFloat(m.SCI, key); ok {
			return v, true
		}
	}
	for _, key := range keys {
		if v, ok := fitsio.HeaderFloat(m.Primary, key); ok {
			return v, true
		}
	}
	return 0, false
}

func classifyUnit(unit string) (PhotometricInputKind, bool, bool) {
	u := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(unit), " ", ""), "^", ""))
	switch u {
	case "COUNTS", "ELECTRONS", "DN", "ADU":
		return InputCounts, false, true
	case "COUNTS/S", "ELECTRONS/S", "DN/S", "ADU/S":
		return InputCountRate, false, true
	case "ERG/S/CM2/A", "ERG/S/CM2/ANGSTROM", "FLAM":
		return InputFlambda, false, true
	case "JY":
		return InputFnu, false, true
	case "MJY":
		return InputFnu, false, true
	case "MJY/SR", "MJY/STERADIAN":
		return InputSurfaceBrightness, true, true
	default:
		return "", false, false
	}
}
