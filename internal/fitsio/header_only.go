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
		val = strings.TrimSpace(strings.Trim(val, "'"))
		if val != "" {
			return val
		}
	}
	return ""
}
