package fitsio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
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

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	var hdus []HDU
	var pos int64

	for {
		hdr, headerBytes, err := readHeader(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		pos += int64(headerBytes)
		end, err := dataUnitEnd(hdr, pos, info.Size())
		if err != nil {
			if errors.Is(err, errTruncatedDataUnit) && len(hdus) > 0 {
				break
			}
			return nil, err
		}

		hdu, dataBytes, err := readImage(reader, hdr)
		if err != nil {
			return nil, err
		}
		hdus = append(hdus, hdu)

		// Skip the rest of the data unit (later cube planes, table rows and
		// heap) plus its padding. Missing final padding is tolerated.
		if _, err := io.CopyN(io.Discard, reader, end-pos-int64(dataBytes)); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		pos = end
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
// It walks HDU boundaries exactly as LoadFile does (the full data unit from
// hduDataBytes, padded to 2880), so any file LoadFile parses, this parses too.
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
	return LoadFileSelectiveIndexed(path, func(_ int, hdr Header) bool { return decode(hdr) })
}

// LoadFileSelectiveIndexed is the positional variant of LoadFileSelective.
// index is the zero-based HDU number, allowing formats with repeated or
// unusable EXTVER values (notably raw Gemini GMOS) to select one chip without
// decoding sibling image arrays.
func LoadFileSelectiveIndexed(path string, decode func(index int, hdr Header) bool) (*File, error) {
	return loadFileFilteredIndexed(path, decode)
}

// loadFileFiltered walks every HDU header and decodes a data unit only when it
// has image data (NAXIS>=2, supported BITPIX) and decode(hdr) returns true. It
// underpins LoadFileMetadata and LoadFileSelective.
func loadFileFiltered(path string, decode func(hdr Header) bool) (*File, error) {
	return loadFileFilteredIndexed(path, func(_ int, hdr Header) bool { return decode(hdr) })
}

func loadFileFilteredIndexed(path string, decode func(index int, hdr Header) bool) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var hdus []HDU
	var pos int64
	for index := 0; ; index++ {
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

		dataBytes, sizeErr := checkedImageDataBytes(hdr)
		if sizeErr != nil {
			return nil, sizeErr
		}
		end, err := dataUnitEnd(hdr, pos, info.Size())
		if err != nil {
			if errors.Is(err, errTruncatedDataUnit) && len(hdus) > 0 {
				break
			}
			return nil, err
		}
		if dataBytes > 0 && decode(index, hdr) {
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
		pos = end
	}
	if len(hdus) == 0 {
		return nil, fmt.Errorf("no HDUs read from %s", path)
	}
	return &File{HDUs: hdus}, nil
}

// imageDataBytes returns the byte size of the first 2-D image plane
// (NAXIS1*NAXIS2*|BITPIX|/8), which is what readImage decodes. For a BINTABLE
// that is the main table as raw bytes (the star-map reader relies on this);
// its heap is not decoded. Returns 0 for NAXIS<2 and unsupported BITPIX values.
// Walkers advance by hduDataBytes instead, which covers the whole data unit.
func imageDataBytes(hdr Header) int {
	bytes, _ := checkedImageDataBytes(hdr)
	return bytes
}

func checkedImageDataBytes(hdr Header) (int, error) {
	if parseInt(hdr.Cards["NAXIS"]) < 2 {
		return 0, nil
	}
	total, err := hduDataBytes(hdr)
	if err != nil {
		return 0, err
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
		return 0, nil
	}
	width, height := parseInt(hdr.Cards["NAXIS1"]), parseInt(hdr.Cards["NAXIS2"])
	if width <= 0 || height <= 0 {
		return 0, fmt.Errorf("invalid image dimensions %dx%d", width, height)
	}
	maxInt := int(^uint(0) >> 1)
	if width > maxInt/height {
		return 0, fmt.Errorf("image dimensions overflow: %dx%d", width, height)
	}
	pixels := width * height
	if pixels > maxInt/bpp {
		return 0, fmt.Errorf("image byte size overflow for %dx%d", width, height)
	}
	dataBytes := pixels * bpp
	if dataBytes > maxInt-padding(dataBytes) {
		return 0, fmt.Errorf("padded image byte size overflow for %dx%d", width, height)
	}
	if total < dataBytes {
		// A zero-length axis beyond NAXIS2 (or GCOUNT=0): no plane to decode.
		return 0, nil
	}
	return dataBytes, nil
}

// hduDataBytes returns the unpadded size of an HDU's whole data unit (FITS
// 4.0 §4.4.1): |BITPIX|/8 * GCOUNT * (PCOUNT + NAXIS1*...*NAXISn), so cubes,
// tables and heaps are skipped correctly. PCOUNT/GCOUNT default to 0/1, as
// for the primary HDU and IMAGE extensions. Random groups (GROUPS=T,
// NAXIS1=0) omit NAXIS1 from the product.
func hduDataBytes(hdr Header) (int, error) {
	naxis := parseInt(hdr.Cards["NAXIS"])
	if naxis < 0 || naxis > 999 {
		return 0, fmt.Errorf("invalid NAXIS %d", naxis)
	}
	if naxis == 0 {
		return 0, nil
	}
	bitpix := parseInt(hdr.Cards["BITPIX"])
	switch bitpix {
	case 8, 16, 32, 64, -32, -64:
	default:
		return 0, fmt.Errorf("invalid BITPIX %d", bitpix)
	}
	const maxInt = int(^uint(0) >> 1)
	first := 1
	if parseInt(hdr.Cards["NAXIS1"]) == 0 && HeaderString(hdr, "GROUPS") == "T" {
		first = 2
	}
	n := 1
	for k := first; k <= naxis; k++ {
		v := parseInt(hdr.Cards["NAXIS"+strconv.Itoa(k)])
		if v < 0 {
			return 0, fmt.Errorf("invalid NAXIS%d %d", k, v)
		}
		if v != 0 && n > maxInt/v {
			return 0, fmt.Errorf("data size overflow at NAXIS%d", k)
		}
		n *= v
	}
	pcount, gcount := parseInt(hdr.Cards["PCOUNT"]), 1
	if _, ok := hdr.Cards["GCOUNT"]; ok {
		gcount = parseInt(hdr.Cards["GCOUNT"])
	}
	if pcount < 0 || gcount < 0 {
		return 0, fmt.Errorf("invalid PCOUNT %d / GCOUNT %d", pcount, gcount)
	}
	if n > maxInt-pcount {
		return 0, errors.New("data size overflow at PCOUNT")
	}
	n += pcount
	bpp := abs(bitpix) / 8
	if gcount != 0 && n > maxInt/gcount/bpp {
		return 0, errors.New("data size overflow at GCOUNT")
	}
	dataBytes := n * gcount * bpp
	if dataBytes > maxInt-padding(dataBytes) {
		return 0, errors.New("padded data size overflow")
	}
	return dataBytes, nil
}

// hduDataBytesOrZero is hduDataBytes with invalid headers treated as empty.
func hduDataBytesOrZero(hdr Header) int {
	n, _ := hduDataBytes(hdr)
	return n
}

// dataUnitEnd returns the offset just past the padded data unit that starts
// at dataOffset, after checking the unpadded data fits in a file of fileSize
// bytes. Callers run it before decoding, so a corrupt header cannot request
// allocations larger than the file could hold.
func dataUnitEnd(hdr Header, dataOffset, fileSize int64) (int64, error) {
	unit, err := hduDataBytes(hdr)
	if err != nil {
		return 0, err
	}
	if int64(unit) > fileSize-dataOffset {
		return 0, fmt.Errorf("%w: HDU data (%d bytes) exceeds remaining file size (%d bytes)", errTruncatedDataUnit, unit, fileSize-dataOffset)
	}
	return dataOffset + int64(unit+padding(unit)), nil
}

// errTruncatedDataUnit marks an HDU whose data runs past the end of the file.
// Whole-file walkers keep the complete HDUs before it instead of failing.
var errTruncatedDataUnit = errors.New("truncated FITS data unit")

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
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
			// EOF on a card boundary is a clean end of file only before the
			// first card; inside a header it means the file was truncated.
			if cardCount > 0 && errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return Header{}, 0, err
		}
		if cardCount == 0 && isFillCard(card) {
			// Zero or blank fill after the last HDU is not another header.
			return Header{}, 0, io.EOF
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
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return Header{}, 0, err
		}
		headerBytes += pad
	}
	return Header{Cards: cards}, headerBytes, nil
}

func isFillCard(card []byte) bool {
	for _, b := range card {
		if b != 0 && b != ' ' {
			return false
		}
	}
	return true
}

func readImage(r *bufio.Reader, hdr Header) (HDU, int, error) {
	bitpix := parseInt(hdr.Cards["BITPIX"])
	naxis := parseInt(hdr.Cards["NAXIS"])
	if naxis < 2 {
		return HDU{Header: hdr}, 0, nil
	}
	dataBytes, err := checkedImageDataBytes(hdr)
	if err != nil {
		return HDU{}, 0, err
	}
	if dataBytes == 0 && hduDataBytesOrZero(hdr) == 0 {
		// An empty data unit (a zero-length axis or GCOUNT=0) has no plane to
		// decode; return the header alone rather than reading past raw.
		return HDU{Header: hdr, ExtName: HeaderString(hdr, "EXTNAME")}, 0, nil
	}
	width := parseInt(hdr.Cards["NAXIS1"])
	height := parseInt(hdr.Cards["NAXIS2"])
	total := width * height

	switch bitpix {
	case 8:
	case 16:
	case 32, -32:
	case -64:
	default:
		return HDU{}, 0, fmt.Errorf("unsupported BITPIX %d", bitpix)
	}

	// Read the raw pixel block once, then decode straight into the float32
	// output in a single pass. This avoids binary.Read's reflection-free but
	// still allocation-heavy path (a typed intermediate slice plus its own
	// full-size byte buffer) and the extra element-by-element conversion loop,
	// roughly halving both transient memory and decode work per HDU.
	raw := make([]byte, dataBytes)
	if _, err := io.ReadFull(r, raw); err != nil {
		return HDU{}, 0, err
	}

	scale := parseSampleScale(hdr)
	pixels := make([]float32, total)
	decodeRow(pixels, raw, bitpix, scale)

	data := ImageData{Width: width, Height: height, Pixels: pixels}
	if bitpix == 32 {
		data.Int32Pixels = make([]int32, total)
		for i := range data.Int32Pixels {
			data.Int32Pixels[i] = scale.exactInt32(raw[i*4:])
		}
	}
	hdu := HDU{Header: hdr, Data: data}
	hdu.ExtName = HeaderString(hdr, "EXTNAME")
	return hdu, dataBytes, nil
}

// sampleScale maps stored integer samples to physical values (FITS 4.0
// §5.3): physical = BZERO + BSCALE*stored, with BLANK samples mapped to NaN.
// Floating-point BITPIX is decoded unscaled, exactly as before.
type sampleScale struct {
	scale, zero float64
	blank       int64
	hasBlank    bool
}

// parseSampleScale reads BSCALE/BZERO/BLANK once per HDU.
func parseSampleScale(hdr Header) sampleScale {
	s := sampleScale{scale: 1}
	if parseInt(hdr.Cards["BITPIX"]) < 0 {
		return s
	}
	if v, ok := HeaderFloat(hdr, "BSCALE"); ok {
		s.scale = v
	}
	if v, ok := HeaderFloat(hdr, "BZERO"); ok {
		s.zero = v
	}
	if raw, ok := hdr.Cards["BLANK"]; ok {
		if v, err := strconv.ParseInt(parseCardValue(raw), 10, 64); err == nil {
			s.blank, s.hasBlank = v, true
		}
	}
	return s
}

func (s sampleScale) apply(v int64) float32 {
	if s.hasBlank && v == s.blank {
		return float32(math.NaN())
	}
	if s.scale == 1 && s.zero == 0 {
		return float32(v)
	}
	return float32(float64(v)*s.scale + s.zero)
}

// exactInt32 returns a BITPIX=32 sample as exact mask bits. Unsigned 32-bit
// data (BSCALE=1, BZERO=2^31) yields its uint32 bit pattern; any other
// scaling keeps the stored value.
func (s sampleScale) exactInt32(raw []byte) int32 {
	v := binary.BigEndian.Uint32(raw)
	if s.scale == 1 && s.zero == 1<<31 {
		v ^= 1 << 31
	}
	return int32(v)
}

// decodeRow decodes len(dst) big-endian samples from raw, applying s to
// integer BITPIX.
func decodeRow(dst []float32, raw []byte, bitpix int, s sampleScale) {
	switch bitpix {
	case 8:
		for i := range dst {
			dst[i] = s.apply(int64(raw[i]))
		}
	case 16:
		for i := range dst {
			dst[i] = s.apply(int64(int16(binary.BigEndian.Uint16(raw[i*2:]))))
		}
	case 32:
		for i := range dst {
			dst[i] = s.apply(int64(int32(binary.BigEndian.Uint32(raw[i*4:]))))
		}
	case -32:
		for i := range dst {
			dst[i] = math.Float32frombits(binary.BigEndian.Uint32(raw[i*4:]))
		}
	case -64:
		for i := range dst {
			dst[i] = float32(math.Float64frombits(binary.BigEndian.Uint64(raw[i*8:])))
		}
	}
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
