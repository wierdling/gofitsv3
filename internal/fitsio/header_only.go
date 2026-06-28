package fitsio

import (
	"bufio"
	"os"
	"strings"
)

// LoadPrimaryHeader reads only the primary FITS header without decoding image data.
func LoadPrimaryHeader(path string) (Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, err
	}
	defer f.Close()

	header, _, err := readHeader(bufio.NewReader(f))
	if err != nil {
		return Header{}, err
	}
	return header, nil
}

// parseCardValue extracts the value from a FITS card's value/comment field,
// stripping the trailing "/ comment". For quoted string values it respects the
// closing quote before looking for the comment delimiter, so a "/" inside the
// string (e.g. BUNIT = 'MJy/sr') is preserved, and it unescapes the FITS
// double-single-quote ('') convention. Unquoted values are cut at the first "/".
func parseCardValue(raw string) string {
	v := strings.TrimLeft(raw, " ")
	if !strings.HasPrefix(v, "'") {
		if idx := strings.Index(v, "/"); idx >= 0 {
			v = v[:idx]
		}
		return strings.TrimSpace(v)
	}

	var b strings.Builder
	for i := 1; i < len(v); i++ {
		if v[i] == '\'' {
			// A doubled quote is an escaped literal quote; otherwise it closes
			// the string and the rest of the card is the comment.
			if i+1 < len(v) && v[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			break
		}
		b.WriteByte(v[i])
	}
	return strings.TrimRight(b.String(), " ")
}

func HeaderString(header Header, keys ...string) string {
	for _, key := range keys {
		raw, ok := header.Cards[key]
		if !ok {
			continue
		}
		val := parseCardValue(raw)
		if val != "" {
			return val
		}
	}
	return ""
}

// FilterString resolves the best filter name from common filter keywords,
// skipping empty, CLEAR, and numeric wheel-position values. This handles ACS/WFC3
// FILTER keys and WFPC2 FILTNAM keys.
//
// For JWST NIRCam/NIRISS the operative bandpass is sometimes in the pupil wheel:
// a medium/narrow filter sits in PUPIL while the FILTER wheel holds a wide
// blocking filter (e.g. FILTER=F150W2, PUPIL=F162M -> effective F162M) or CLEAR.
// When PUPIL holds a real filter name it therefore wins over FILTER; other pupil
// elements (CLEAR, GRISMR, WLP8, MASK*, FLAT, ...) are ignored.
func FilterString(header Header) string {
	if pupil := HeaderString(header, "PUPIL"); looksLikeFilterName(pupil) {
		return pupil
	}
	for _, key := range []string{"FILTER", "FILTNAM1", "FILTNAM2", "FILTER1", "FILTER2"} {
		val := HeaderString(header, key)
		if val != "" && !isBlankFilterValue(val) {
			return val
		}
	}
	return ""
}

// looksLikeFilterName reports whether v is an optical filter designation such as
// "F200W" or "F162M" — an 'F' immediately followed by a digit — as opposed to
// other pupil-wheel elements (CLEAR, GRISMR, WLP8, MASKRND, FLAT, ...).
func looksLikeFilterName(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) < 2 || (v[0] != 'F' && v[0] != 'f') {
		return false
	}
	return v[1] >= '0' && v[1] <= '9'
}

func isBlankFilterValue(val string) bool {
	upper := strings.ToUpper(strings.TrimSpace(val))
	if upper == "" || strings.Contains(upper, "CLEAR") {
		return true
	}
	for _, r := range upper {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
