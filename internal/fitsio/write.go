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
	if err := validatePrimaryData(data); err != nil {
		return err
	}
	for i, ext := range exts {
		if err := validateExtensionData(ext.Data); err != nil {
			return fmt.Errorf("extension %d (%s): %w", i, ext.ExtName, err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	bw := bufio.NewWriter(f)
	if err := writeHeader(bw, header, data.Width, data.Height); err != nil {
		return err
	}
	if err := writeImageData(bw, data); err != nil {
		return err
	}
	for _, ext := range exts {
		if err := writeExtensionHeader(bw, ext); err != nil {
			return err
		}
		if err := writeImageData(bw, ext.Data); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func validatePrimaryData(data ImageData) error {
	if len(data.Pixels) == 0 && len(data.Int32Pixels) == 0 {
		if data.Width == 0 && data.Height == 0 {
			return nil // metadata-only primary HDU
		}
		return fmt.Errorf("metadata-only primary requires zero dimensions, got %dx%d", data.Width, data.Height)
	}
	return validateFloatData(data)
}

func validateExtensionData(data ImageData) error {
	if data.Width <= 0 || data.Height <= 0 {
		return fmt.Errorf("invalid image dimensions %dx%d", data.Width, data.Height)
	}
	if data.Width > int(^uint(0)>>1)/data.Height {
		return fmt.Errorf("image dimensions overflow: %dx%d", data.Width, data.Height)
	}
	total := data.Width * data.Height
	if len(data.Int32Pixels) > 0 {
		if len(data.Int32Pixels) != total {
			return fmt.Errorf("int32 pixel count %d does not match %dx%d", len(data.Int32Pixels), data.Width, data.Height)
		}
		if len(data.Pixels) != 0 && len(data.Pixels) != total {
			return fmt.Errorf("float32 pixel count %d does not match %dx%d", len(data.Pixels), data.Width, data.Height)
		}
		return nil
	}
	return validateFloatData(data)
}

func validateFloatData(data ImageData) error {
	if data.Width <= 0 || data.Height <= 0 {
		return fmt.Errorf("invalid image dimensions %dx%d", data.Width, data.Height)
	}
	if data.Width > int(^uint(0)>>1)/data.Height {
		return fmt.Errorf("image dimensions overflow: %dx%d", data.Width, data.Height)
	}
	total := data.Width * data.Height
	if len(data.Pixels) != total {
		return fmt.Errorf("float32 pixel count %d does not match %dx%d", len(data.Pixels), data.Width, data.Height)
	}
	return nil
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
	if width > 0 && height > 0 {
		cards["BITPIX"] = "-32"
		cards["NAXIS"] = "2"
		cards["NAXIS1"] = strconv.Itoa(width)
		cards["NAXIS2"] = strconv.Itoa(height)
	} else {
		// A metadata-only primary HDU is common in multi-extension FITS files.
		cards["BITPIX"] = "8"
		cards["NAXIS"] = "0"
		delete(cards, "NAXIS1")
		delete(cards, "NAXIS2")
	}
	cards["EXTEND"] = "T"
	delete(cards, "END")

	ordered := []string{"SIMPLE", "BITPIX", "NAXIS"}
	if width > 0 && height > 0 {
		ordered = append(ordered, "NAXIS1", "NAXIS2")
	}
	ordered = append(ordered, "EXTEND")
	return emitHeaderCards(w, cards, ordered)
}

func writeExtensionHeader(w *bufio.Writer, ext ImageExtension) error {
	cards := make(map[string]string, len(ext.Header.Cards)+8)
	for key, value := range ext.Header.Cards {
		cards[key] = value
	}
	cards["XTENSION"] = formatFitsString("IMAGE")
	if len(ext.Data.Int32Pixels) > 0 {
		cards["BITPIX"] = "32"
	} else {
		cards["BITPIX"] = "-32"
	}
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

func writeImageData(w *bufio.Writer, data ImageData) error {
	if len(data.Int32Pixels) > 0 {
		if len(data.Int32Pixels) != data.Width*data.Height {
			return fmt.Errorf("int32 pixel count %d does not match %dx%d", len(data.Int32Pixels), data.Width, data.Height)
		}
		if err := binary.Write(w, binary.BigEndian, data.Int32Pixels); err != nil {
			return err
		}
		if pad := padding(len(data.Int32Pixels) * 4); pad > 0 {
			_, err := w.Write(make([]byte, pad))
			return err
		}
		return nil
	}
	return writeFloat32Data(w, data.Pixels)
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
