package fitsio

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// WriteFloat32Image writes a primary-HDU FITS image using float32 pixels.
func WriteFloat32Image(path string, header Header, data ImageData) error {
	return WriteFloat32ImageWithExtensions(path, header, data)
}

// ImageExtension describes a float32 IMAGE extension appended after the primary
// HDU. The structural cards (XTENSION, BITPIX, NAXIS*, PCOUNT, GCOUNT, EXTNAME)
// are derived from ExtName and Data and override anything in Header.
type ImageExtension struct {
	ExtName string
	Header  Header
	Data    ImageData
}

// WriteFloat32ImageWithExtensions writes a primary-HDU float32 FITS image
// followed by zero or more float32 IMAGE extensions (e.g. a WHT weight plane).
func WriteFloat32ImageWithExtensions(path string, header Header, data ImageData, exts ...ImageExtension) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	bw := bufio.NewWriter(f)
	if err := writeHeader(bw, header, data.Width, data.Height); err != nil {
		return err
	}
	if err := writeFloat32Data(bw, data.Pixels); err != nil {
		return err
	}
	for _, ext := range exts {
		if err := writeExtensionHeader(bw, ext); err != nil {
			return err
		}
		if err := writeFloat32Data(bw, ext.Data.Pixels); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func CloneHeader(header Header) Header {
	cloned := Header{Cards: make(map[string]string, len(header.Cards))}
	for key, value := range header.Cards {
		cloned.Cards[key] = value
	}
	return cloned
}

func HeaderFloat(header Header, key string) (float64, bool) {
	raw, ok := header.Cards[key]
	if !ok {
		return 0, false
	}
	value := raw
	if idx := strings.Index(value, "/"); idx >= 0 {
		value = value[:idx]
	}
	value = strings.TrimSpace(strings.Trim(value, "'"))
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func writeHeader(w *bufio.Writer, header Header, width, height int) error {
	cards := make(map[string]string, len(header.Cards)+6)
	for key, value := range header.Cards {
		cards[key] = value
	}
	cards["SIMPLE"] = "T"
	cards["BITPIX"] = "-32"
	cards["NAXIS"] = "2"
	cards["NAXIS1"] = strconv.Itoa(width)
	cards["NAXIS2"] = strconv.Itoa(height)
	cards["EXTEND"] = "T"
	delete(cards, "END")

	return emitHeaderCards(w, cards, []string{"SIMPLE", "BITPIX", "NAXIS", "NAXIS1", "NAXIS2", "EXTEND"})
}

func writeExtensionHeader(w *bufio.Writer, ext ImageExtension) error {
	cards := make(map[string]string, len(ext.Header.Cards)+8)
	for key, value := range ext.Header.Cards {
		cards[key] = value
	}
	cards["XTENSION"] = formatFitsString("IMAGE")
	cards["BITPIX"] = "-32"
	cards["NAXIS"] = "2"
	cards["NAXIS1"] = strconv.Itoa(ext.Data.Width)
	cards["NAXIS2"] = strconv.Itoa(ext.Data.Height)
	cards["PCOUNT"] = "0"
	cards["GCOUNT"] = "1"
	cards["EXTNAME"] = formatFitsString(ext.ExtName)
	delete(cards, "SIMPLE")
	delete(cards, "EXTEND")
	delete(cards, "END")

	return emitHeaderCards(w, cards, []string{"XTENSION", "BITPIX", "NAXIS", "NAXIS1", "NAXIS2", "PCOUNT", "GCOUNT", "EXTNAME"})
}

// emitHeaderCards writes the required cards (in the given order) followed by any
// remaining cards sorted alphabetically, then an END card padded to a 2880-byte
// block boundary.
func emitHeaderCards(w *bufio.Writer, cards map[string]string, ordered []string) error {
	seen := make(map[string]bool, len(ordered))
	for _, key := range ordered {
		seen[key] = true
	}

	extra := make([]string, 0, len(cards))
	for key := range cards {
		if seen[key] {
			continue
		}
		extra = append(extra, key)
	}
	sort.Strings(extra)

	keys := append(append([]string{}, ordered...), extra...)

	cardCount := 0
	for _, key := range keys {
		if err := writeCard(w, key, cards[key]); err != nil {
			return err
		}
		cardCount++
	}
	if _, err := w.WriteString(formatEndCard()); err != nil {
		return err
	}
	cardCount++

	headerBytes := cardCount * 80
	if pad := padding(headerBytes); pad > 0 {
		_, err := w.Write(make([]byte, pad))
		return err
	}
	return nil
}

func writeFloat32Data(w *bufio.Writer, pixels []float32) error {
	if err := binary.Write(w, binary.BigEndian, pixels); err != nil {
		return err
	}
	dataBytes := len(pixels) * 4
	if pad := padding(dataBytes); pad > 0 {
		_, err := w.Write(make([]byte, pad))
		return err
	}
	return nil
}

func writeCard(w *bufio.Writer, key, value string) error {
	_, err := w.WriteString(formatHeaderCard(key, value))
	return err
}

func formatHeaderCard(key, value string) string {
	line := fmt.Sprintf("%-8s= %s", key, value)
	if len(line) > 80 {
		line = line[:80]
	}
	if len(line) < 80 {
		line += strings.Repeat(" ", 80-len(line))
	}
	return line
}

func formatEndCard() string {
	return "END" + strings.Repeat(" ", 77)
}

// formatFitsString renders a FITS string value: single-quoted and padded to at
// least 8 characters, matching how the reader stores and parses string cards.
func formatFitsString(s string) string {
	if len(s) < 8 {
		s += strings.Repeat(" ", 8-len(s))
	}
	return "'" + s + "'"
}
