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

func HeaderString(header Header, keys ...string) string {
	for _, key := range keys {
		raw, ok := header.Cards[key]
		if !ok {
			continue
		}
		val := raw
		if idx := strings.Index(val, "/"); idx >= 0 {
			val = val[:idx]
		}
		val = strings.TrimSpace(strings.Trim(strings.TrimSpace(val), "'"))
		if val != "" {
			return val
		}
	}
	return ""
}

// FilterString resolves the best filter name from common HST filter keywords,
// skipping empty, CLEAR, and numeric wheel-position values.
// This handles ACS/WFC3 FILTER keys and WFPC2 FILTNAM keys.
func FilterString(header Header) string {
	for _, key := range []string{"FILTER", "FILTNAM1", "FILTNAM2", "FILTER1", "FILTER2"} {
		val := HeaderString(header, key)
		if val != "" && !isBlankFilterValue(val) {
			return val
		}
	}
	return ""
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
