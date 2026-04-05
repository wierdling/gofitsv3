package fitsio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// Header represents a FITS header with key/value/comments.
type Header struct {
	Cards map[string]string
}

// HDU holds a single Header/Data unit.
type HDU struct {
	Header  Header
	Data    ImageData
	ExtName string
}

// ImageData stores the raw pixel data normalized to float32.
type ImageData struct {
	Width  int
	Height int
	Pixels []float32
}

// File holds all HDUs read from a FITS file.
type File struct {
	HDUs []HDU
}

// LoadFile reads a FITS file and returns a parsed representation.
func LoadFile(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	var hdus []HDU

	for {
		hdr, headerBytes, err := readHeader(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		_ = headerBytes

		hdu, dataBytes, err := readImage(reader, hdr)
		if err != nil {
			return nil, err
		}
		hdus = append(hdus, hdu)

		if err := skipPadding(reader, dataBytes); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	if len(hdus) == 0 {
		return nil, fmt.Errorf("no HDUs read from %s", path)
	}
	return &File{HDUs: hdus}, nil
}

func (f *File) SelectSCI() []HDU {
	var sci []HDU
	for _, h := range f.HDUs {
		if strings.HasPrefix(strings.ToUpper(h.ExtName), "SCI") {
			sci = append(sci, h)
		}
	}
	return sci
}

func (f *File) GetHDU(name string) *HDU {
	target := strings.ToUpper(name)
	for i := range f.HDUs {
		if strings.ToUpper(f.HDUs[i].ExtName) == target {
			return &f.HDUs[i]
		}
	}
	return nil
}

func (f *File) SelectDQ() *HDU {
	return f.GetHDU("DQ")
}

func readHeader(r *bufio.Reader) (Header, int, error) {
	cards := make(map[string]string)
	cardCount := 0
	for {
		card := make([]byte, 80)
		if _, err := io.ReadFull(r, card); err != nil {
			return Header{}, 0, err
		}
		cardCount++
		text := string(card)
		key := strings.TrimSpace(text[:8])
		if key == "END" {
			break
		}

		value := ""
		if idx := strings.Index(text, "="); idx >= 0 {
			value = strings.TrimSpace(text[idx+1:])
		} else if len(text) > 10 {
			value = strings.TrimSpace(text[10:])
		}
		cards[key] = value
	}
	headerBytes := cardCount * 80
	if pad := padding(headerBytes); pad > 0 {
		if _, err := io.CopyN(io.Discard, r, int64(pad)); err != nil {
			return Header{}, 0, err
		}
		headerBytes += pad
	}
	return Header{Cards: cards}, headerBytes, nil
}

func readImage(r *bufio.Reader, hdr Header) (HDU, int, error) {
	bitpix := parseInt(hdr.Cards["BITPIX"])
	naxis := parseInt(hdr.Cards["NAXIS"])
	if naxis < 2 {
		return HDU{Header: hdr}, 0, nil
	}
	width := parseInt(hdr.Cards["NAXIS1"])
	height := parseInt(hdr.Cards["NAXIS2"])

	total := width * height
	pixels := make([]float32, total)
	dataBytes := 0

	switch bitpix {
	case 8:
		buf := make([]byte, total)
		if _, err := io.ReadFull(r, buf); err != nil {
			return HDU{}, 0, err
		}
		dataBytes = len(buf)
		for i, b := range buf {
			pixels[i] = float32(b)
		}
	case 16:
		buf := make([]int16, total)
		if err := binary.Read(r, binary.BigEndian, buf); err != nil {
			return HDU{}, 0, err
		}
		dataBytes = len(buf) * 2
		for i, v := range buf {
			pixels[i] = float32(v)
		}
	case 32:
		buf := make([]int32, total)
		if err := binary.Read(r, binary.BigEndian, buf); err != nil {
			return HDU{}, 0, err
		}
		dataBytes = len(buf) * 4
		for i, v := range buf {
			pixels[i] = float32(v)
		}
	case -32:
		buf := make([]float32, total)
		if err := binary.Read(r, binary.BigEndian, buf); err != nil {
			return HDU{}, 0, err
		}
		dataBytes = len(buf) * 4
		copy(pixels, buf)
	case -64:
		buf := make([]float64, total)
		if err := binary.Read(r, binary.BigEndian, buf); err != nil {
			return HDU{}, 0, err
		}
		dataBytes = len(buf) * 8
		for i, v := range buf {
			pixels[i] = float32(v)
		}
	default:
		return HDU{}, 0, fmt.Errorf("unsupported BITPIX %d", bitpix)
	}

	hdu := HDU{Header: hdr, Data: ImageData{Width: width, Height: height, Pixels: pixels}}
	if ext, ok := hdr.Cards["EXTNAME"]; ok {
		hdu.ExtName = strings.Trim(ext, " '=")
	}
	return hdu, dataBytes, nil
}

func padding(n int) int {
	if n%2880 == 0 {
		return 0
	}
	return 2880 - (n % 2880)
}

func skipPadding(r *bufio.Reader, bytesRead int) error {
	if pad := padding(bytesRead); pad > 0 {
		_, err := io.CopyN(io.Discard, r, int64(pad))
		return err
	}
	return nil
}

func parseInt(val string) int {
	var i int
	fmt.Sscanf(val, "%d", &i)
	return i
}

func (img ImageData) Normalize() ImageData {
	min, max := math.MaxFloat64, -math.MaxFloat64
	for _, v := range img.Pixels {
		fv := float64(v)
		if fv < min {
			min = fv
		}
		if fv > max {
			max = fv
		}
	}
	span := max - min
	if span == 0 {
		span = 1
	}
	out := make([]float32, len(img.Pixels))
	for i, v := range img.Pixels {
		out[i] = float32((float64(v) - min) / span)
	}
	return ImageData{Width: img.Width, Height: img.Height, Pixels: out}
}

func (img ImageData) ToRGBA() []byte {
	buf := make([]byte, img.Width*img.Height*4)
	for i, v := range img.Pixels {
		b := byte(clamp01(float64(v)) * 255)
		idx := i * 4
		buf[idx] = b
		buf[idx+1] = b
		buf[idx+2] = b
		buf[idx+3] = 255
	}
	return buf
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
