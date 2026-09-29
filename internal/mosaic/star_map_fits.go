package mosaic

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

// SaveStarMapFITS writes standard, padded FITS HDUs to a sibling temporary file,
// flushes and closes it, then publishes it using the existing safe replacement.
// STARS is a binary table; INPUTS holds the full JSON provenance in a binary
// table string cell to preserve paths and transforms without truncation.
func SaveStarMapFITS(ctx context.Context, path string, product *StarMapProduct, pixels []float32) error {
	if product == nil || product.Map == nil {
		return fmt.Errorf("no star map")
	}
	if _, err := os.Stat(path); err == nil {
		existing, err := fitsio.LoadPrimaryHeader(path)
		if err != nil {
			return err
		}
		if fitsio.HeaderString(existing, "PRODUCT") != "STARMAP" {
			return fmt.Errorf("refusing to replace a non-star-map file: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := checkStarMapInputs(product.Inputs); err != nil {
		return err
	}
	mask, labels, flags, err := product.Map.Rasterize(ctx, pixels)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".starmap-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	bw := bufio.NewWriterSize(f, 65536)
	hdr := starMapHeader(product.Header)
	hdr["PRODUCT"] = "'STARMAP'"
	hdr["SMAPVER"] = "1"
	hdr["BUNIT"] = "'1'"
	hdr["SMAPMODE"] = starFITSString(product.EvidenceMode)
	hdr["NSOURCE"] = strconv.Itoa(len(product.Map.Sources))
	accepted := 0
	for _, s := range product.Map.Sources {
		if s.Accepted() {
			accepted++
		}
	}
	hdr["NACCEPT"] = strconv.Itoa(accepted)
	hdr["PSFFWHM"] = fmt.Sprintf("%.8E", product.Map.FWHM)
	hdr["MINSNR"] = fmt.Sprintf("%.8E", product.Map.Options.MinSNR)
	hdr["MAXRESID"] = fmt.Sprintf("%.8E", product.Map.Options.MaxResidual)
	// Bind catalog/manual decisions to this exact science image, including NaNs.
	hash := sha256.New()
	buf := [4]byte{}
	for i, v := range pixels {
		if i%65536 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		binary.BigEndian.PutUint32(buf[:], math.Float32bits(v))
		hash.Write(buf[:])
	}
	hdr["SCIHASH"] = starFITSString(hex.EncodeToString(hash.Sum(nil)))
	w, h := product.Map.Width, product.Map.Height
	if err = writeStarImage(ctx, bw, hdr, "", w, h, mask, nil); err != nil {
		return err
	}
	planeHeader := starMapHeader(product.Header)
	planeHeader["BUNIT"] = "'1'"
	if err = writeStarImage(ctx, bw, planeHeader, "LABELS", w, h, nil, labels); err != nil {
		return err
	}
	flagHeader := starMapHeader(product.Header)
	flagHeader["BUNIT"] = "'1'"
	flagHeader["FLAG1"] = "'Uncovered'"
	flagHeader["FLAG2"] = "'Uncertain source'"
	flagHeader["FLAG4"] = "'Saturation evidence'"
	flagHeader["FLAG16"] = "'Manual decision'"
	if err = writeStarImage(ctx, bw, flagHeader, "FLAGS", w, h, nil, flags); err != nil {
		return err
	}
	if err = writeStarCatalog(bw, product.Map.Sources); err != nil {
		return err
	}
	provenance := starMapProvenance{Algorithm: "local-moffat-v1", Inputs: product.Inputs, EvidenceMode: product.EvidenceMode, Warnings: product.Warnings, EmpiricalProfile: product.Map.Empirical, ReferencePath: product.ReferencePath, ReferenceHeader: product.ReferenceHeader, Geometry: product.Geometry}

	data, err := json.Marshal(provenance)
	if err != nil {
		return err
	}
	cards := map[string]string{"XTENSION": "'BINTABLE'", "BITPIX": "8", "NAXIS": "2", "NAXIS1": strconv.Itoa(len(data)), "NAXIS2": "1", "PCOUNT": "0", "GCOUNT": "1", "TFIELDS": "1", "EXTNAME": "'INPUTS'", "TTYPE1": "'JSON'", "TFORM1": starFITSString(fmt.Sprintf("%dA", len(data)))}
	if err = writeStarHeader(bw, cards, []string{"XTENSION", "BITPIX", "NAXIS", "NAXIS1", "NAXIS2", "PCOUNT", "GCOUNT", "TFIELDS"}); err != nil {
		return err
	}
	if _, err = bw.Write(data); err != nil {
		return err
	}
	if err = starPad(bw, len(data), 0); err != nil {
		return err
	}
	if err = bw.Flush(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err := checkStarMapInputs(product.Inputs); err != nil {
		return err
	}
	return atomicReplaceWorkingFile(tmp, path)
}

func starFITSString(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
func starMapHeader(h fitsio.Header) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"CTYPE1", "CTYPE2", "CUNIT1", "CUNIT2", "CRPIX1", "CRPIX2", "CRVAL1", "CRVAL2", "CD1_1", "CD1_2", "CD2_1", "CD2_2", "PC1_1", "PC1_2", "PC2_1", "PC2_2", "CDELT1", "CDELT2", "RADESYS", "EQUINOX", "LONPOLE", "LATPOLE", "WCSAXES", "FILTER"} {
		if _, ok := h.Cards[k]; !ok {
			continue
		}
		if v, ok := fitsio.HeaderFloat(h, k); ok {
			out[k] = strconv.FormatFloat(v, 'E', 14, 64)
		} else {
			out[k] = starFITSString(fitsio.HeaderString(h, k))
		}
	}
	// Output masks support a linear TAN grid; retaining a SIP CTYPE without its
	// coefficients would be wrong, so callers must supply mosaic geometry.
	return out
}
func writeStarHeader(w io.Writer, cards map[string]string, order []string) error {
	keys := append([]string{}, order...)
	seen := map[string]bool{}
	for _, k := range order {
		seen[k] = true
	}
	extra := []string{}
	for k := range cards {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)
	for _, k := range keys {
		v := cards[k]
		if len(k) > 8 || len(v) > 70 {
			return fmt.Errorf("FITS card %s too long", k)
		}
		var line string
		if strings.HasPrefix(v, "'") {
			line = fmt.Sprintf("%-8s= %-70s", k, v)
		} else {
			line = fmt.Sprintf("%-8s= %20s", k, v)
			line += strings.Repeat(" ", 80-len(line))
		}
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, "END"+strings.Repeat(" ", 77)); err != nil {
		return err
	}
	return starPad(w, (len(keys)+1)*80, ' ')
}
func starPad(w io.Writer, n int, b byte) error {
	pad := (2880 - n%2880) % 2880
	data := make([]byte, pad)
	for i := range data {
		data[i] = b
	}
	_, err := w.Write(data)
	return err
}
func writeStarImage(ctx context.Context, w io.Writer, base map[string]string, name string, width, height int, p []float32, ints []int32) error {
	c := map[string]string{}
	for k, v := range base {
		c[k] = v
	}
	c["BITPIX"] = "-32"
	if ints != nil {
		c["BITPIX"] = "32"
	}
	c["NAXIS"] = "2"
	c["NAXIS1"] = strconv.Itoa(width)
	c["NAXIS2"] = strconv.Itoa(height)
	order := []string{"SIMPLE", "BITPIX", "NAXIS", "NAXIS1", "NAXIS2"}
	if name == "" {
		c["SIMPLE"] = "T"
		c["EXTEND"] = "T"
	} else {
		c["XTENSION"] = "'IMAGE   '"
		c["PCOUNT"] = "0"
		c["GCOUNT"] = "1"
		c["EXTNAME"] = starFITSString(name)
		order = []string{"XTENSION", "BITPIX", "NAXIS", "NAXIS1", "NAXIS2", "PCOUNT", "GCOUNT"}
	}
	if err := writeStarHeader(w, c, order); err != nil {
		return err
	}
	const chunk = 16384
	buf := make([]byte, chunk*4)
	for off := 0; off < width*height; off += chunk {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(chunk, width*height-off)
		for j := 0; j < n; j++ {
			var v uint32
			if ints != nil {
				v = uint32(ints[off+j])
			} else {
				v = math.Float32bits(p[off+j])
			}
			binary.BigEndian.PutUint32(buf[j*4:], v)
		}
		if _, err := w.Write(buf[:n*4]); err != nil {
			return err
		}
	}
	return starPad(w, width*height*4, 0)
}
func writeStarCatalog(w io.Writer, sources []processing.StarMapSource) error {
	names := []string{"ID", "X", "Y", "FWHM", "RADIUS", "SNR", "RESIDUAL", "AMPLITUDE", "STATUS", "REASON", "SATURATED", "USABLE", "CONFIRMED", "OVERRIDE"}
	forms := []string{"1J", "1D", "1D", "1D", "1D", "1D", "1D", "1D", "12A", "80A", "1L", "1J", "1J", "8A"}
	const rowSize = 4 + 7*8 + 12 + 80 + 1 + 4 + 4 + 8
	c := map[string]string{"XTENSION": "'BINTABLE'", "BITPIX": "8", "NAXIS": "2", "NAXIS1": strconv.Itoa(rowSize), "NAXIS2": strconv.Itoa(len(sources)), "PCOUNT": "0", "GCOUNT": "1", "TFIELDS": strconv.Itoa(len(names)), "EXTNAME": "'STARS'", "COORDSYS": "'FITS 1-based pixels'"}
	for i, n := range names {
		c[fmt.Sprintf("TTYPE%d", i+1)] = starFITSString(n)
		c[fmt.Sprintf("TFORM%d", i+1)] = starFITSString(forms[i])
	}
	if err := writeStarHeader(w, c, []string{"XTENSION", "BITPIX", "NAXIS", "NAXIS1", "NAXIS2", "PCOUNT", "GCOUNT", "TFIELDS"}); err != nil {
		return err
	}
	for _, s := range sources {
		row := make([]byte, rowSize)
		binary.BigEndian.PutUint32(row, uint32(s.ID))
		off := 4
		for _, v := range []float64{s.X + 1, s.Y + 1, s.FWHM, s.Radius, s.SNR, s.Residual, s.Amplitude} {
			binary.BigEndian.PutUint64(row[off:], math.Float64bits(v))
			off += 8
		}
		for _, v := range []struct {
			text string
			n    int
		}{{s.Status, 12}, {s.Reason, 80}} {
			for j := 0; j < v.n; j++ {
				row[off+j] = ' '
			}
			copy(row[off:off+v.n], v.text)
			off += v.n
		}
		row[off] = 'F'
		if s.Saturated {
			row[off] = 'T'
		}
		off++
		binary.BigEndian.PutUint32(row[off:], uint32(s.Usable))
		off += 4
		binary.BigEndian.PutUint32(row[off:], uint32(s.Confirmed))
		off += 4
		for j := 0; j < 8; j++ {
			row[off+j] = ' '
		}
		copy(row[off:], s.Override)
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	return starPad(w, len(sources)*rowSize, 0)
}

func checkStarMapInputs(inputs []StarMapInputRecord) error {
	for _, in := range inputs {
		st, err := os.Stat(in.Path)
		if err != nil {
			return err
		}
		if st.Size() != in.Size || st.ModTime().UnixNano() != in.Modified {
			return fmt.Errorf("star-map evidence changed: %s; regenerate the map", in.Path)
		}
	}
	return nil
}

// LoadStarMapFITS reopens a review product only for the exact original science
// pixels. It never treats a star mask as a science exposure.
func LoadStarMapFITS(ctx context.Context, path string, science fitsio.ImageData, scienceHeader fitsio.Header) (*StarMapProduct, error) {
	f, err := fitsio.LoadFile(path)
	if err != nil {
		return nil, err
	}
	h := f.HDUs[0]
	if fitsio.HeaderString(h.Header, "PRODUCT") != "STARMAP" || fitsio.HeaderString(h.Header, "SMAPVER") != "1" {
		return nil, fmt.Errorf("unsupported star-map product")
	}
	if h.Data.Width != science.Width || h.Data.Height != science.Height || len(science.Pixels) != science.Width*science.Height {
		return nil, fmt.Errorf("star map and science dimensions differ")
	}
	if !maps.Equal(starMapHeader(h.Header), starMapHeader(scienceHeader)) {
		return nil, fmt.Errorf("science grid or filter changed; regenerate the star map")
	}
	hash := sha256.New()
	buf := [4]byte{}
	for i, v := range science.Pixels {
		if i%65536 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		binary.BigEndian.PutUint32(buf[:], math.Float32bits(v))
		hash.Write(buf[:])
	}
	if hex.EncodeToString(hash.Sum(nil)) != fitsio.HeaderString(h.Header, "SCIHASH") {
		return nil, fmt.Errorf("science pixels changed; regenerate the star map")
	}
	catalog := f.GetHDU("STARS")
	provenance := f.GetHDU("INPUTS")
	if catalog == nil || provenance == nil {
		return nil, fmt.Errorf("missing star-map catalog/provenance")
	}
	const rowSize = 4 + 7*8 + 12 + 80 + 1 + 4 + 4 + 8
	if catalog.Data.Width != rowSize || len(catalog.Data.Pixels) != rowSize*catalog.Data.Height {
		return nil, fmt.Errorf("invalid star catalog layout")
	}
	p := &StarMapProduct{Header: h.Header, EvidenceMode: fitsio.HeaderString(h.Header, "SMAPMODE"), Map: &processing.StarMap{Width: science.Width, Height: science.Height, Options: processing.DefaultStarMapOptions()}}
	p.Map.FWHM, _ = fitsio.HeaderFloat(h.Header, "PSFFWHM")
	p.Map.Options.MinSNR, _ = fitsio.HeaderFloat(h.Header, "MINSNR")
	p.Map.Options.MaxResidual, _ = fitsio.HeaderFloat(h.Header, "MAXRESID")
	for row := 0; row < catalog.Data.Height; row++ {
		b := make([]byte, rowSize)
		for j := range b {
			b[j] = byte(catalog.Data.Pixels[row*rowSize+j])
		}
		s := processing.StarMapSource{ID: int(binary.BigEndian.Uint32(b))}
		off := 4
		values := make([]float64, 7)
		for j := range values {
			values[j] = math.Float64frombits(binary.BigEndian.Uint64(b[off:]))
			off += 8
		}
		s.X, s.Y, s.FWHM, s.Radius, s.SNR, s.Residual, s.Amplitude = values[0]-1, values[1]-1, values[2], values[3], values[4], values[5], values[6]
		s.Status = strings.TrimSpace(string(b[off : off+12]))
		off += 12
		s.Reason = strings.TrimSpace(string(b[off : off+80]))
		off += 80
		s.Saturated = b[off] == 'T'
		off++
		s.Usable = int(binary.BigEndian.Uint32(b[off:]))
		off += 4
		s.Confirmed = int(binary.BigEndian.Uint32(b[off:]))
		off += 4
		s.Override = strings.TrimSpace(string(b[off:]))
		p.Map.Sources = append(p.Map.Sources, s)
	}
	b := make([]byte, len(provenance.Data.Pixels))
	for i, v := range provenance.Data.Pixels {
		b[i] = byte(v)
	}
	var record starMapProvenance
	if err = json.Unmarshal(b, &record); err != nil {
		return nil, err
	}
	p.Inputs, p.Warnings, p.Map.Empirical = record.Inputs, record.Warnings, record.EmpiricalProfile
	p.ReferencePath, p.ReferenceHeader, p.Geometry = record.ReferencePath, record.ReferenceHeader, record.Geometry
	if err = checkStarMapInputs(p.Inputs); err != nil {
		return nil, err
	}
	return p, nil
}

type starMapProvenance struct {
	Algorithm        string
	Inputs           []StarMapInputRecord
	EvidenceMode     string
	Warnings         []string
	EmpiricalProfile []float64
	ReferencePath    string
	ReferenceHeader  fitsio.Header
	Geometry         MaskOutputGeometry
}
