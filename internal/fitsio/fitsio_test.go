package fitsio

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestReadHeaderParsesCardsAndConsumesPadding(t *testing.T) {
	data := append(headerBlock(
		cardLine("SIMPLE", "T"),
		cardLine("BITPIX", "-32"),
		cardLine("EXTNAME", "'SCI' / science extension"),
	), []byte("tail")...)

	header, bytesRead, err := readHeader(bufio.NewReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatalf("readHeader error = %v", err)
	}
	if bytesRead != 2880 {
		t.Fatalf("bytesRead = %d, want 2880", bytesRead)
	}
	if got := header.Cards["SIMPLE"]; got != "T" {
		t.Fatalf("SIMPLE = %q, want T", got)
	}
	if got := header.Cards["EXTNAME"]; got != "'SCI' / science extension" {
		t.Fatalf("EXTNAME = %q", got)
	}
}

func TestReadImageSupportsBitpixModesAndExtName(t *testing.T) {
	tests := []struct {
		name    string
		bitpix  string
		values  any
		want    []float32
		extName string
	}{
		{name: "uint8", bitpix: "8", values: []byte{1, 2, 255, 0}, want: []float32{1, 2, 255, 0}},
		{name: "int16", bitpix: "16", values: []int16{-1, 2, 3, 4}, want: []float32{-1, 2, 3, 4}},
		{name: "int32", bitpix: "32", values: []int32{-1, 2, 3, 4}, want: []float32{-1, 2, 3, 4}},
		{name: "float32", bitpix: "-32", values: []float32{1.5, 2.5, 3.5, 4.5}, want: []float32{1.5, 2.5, 3.5, 4.5}, extName: "SCI"},
		{name: "float64", bitpix: "-64", values: []float64{1.25, 2.25, 3.25, 4.25}, want: []float32{1.25, 2.25, 3.25, 4.25}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := Header{Cards: map[string]string{
				"BITPIX":  tt.bitpix,
				"NAXIS":   "2",
				"NAXIS1":  "2",
				"NAXIS2":  "2",
				"EXTNAME": "'" + tt.extName + "'",
			}}
			reader := bufio.NewReader(bytes.NewReader(mustEncodeBigEndian(t, tt.values)))
			hdu, dataBytes, err := readImage(reader, header)
			if err != nil {
				t.Fatalf("readImage error = %v", err)
			}
			if dataBytes != len(tt.want)*bitpixBytes(tt.bitpix) {
				t.Fatalf("dataBytes = %d", dataBytes)
			}
			if hdu.Data.Width != 2 || hdu.Data.Height != 2 {
				t.Fatalf("dimensions = %dx%d", hdu.Data.Width, hdu.Data.Height)
			}
			if len(hdu.Data.Pixels) != len(tt.want) {
				t.Fatalf("len(Pixels) = %d, want %d", len(hdu.Data.Pixels), len(tt.want))
			}
			for i, want := range tt.want {
				if hdu.Data.Pixels[i] != want {
					t.Fatalf("pixel[%d] = %v, want %v", i, hdu.Data.Pixels[i], want)
				}
			}
			if tt.extName != "" && hdu.ExtName != tt.extName {
				t.Fatalf("ExtName = %q, want %q", hdu.ExtName, tt.extName)
			}
		})
	}
}

func TestReadImageRejectsUnsupportedBitpixAndShortRead(t *testing.T) {
	t.Run("unsupported bitpix", func(t *testing.T) {
		header := Header{Cards: map[string]string{"BITPIX": "99", "NAXIS": "2", "NAXIS1": "1", "NAXIS2": "1"}}
		if _, _, err := readImage(bufio.NewReader(bytes.NewReader(nil)), header); err == nil {
			t.Fatal("expected unsupported BITPIX error")
		}
	})

	t.Run("short read", func(t *testing.T) {
		header := Header{Cards: map[string]string{"BITPIX": "-32", "NAXIS": "2", "NAXIS1": "2", "NAXIS2": "2"}}
		if _, _, err := readImage(bufio.NewReader(bytes.NewReader([]byte{1, 2, 3})), header); err == nil {
			t.Fatal("expected short read error")
		}
	})
}

func TestLoadFileParsesSyntheticFileAndSelectors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic.fits")
	data := buildFITSFile(
		fitsHDU("SCI", "1", 2, 2, []float32{1, 2, 3, 4}, nil),
		fitsHDU("DQ", "1", 2, 2, []int16{0, 1, 0, 1}, nil),
		fitsHDU("SCI", "2", 1, 2, []float32{9, 8}, map[string]string{"OBJECT": "'Nebula'"}),
	)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	file, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile error = %v", err)
	}
	if len(file.HDUs) != 3 {
		t.Fatalf("len(HDUs) = %d, want 3", len(file.HDUs))
	}
	sci := file.SelectSCI()
	if len(sci) != 2 {
		t.Fatalf("len(SelectSCI) = %d, want 2", len(sci))
	}
	if dq := file.SelectDQ(); dq == nil || dq.ExtName != "DQ" {
		t.Fatalf("SelectDQ = %#v, want DQ HDU", dq)
	}
	if got := file.GetHDU("sci"); got == nil || got.ExtName != "SCI" {
		t.Fatalf("GetHDU(sci) = %#v", got)
	}
	if got := file.GetHDUByExtVer("SCI", "2"); got == nil || got.Header.Cards["OBJECT"] != "'Nebula'" {
		t.Fatalf("GetHDUByExtVer(SCI,2) = %#v", got)
	}
	if got := file.GetHDUByExtVer("SCI", ""); got == nil || got.Header.Cards["EXTVER"] != "1" {
		t.Fatalf("GetHDUByExtVer fallback = %#v", got)
	}
	if file.GetHDU("ERR") != nil {
		t.Fatal("GetHDU(ERR) should return nil")
	}
}

func TestLoadFileNormalizesExtNameWithComment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "commented_extname.fits")
	data := buildFITSFile(
		headerBlock(
			cardLine("SIMPLE", "T"),
			cardLine("BITPIX", "8"),
			cardLine("NAXIS", "0"),
		),
		headerBlock(
			cardLine("XTENSION", "'IMAGE'"),
			cardLine("BITPIX", "16"),
			cardLine("NAXIS", "2"),
			cardLine("NAXIS1", "1"),
			cardLine("NAXIS2", "1"),
			cardLine("EXTNAME", "'DQ      '           / Extension name"),
			cardLine("EXTVER", "1                   / Extension version"),
		),
		mustEncodeBigEndian(t, []int16{1}),
		make([]byte, padding(2)),
	)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	file, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile error = %v", err)
	}
	if got := file.HDUs[1].ExtName; got != "DQ" {
		t.Fatalf("ExtName = %q, want DQ", got)
	}
	if file.GetHDUByExtVer("DQ", "1") == nil {
		t.Fatal("GetHDUByExtVer(DQ,1) = nil, want normalized DQ extension")
	}
}

func TestLoadFileRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.fits")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "no HDUs read") {
		t.Fatalf("LoadFile error = %v, want no HDUs read", err)
	}
}

func TestSkipPaddingConsumesExpectedBytes(t *testing.T) {
	reader := bufio.NewReader(bytes.NewReader(append([]byte("abc"), bytes.Repeat([]byte{0}, padding(3))...)))
	buf := make([]byte, 3)
	if _, err := reader.Read(buf); err != nil {
		t.Fatalf("Read error = %v", err)
	}
	if err := skipPadding(reader, 3); err != nil {
		t.Fatalf("skipPadding error = %v", err)
	}
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("expected reader to be exhausted after skipping padding")
	}
}

func TestImageDataNormalizeAndToRGBA(t *testing.T) {
	img := ImageData{Width: 2, Height: 2, Pixels: []float32{2, 4, 6, 8}}
	norm := img.Normalize()
	wantNorm := []float32{0, 1.0 / 3.0, 2.0 / 3.0, 1}
	for i, want := range wantNorm {
		if math.Abs(float64(norm.Pixels[i]-want)) > 1e-6 {
			t.Fatalf("norm[%d] = %v, want %v", i, norm.Pixels[i], want)
		}
	}

	rgba := ImageData{Width: 2, Height: 2, Pixels: []float32{-1, 0.5, 2, 0}}.ToRGBA()
	wantRGBA := []byte{
		0, 0, 0, 255,
		127, 127, 127, 255,
		255, 255, 255, 255,
		0, 0, 0, 255,
	}
	if !bytes.Equal(rgba, wantRGBA) {
		t.Fatalf("ToRGBA = %v, want %v", rgba, wantRGBA)
	}
}

func TestCloneHeaderAndHeaderFloat(t *testing.T) {
	header := Header{Cards: map[string]string{
		"EXPTIME":  "1200.5 / seconds",
		"PIXSCALE": "'0.04'",
		"EMPTY":    "   ",
	}}
	cloned := CloneHeader(header)
	cloned.Cards["EXPTIME"] = "15"
	if header.Cards["EXPTIME"] != "1200.5 / seconds" {
		t.Fatal("CloneHeader should not mutate original map")
	}

	if got, ok := HeaderFloat(header, "EXPTIME"); !ok || got != 1200.5 {
		t.Fatalf("HeaderFloat(EXPTIME) = %v,%v", got, ok)
	}
	if got, ok := HeaderFloat(header, "PIXSCALE"); !ok || got != 0.04 {
		t.Fatalf("HeaderFloat(PIXSCALE) = %v,%v", got, ok)
	}
	if _, ok := HeaderFloat(header, "EMPTY"); ok {
		t.Fatal("HeaderFloat(EMPTY) should fail")
	}
	if _, ok := HeaderFloat(header, "MISSING"); ok {
		t.Fatal("HeaderFloat(MISSING) should fail")
	}
}

func TestFilterStringUsesWFPC2FilterNamesBeforeNumericWheelPositions(t *testing.T) {
	header := Header{Cards: map[string]string{
		"FILTNAM1": "'F555W   '           / first filter name",
		"FILTNAM2": "'        '           / second filter name",
		"FILTER1":  "25                  / first filter number",
		"FILTER2":  "0                   / second filter number",
	}}
	if got := FilterString(header); got != "F555W" {
		t.Fatalf("FilterString = %q, want F555W", got)
	}
}

func TestFilterStringSkipsClearAndNumericValues(t *testing.T) {
	header := Header{Cards: map[string]string{
		"FILTER":  "'CLEAR1L'",
		"FILTER1": "25",
		"FILTER2": "'F814W'",
	}}
	if got := FilterString(header); got != "F814W" {
		t.Fatalf("FilterString = %q, want F814W", got)
	}
}

func TestWriteFloat32ImageRoundTripAndHeaderFormatting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roundtrip.fits")
	header := Header{Cards: map[string]string{
		"OBJECT": "'Nebula'",
		"CRPIX1": "42.5",
	}}
	data := ImageData{Width: 2, Height: 2, Pixels: []float32{1, 2, 3, 4}}
	if err := WriteFloat32Image(path, header, data); err != nil {
		t.Fatalf("WriteFloat32Image error = %v", err)
	}

	file, err := LoadFile(path)
	if err != nil {
		t.Fatalf("round-trip LoadFile error = %v", err)
	}
	if len(file.HDUs) != 1 {
		t.Fatalf("len(HDUs) = %d, want 1", len(file.HDUs))
	}
	if file.HDUs[0].Header.Cards["OBJECT"] != "'Nebula'" {
		t.Fatalf("OBJECT = %q", file.HDUs[0].Header.Cards["OBJECT"])
	}
	if file.HDUs[0].Header.Cards["BITPIX"] != "-32" {
		t.Fatalf("BITPIX = %q, want -32", file.HDUs[0].Header.Cards["BITPIX"])
	}
	if len(file.HDUs[0].Data.Pixels) != 4 || file.HDUs[0].Data.Pixels[3] != 4 {
		t.Fatalf("round-trip pixels = %v", file.HDUs[0].Data.Pixels)
	}

	card := formatHeaderCard("OBJECT", "'Nebula'")
	if len(card) != 80 {
		t.Fatalf("formatHeaderCard len = %d, want 80", len(card))
	}
	if !strings.HasPrefix(card, "OBJECT  = 'Nebula'") {
		t.Fatalf("formatHeaderCard prefix = %q", card[:20])
	}
	if got := formatEndCard(); len(got) != 80 || !strings.HasPrefix(got, "END") {
		t.Fatalf("formatEndCard invalid")
	}
}

func TestWriteFloat32ImageCreateError(t *testing.T) {
	err := WriteFloat32Image(t.TempDir(), Header{}, ImageData{Width: 1, Height: 1, Pixels: []float32{1}})
	if err == nil {
		t.Fatal("expected create error")
	}
}

// TestLoadRealFits verifies we can parse HST/JWST FITS headers and data for sample files.
func TestLoadRealFits(t *testing.T) {
	base := filepath.Join("..", "..", "TestImages")
	paths := []string{
		filepath.Join(base, "ick909030_drc.fits"),
		filepath.Join(base, "ick909030_drz.fits"),
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				t.Skipf("sample FITS file not present: %s", p)
			}
			t.Fatalf("stat %s: %v", p, err)
		}
		f, err := LoadFile(p)
		if err != nil {
			t.Fatalf("load %s: %v", p, err)
		}
		if len(f.HDUs) == 0 {
			t.Fatalf("no HDUs for %s", p)
		}
		sci := f.SelectSCI()
		if len(sci) == 0 {
			t.Fatalf("no SCI extension in %s", p)
		}
		if sci[0].Data.Width == 0 || sci[0].Data.Height == 0 {
			t.Fatalf("empty dimensions for %s", p)
		}
	}
}

func buildFITSFile(hdus ...[]byte) []byte {
	var out []byte
	for _, hdu := range hdus {
		out = append(out, hdu...)
	}
	return out
}

func fitsHDU(extName, extver string, width, height int, values any, extra map[string]string) []byte {
	cards := []string{
		cardLine("SIMPLE", "T"),
		cardLine("BITPIX", bitpixValue(values)),
		cardLine("NAXIS", "2"),
		cardLine("NAXIS1", intString(width)),
		cardLine("NAXIS2", intString(height)),
	}
	if extName != "" {
		cards = append(cards, cardLine("EXTNAME", "'"+extName+"'"))
	}
	if extver != "" {
		cards = append(cards, cardLine("EXTVER", extver))
	}
	for key, value := range extra {
		cards = append(cards, cardLine(key, value))
	}
	out := headerBlock(cards...)
	out = append(out, mustEncodeBigEndianValue(values)...)
	if pad := padding(dataLength(values)); pad > 0 {
		out = append(out, make([]byte, pad)...)
	}
	return out
}

func headerBlock(cards ...string) []byte {
	all := append([]string{}, cards...)
	all = append(all, "END"+strings.Repeat(" ", 77))
	var buf bytes.Buffer
	for _, card := range all {
		buf.WriteString(card)
	}
	if pad := padding(buf.Len()); pad > 0 {
		buf.Write(make([]byte, pad))
	}
	return buf.Bytes()
}

func cardLine(key, value string) string {
	line := key
	if value != "" {
		line = key + strings.Repeat(" ", max(0, 8-len(key))) + "= " + value
	}
	if len(line) > 80 {
		line = line[:80]
	}
	if len(line) < 80 {
		line += strings.Repeat(" ", 80-len(line))
	}
	return line
}

func mustEncodeBigEndian(t *testing.T, values any) []byte {
	t.Helper()
	return mustEncodeBigEndianValue(values)
}

func mustEncodeBigEndianValue(values any) []byte {
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.BigEndian, values); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func bitpixValue(values any) string {
	switch values.(type) {
	case []byte:
		return "8"
	case []int16:
		return "16"
	case []int32:
		return "32"
	case []float32:
		return "-32"
	case []float64:
		return "-64"
	default:
		panic("unsupported values type")
	}
}

func bitpixBytes(bitpix string) int {
	switch bitpix {
	case "8":
		return 1
	case "16":
		return 2
	case "32", "-32":
		return 4
	case "-64":
		return 8
	default:
		return 0
	}
}

func dataLength(values any) int {
	switch v := values.(type) {
	case []byte:
		return len(v)
	case []int16:
		return len(v) * 2
	case []int32:
		return len(v) * 4
	case []float32:
		return len(v) * 4
	case []float64:
		return len(v) * 8
	default:
		panic("unsupported values type")
	}
}

func intString(v int) string {
	return strconv.Itoa(v)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
