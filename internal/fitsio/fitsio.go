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
	// Int32Pixels retains exact signed 32-bit image samples for bit-mask
	// extensions such as DQ and CTX. Pixels is still populated for existing
	// consumers, but must not be used to round-trip mask bits.
	Int32Pixels []int32
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

	reader := bufio.NewReaderSize(f, 64*1024)
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

// LoadFileMetadata reads every HDU's header but decodes pixel data only for
// small data units (e.g. D2IMARR distortion tables, which the build planner
// needs). Large science arrays (SCI/ERR/DQ) are skipped with seeks, so each
// HDU's dimensions and WCS are available without the memory or I/O cost of its
// pixels. Skipped HDUs carry Width/Height (from NAXISn) but nil Pixels.
//
// It walks HDU boundaries exactly as LoadFile does (NAXIS1*NAXIS2*|BITPIX|/8
// per data unit, padded to 2880), so any file LoadFile parses, this parses too.
func LoadFileMetadata(path string) (*File, error) {
	// Decode data units up to this size (distortion tables are a few KiB); skip
	// anything larger (the science arrays we want to stream on demand instead).
	const decodeLimit = 1 << 20
	return loadFileFiltered(path, func(hdr Header) bool {
		return imageDataBytes(hdr) <= decodeLimit
	})
}

// LoadFileSelective reads every HDU's header but decodes pixel data only for
// image HDUs where decode(hdr) returns true. Skipped HDUs carry Width/Height
// (from NAXISn) but nil Pixels. This lets callers read just the extensions they
// need (e.g. a single SCI chip and its DQ) without paying the memory or I/O cost
// of the rest of the file.
func LoadFileSelective(path string, decode func(hdr Header) bool) (*File, error) {
	return loadFileFiltered(path, decode)
}

// loadFileFiltered walks every HDU header and decodes a data unit only when it
// has image data (NAXIS>=2, supported BITPIX) and decode(hdr) returns true. It
// underpins LoadFileMetadata and LoadFileSelective.
func loadFileFiltered(path string, decode func(hdr Header) bool) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var hdus []HDU
	var pos int64
	for {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
		hdr, headerBytes, err := readHeader(bufio.NewReaderSize(f, 64*1024))
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		pos += int64(headerBytes)

		dataBytes := imageDataBytes(hdr)
		if dataBytes > 0 && decode(hdr) {
			if _, err := f.Seek(pos, io.SeekStart); err != nil {
				return nil, err
			}
			hdu, _, err := readImage(bufio.NewReaderSize(f, 64*1024), hdr)
			if err != nil {
				return nil, err
			}
			hdus = append(hdus, hdu)
		} else {
			hdu := HDU{Header: hdr, ExtName: HeaderString(hdr, "EXTNAME")}
			if parseInt(hdr.Cards["NAXIS"]) >= 2 {
				hdu.Data = ImageData{
					Width:  parseInt(hdr.Cards["NAXIS1"]),
					Height: parseInt(hdr.Cards["NAXIS2"]),
				}
			}
			hdus = append(hdus, hdu)
		}
		pos += int64(dataBytes + padding(dataBytes))
	}
	if len(hdus) == 0 {
		return nil, fmt.Errorf("no HDUs read from %s", path)
	}
	return &File{HDUs: hdus}, nil
}

// imageDataBytes returns the byte size of an HDU's data unit using the same
// 2-D NAXIS1*NAXIS2*|BITPIX|/8 convention readImage consumes, so LoadFileMetadata
// stays byte-for-byte aligned with LoadFile. Returns 0 for non-image HDUs and
// unsupported BITPIX values.
func imageDataBytes(hdr Header) int {
	if parseInt(hdr.Cards["NAXIS"]) < 2 {
		return 0
	}
	var bpp int
	switch parseInt(hdr.Cards["BITPIX"]) {
	case 8:
		bpp = 1
	case 16:
		bpp = 2
	case 32, -32:
		bpp = 4
	case -64:
		bpp = 8
	default:
		return 0
	}
	return parseInt(hdr.Cards["NAXIS1"]) * parseInt(hdr.Cards["NAXIS2"]) * bpp
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

// GetHDUByExtVer returns the first HDU matching name and EXTVER card value.
// If extver is empty, it falls back to the first HDU matching name.
func (f *File) GetHDUByExtVer(name, extver string) *HDU {
	target := strings.ToUpper(name)
	for i := range f.HDUs {
		h := &f.HDUs[i]
		if strings.ToUpper(h.ExtName) != target {
			continue
		}
		if extver == "" {
			return h
		}
		if HeaderString(h.Header, "EXTVER") == strings.TrimSpace(extver) {
			return h
		}
	}
	return nil
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

	bytesPerPixel := 0
	switch bitpix {
	case 8:
		bytesPerPixel = 1
	case 16:
		bytesPerPixel = 2
	case 32, -32:
		bytesPerPixel = 4
	case -64:
		bytesPerPixel = 8
	default:
		return HDU{}, 0, fmt.Errorf("unsupported BITPIX %d", bitpix)
	}

	// Read the raw pixel block once, then decode straight into the float32
	// output in a single pass. This avoids binary.Read's reflection-free but
	// still allocation-heavy path (a typed intermediate slice plus its own
	// full-size byte buffer) and the extra element-by-element conversion loop,
	// roughly halving both transient memory and decode work per HDU.
	dataBytes := total * bytesPerPixel
	raw := make([]byte, dataBytes)
	if _, err := io.ReadFull(r, raw); err != nil {
		return HDU{}, 0, err
	}

	pixels := make([]float32, total)
	switch bitpix {
	case 8:
		for i, b := range raw {
			pixels[i] = float32(b)
		}
	case 16:
		for i := 0; i < total; i++ {
			pixels[i] = float32(int16(binary.BigEndian.Uint16(raw[i*2 : i*2+2])))
		}
	case 32:
		for i := 0; i < total; i++ {
			pixels[i] = float32(int32(binary.BigEndian.Uint32(raw[i*4 : i*4+4])))
		}
	case -32:
		for i := 0; i < total; i++ {
			pixels[i] = math.Float32frombits(binary.BigEndian.Uint32(raw[i*4 : i*4+4]))
		}
	case -64:
		for i := 0; i < total; i++ {
			pixels[i] = float32(math.Float64frombits(binary.BigEndian.Uint64(raw[i*8 : i*8+8])))
		}
	}

	data := ImageData{Width: width, Height: height, Pixels: pixels}
	if bitpix == 32 {
		data.Int32Pixels = make([]int32, total)
		for i := range data.Int32Pixels {
			data.Int32Pixels[i] = int32(binary.BigEndian.Uint32(raw[i*4 : i*4+4]))
		}
	}
	hdu := HDU{Header: hdr, Data: data}
	hdu.ExtName = HeaderString(hdr, "EXTNAME")
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
