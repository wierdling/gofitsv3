package fitsio

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// FilterBandClass describes the spectral role of a filter in a colour mix.
type FilterBandClass string

const (
	FilterBandWide     FilterBandClass = "wide"
	FilterBandMedium   FilterBandClass = "medium"
	FilterBandNarrow   FilterBandClass = "narrow"
	FilterBandLongpass FilterBandClass = "long-pass"
	FilterBandUnknown  FilterBandClass = "unknown"
)

// FilterBandpass is the resolved metadata needed by wavelength-aware colour
// mapping. WavelengthNm is zero when a manual value is required.
type FilterBandpass struct {
	Name             string
	WavelengthNm     float64
	Class            FilterBandClass
	Origin           string
	Confidence       string
	UnresolvedReason string
}

var filterBandpassPattern = regexp.MustCompile(`(?i)^F([0-9]+)(W2|LP|W|M|N|L)$`)

// ResolveFilterBandpass resolves a filter from already-loaded FITS headers.
// A valid PHOTPLAM is authoritative; filter-name conventions are used only
// when neither header provides a usable wavelength.
func ResolveFilterBandpass(primary, selectedHDU Header) FilterBandpass {
	// The selected extension describes the actual image being composed. Its
	// operative PUPIL/FILTER therefore wins over the primary header's default;
	// a primary PHOTPLAM remains authoritative for the wavelength itself.
	name := FilterString(selectedHDU)
	if name == "" {
		name = FilterString(primary)
	}

	if wavelength, ok := validPhotplam(primary); ok {
		return FilterBandpass{
			Name: name, WavelengthNm: wavelength, Class: classifyFilterName(name),
			Origin: "PHOTPLAM", Confidence: "authoritative",
		}
	}
	if wavelength, ok := validPhotplam(selectedHDU); ok {
		return FilterBandpass{
			Name: name, WavelengthNm: wavelength, Class: classifyFilterName(name),
			Origin: "selected-HDU PHOTPLAM", Confidence: "authoritative",
		}
	}

	result := FilterBandpass{Name: name, Class: classifyFilterName(name)}
	if name == "" {
		result.Origin = "unresolved"
		result.Confidence = "none"
		result.UnresolvedReason = "no filter name or valid PHOTPLAM was found; enter a manual wavelength"
		return result
	}

	match := filterBandpassPattern.FindStringSubmatch(strings.TrimSpace(name))
	if match == nil {
		result.Origin = "unresolved"
		result.Confidence = "none"
		result.UnresolvedReason = "filter name is not a supported F<digits><band> pattern; enter a manual wavelength"
		return result
	}
	if result.Class == FilterBandLongpass {
		result.Origin = "filter name"
		result.Confidence = "classification only"
		result.UnresolvedReason = "long-pass name provides only a cutoff, not an effective wavelength; enter a manual wavelength"
		return result
	}

	scale, recognized := filterWavelengthScale(primary, selectedHDU)
	if !recognized {
		result.Origin = "unresolved"
		result.Confidence = "none"
		result.UnresolvedReason = "instrument convention is ambiguous; enter a manual wavelength"
		return result
	}
	numeric, err := strconv.ParseFloat(match[1], 64)
	if err != nil || numeric <= 0 || math.IsInf(numeric*scale, 0) {
		// The regexp and ParseFloat make this practically unreachable, but keep
		// the result safe if the accepted pattern is expanded later.
		result.Origin = "unresolved"
		result.Confidence = "none"
		result.UnresolvedReason = "filter wavelength is invalid; enter a manual wavelength"
		return result
	}
	result.WavelengthNm = numeric * scale
	result.Origin = "filter name"
	result.Confidence = "instrument convention"
	return result
}

func validPhotplam(header Header) (float64, bool) {
	wavelength, ok := HeaderFloat(header, "PHOTPLAM")
	if !ok || math.IsNaN(wavelength) || math.IsInf(wavelength, 0) || wavelength <= 0 {
		return 0, false
	}
	return wavelength / 10, true
}

func classifyFilterName(name string) FilterBandClass {
	match := filterBandpassPattern.FindStringSubmatch(strings.TrimSpace(name))
	if match == nil {
		return FilterBandUnknown
	}
	switch strings.ToUpper(match[2]) {
	case "W", "W2":
		return FilterBandWide
	case "M":
		return FilterBandMedium
	case "N":
		return FilterBandNarrow
	case "L", "LP":
		return FilterBandLongpass
	default:
		return FilterBandUnknown
	}
}

func filterWavelengthScale(primary, selected Header) (float64, bool) {
	tele := strings.ToUpper(HeaderString(primary, "TELESCOP"))
	if tele == "" {
		tele = strings.ToUpper(HeaderString(selected, "TELESCOP"))
	}
	instrument := strings.ToUpper(HeaderString(primary, "INSTRUME"))
	if instrument == "" {
		instrument = strings.ToUpper(HeaderString(selected, "INSTRUME"))
	}
	detector := strings.ToUpper(HeaderString(primary, "DETECTOR"))
	if detector == "" {
		detector = strings.ToUpper(HeaderString(selected, "DETECTOR"))
	}
	if tele == "JWST" || strings.Contains(instrument, "NIRCAM") || strings.Contains(instrument, "NIRISS") || strings.Contains(instrument, "MIRI") {
		return 10, true
	}
	if strings.Contains(instrument, "WFC3") {
		if strings.Contains(detector, "IR") || strings.Contains(instrument, "/IR") || strings.HasSuffix(instrument, " IR") {
			return 10, true
		}
		if strings.Contains(detector, "UVIS") || strings.Contains(detector, "UV") {
			return 1, true
		}
		return 0, false
	}
	if strings.Contains(instrument, "NICMOS") {
		return 10, true
	}
	if tele == "HST" && (strings.Contains(instrument, "ACS") || strings.Contains(instrument, "WFPC") || strings.Contains(instrument, "STIS") || strings.Contains(instrument, "COS") || strings.Contains(instrument, "FOC") || strings.Contains(instrument, "HSP")) {
		return 1, true
	}
	return 0, false
}
