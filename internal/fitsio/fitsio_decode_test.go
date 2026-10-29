package fitsio

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func writeTestFITS(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.fits")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFileAppliesBZEROForUnsigned16(t *testing.T) {
	// Unsigned 16-bit convention: stored = physical - 32768.
	stored := []int16{-32768, -1, 0, 32767}
	want := []float32{0, 32767, 32768, 65535}
	path := writeTestFITS(t, fitsHDU("SCI", "", 2, 2, stored, map[string]string{"BZERO": "32768", "BSCALE": "1"}))

	f, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, "LoadFile", f.HDUs[0].Data.Pixels, want)

	var streamed []float32
	if _, _, err := StreamHDUByIndex(context.Background(), path, 0, func(_ int, values []float32, _ []int32) error {
		streamed = append(streamed, values...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertPixels(t, "StreamHDUByIndex", streamed, want)

	dst := filepath.Join(t.TempDir(), "raw.f32")
	if err := CopySelectedHDUToRawFloat32Artifact(path, "SCI", "", dst); err != nil {
		t.Fatal(err)
	}
	pixels, _, _, err := MaterializeFloat32Artifact(dst)
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, "CopySelectedHDUToRawFloat32Artifact", pixels, want)
}

func TestLoadFileAppliesBSCALE(t *testing.T) {
	path := writeTestFITS(t, fitsHDU("", "", 3, 1, []int32{-2, 0, 10}, map[string]string{"BSCALE": "0.5 / scale", "BZERO": "100.0"}))
	f, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, "LoadFile", f.HDUs[0].Data.Pixels, []float32{99, 100, 105})
	// Int32Pixels keeps stored values for mask bits when scaling is not the
	// unsigned convention.
	if got := f.HDUs[0].Data.Int32Pixels; got[0] != -2 || got[2] != 10 {
		t.Fatalf("Int32Pixels = %v, want stored values", got)
	}
}

func TestLoadFileUnsigned32KeepsMaskBits(t *testing.T) {
	path := writeTestFITS(t, fitsHDU("DQ", "", 2, 1, []int32{math.MinInt32, -1}, map[string]string{"BZERO": "2147483648"}))
	f, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.HDUs[0].Data.Int32Pixels; got[0] != 0 || uint32(got[1]) != 0x7fffffff {
		t.Fatalf("Int32Pixels = %#x, want physical uint32 bits [0 0x7fffffff]", got)
	}
}

func TestLoadFileMapsBLANKToNaN(t *testing.T) {
	path := writeTestFITS(t, fitsHDU("", "", 3, 1, []int16{-99, 5, -99}, map[string]string{"BLANK": "-99", "BZERO": "10"}))
	f, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	px := f.HDUs[0].Data.Pixels
	if !math.IsNaN(float64(px[0])) || px[1] != 15 || !math.IsNaN(float64(px[2])) {
		t.Fatalf("pixels = %v, want [NaN 15 NaN]", px)
	}
}

func TestLoadFileFloatIgnoresScaleCards(t *testing.T) {
	// Float products decode unchanged even if BZERO/BSCALE/BLANK are present.
	path := writeTestFITS(t, fitsHDU("SCI", "", 2, 1, []float32{1.5, -2}, map[string]string{"BZERO": "7", "BSCALE": "3", "BLANK": "1"}))
	f, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, "LoadFile", f.HDUs[0].Data.Pixels, []float32{1.5, -2})
}

// multiHDUFixture returns a primary NAXIS3 cube, a BINTABLE with a heap
// (PCOUNT>0), then an IMAGE extension that only parses if both preceding data
// units are sized correctly.
func multiHDUFixture() []byte {
	cube := headerBlock(
		cardLine("SIMPLE", "T"),
		cardLine("BITPIX", "16"),
		cardLine("NAXIS", "3"),
		cardLine("NAXIS1", "2"),
		cardLine("NAXIS2", "2"),
		cardLine("NAXIS3", "3"),
	)
	cube = append(cube, mustEncodeBigEndianValue([]int16{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})...)
	cube = append(cube, make([]byte, padding(24))...)

	// 40 rows x 50 bytes + 1000-byte heap = 3000 bytes, i.e. two blocks. A
	// reader that ignores PCOUNT lands inside the heap.
	table := headerBlock(
		cardLine("XTENSION", "'BINTABLE'"),
		cardLine("BITPIX", "8"),
		cardLine("NAXIS", "2"),
		cardLine("NAXIS1", "50"),
		cardLine("NAXIS2", "40"),
		cardLine("PCOUNT", "1000"),
		cardLine("GCOUNT", "1"),
		cardLine("EXTNAME", "'HDRTAB'"),
	)
	tableData := bytes.Repeat([]byte("E"), 3000)
	table = append(table, tableData...)
	table = append(table, make([]byte, padding(len(tableData)))...)

	sci := headerBlock(
		cardLine("XTENSION", "'IMAGE   '"),
		cardLine("BITPIX", "-32"),
		cardLine("NAXIS", "2"),
		cardLine("NAXIS1", "2"),
		cardLine("NAXIS2", "1"),
		cardLine("PCOUNT", "0"),
		cardLine("GCOUNT", "1"),
		cardLine("EXTNAME", "'SCI'"),
	)
	sci = append(sci, mustEncodeBigEndianValue([]float32{42, 43})...)
	sci = append(sci, make([]byte, padding(8))...)
	return buildFITSFile(cube, table, sci)
}

func TestMultiHDUSizingHonorsNAXIS3AndPCOUNT(t *testing.T) {
	path := writeTestFITS(t, multiHDUFixture())

	f, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.HDUs) != 3 {
		t.Fatalf("LoadFile HDUs = %d, want 3", len(f.HDUs))
	}
	assertPixels(t, "cube first plane", f.HDUs[0].Data.Pixels, []float32{1, 2, 3, 4})
	// Tables decode their main rows as raw bytes; the heap is skipped.
	if tab := f.HDUs[1]; tab.ExtName != "HDRTAB" || len(tab.Data.Pixels) != 50*40 {
		t.Fatalf("table HDU = %q with %d bytes, want HDRTAB with 2000", tab.ExtName, len(tab.Data.Pixels))
	}
	assertPixels(t, "LoadFile SCI", f.SelectSCI()[0].Data.Pixels, []float32{42, 43})

	meta, err := LoadFileMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.HDUs) != 3 || meta.SelectSCI()[0].Data.Pixels[1] != 43 {
		t.Fatalf("LoadFileMetadata = %d HDUs", len(meta.HDUs))
	}

	_, sel, err := LoadSelectedHDU(path, "SCI", "")
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, "LoadSelectedHDU", sel.Data.Pixels, []float32{42, 43})

	_, byIndex, err := LoadHDUByIndex(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, "LoadHDUByIndex", byIndex.Data.Pixels, []float32{42, 43})

	if _, _, err := InspectSelectedHDU(path, "SCI", ""); err != nil {
		t.Fatal(err)
	}
}

func TestHDUDataBytes(t *testing.T) {
	tests := []struct {
		name  string
		cards map[string]string
		want  int
	}{
		{"primary empty", map[string]string{"BITPIX": "8", "NAXIS": "0"}, 0},
		{"spectrum", map[string]string{"BITPIX": "-32", "NAXIS": "1", "NAXIS1": "7"}, 28},
		{"cube", map[string]string{"BITPIX": "16", "NAXIS": "3", "NAXIS1": "4", "NAXIS2": "5", "NAXIS3": "6"}, 240},
		{"bintable heap", map[string]string{"XTENSION": "'BINTABLE'", "BITPIX": "8", "NAXIS": "2", "NAXIS1": "10", "NAXIS2": "3", "PCOUNT": "17", "GCOUNT": "1"}, 47},
		{"random groups", map[string]string{"BITPIX": "-32", "NAXIS": "3", "NAXIS1": "0", "NAXIS2": "3", "NAXIS3": "2", "GROUPS": "T", "PCOUNT": "2", "GCOUNT": "5"}, 160},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := hduDataBytes(Header{Cards: tt.cards})
			if err != nil || got != tt.want {
				t.Fatalf("hduDataBytes = %d, %v; want %d", got, err, tt.want)
			}
		})
	}
	for _, cards := range []map[string]string{
		{"BITPIX": "12", "NAXIS": "2", "NAXIS1": "1", "NAXIS2": "1"},
		{"BITPIX": "8", "NAXIS": "2", "NAXIS1": "-1", "NAXIS2": "1"},
		{"BITPIX": "8", "NAXIS": "2", "NAXIS1": "1", "NAXIS2": "1", "PCOUNT": "-5"},
		{"BITPIX": "-64", "NAXIS": "3", "NAXIS1": "9223372036854775807", "NAXIS2": "2", "NAXIS3": "2"},
	} {
		if _, err := hduDataBytes(Header{Cards: cards}); err == nil {
			t.Fatalf("hduDataBytes(%v) accepted invalid header", cards)
		}
	}
}

func TestLoadRejectsDataLargerThanFile(t *testing.T) {
	// A tiny file claiming a ~320 GB image must fail before allocating.
	data := headerBlock(
		cardLine("SIMPLE", "T"),
		cardLine("BITPIX", "-64"),
		cardLine("NAXIS", "2"),
		cardLine("NAXIS1", "200000"),
		cardLine("NAXIS2", "200000"),
	)
	path := writeTestFITS(t, data)
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "exceeds remaining file size") {
		t.Fatalf("LoadFile err = %v, want file-size error", err)
	}
	if _, _, err := LoadSelectedHDU(path, "", ""); err == nil {
		t.Fatal("LoadSelectedHDU accepted oversized HDU")
	}
	if _, _, err := LoadHDUByIndex(path, 0); err == nil {
		t.Fatal("LoadHDUByIndex accepted oversized HDU")
	}
	if _, err := LoadFileMetadata(path); err == nil {
		t.Fatal("LoadFileMetadata accepted oversized HDU")
	}
}

func TestReadHeaderTruncatedAtCardBoundaryIsError(t *testing.T) {
	full := headerBlock(cardLine("SIMPLE", "T"), cardLine("BITPIX", "8"), cardLine("NAXIS", "0"))
	if _, _, err := readHeader(bufio.NewReader(bytes.NewReader(full[:160]))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("readHeader truncated err = %v, want ErrUnexpectedEOF", err)
	}
	if _, _, err := readHeader(bufio.NewReader(bytes.NewReader(nil))); !errors.Is(err, io.EOF) {
		t.Fatalf("readHeader empty err = %v, want EOF", err)
	}

	// A second HDU cut off at a card boundary must fail LoadFile, not be
	// silently dropped.
	second := headerBlock(cardLine("XTENSION", "'IMAGE'"), cardLine("BITPIX", "-32"), cardLine("NAXIS", "2"), cardLine("NAXIS1", "1"), cardLine("NAXIS2", "1"))
	file := append(fitsHDU("", "", 1, 1, []float32{1}, nil), second[:240]...)
	if _, err := LoadFile(writeTestFITS(t, file)); err == nil {
		t.Fatal("LoadFile accepted a truncated extension header")
	}

	// Zero fill after the last HDU is still a clean end.
	filled := append(fitsHDU("", "", 1, 1, []float32{1}, nil), make([]byte, 2880)...)
	if f, err := LoadFile(writeTestFITS(t, filled)); err != nil || len(f.HDUs) != 1 {
		t.Fatalf("LoadFile zero-filled trailer = %v", err)
	}
}

func TestFloat32ArtifactCloseConcurrentWithAccess(t *testing.T) {
	a, err := CreateFloat32Artifact(filepath.Join(t.TempDir(), "a.f32"), 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			row := make([]float32, 8)
			for i := 0; i < 200; i++ {
				_ = a.ReadRow(i%8, row)
				_ = a.WriteRow(i%8, row)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = a.Close()
	}()
	wg.Wait()
	if err := a.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if err := a.ReadRow(0, make([]float32, 8)); !errors.Is(err, errArtifactClosed) {
		t.Fatalf("ReadRow after Close = %v", err)
	}
}

func FuzzReadHeader(f *testing.F) {
	f.Add(headerBlock(cardLine("SIMPLE", "T"), cardLine("BITPIX", "8"), cardLine("NAXIS", "0")))
	f.Add(multiHDUFixture())
	f.Add([]byte("SIMPLE  =                    T"))
	f.Fuzz(func(t *testing.T, data []byte) {
		hdr, n, err := readHeader(bufio.NewReader(bytes.NewReader(data)))
		if err != nil {
			return
		}
		if n <= 0 || n%2880 != 0 || n > len(data) {
			t.Fatalf("headerBytes = %d for %d input bytes", n, len(data))
		}
		if _, err := hduDataBytes(hdr); err != nil {
			return
		}
		_, _ = checkedImageDataBytes(hdr)
	})
}

func FuzzHDUDataBytes(f *testing.F) {
	f.Add(int64(16), int64(3), int64(4), int64(5), int64(6), int64(0), int64(1), false)
	f.Add(int64(8), int64(2), int64(50), int64(40), int64(0), int64(1000), int64(1), true)
	f.Add(int64(-64), int64(2), int64(200000), int64(200000), int64(0), int64(0), int64(1), false)
	f.Fuzz(func(t *testing.T, bitpix, naxis, n1, n2, n3, pcount, gcount int64, table bool) {
		cards := map[string]string{
			"BITPIX": strconv.FormatInt(bitpix, 10), "NAXIS": strconv.FormatInt(naxis, 10),
			"NAXIS1": strconv.FormatInt(n1, 10), "NAXIS2": strconv.FormatInt(n2, 10), "NAXIS3": strconv.FormatInt(n3, 10),
			"PCOUNT": strconv.FormatInt(pcount, 10), "GCOUNT": strconv.FormatInt(gcount, 10),
		}
		if table {
			cards["XTENSION"] = "'BINTABLE'"
		}
		hdr := Header{Cards: cards}
		total, err := hduDataBytes(hdr)
		if err != nil {
			return
		}
		if total < 0 || total+padding(total) < total {
			t.Fatalf("hduDataBytes = %d overflows", total)
		}
		plane, err := checkedImageDataBytes(hdr)
		if err != nil {
			return
		}
		if plane < 0 || plane > total {
			t.Fatalf("image plane %d exceeds data unit %d", plane, total)
		}
	})
}

func assertPixels(t *testing.T, label string, got, want []float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d pixels %v, want %v", label, len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: pixel %d = %v, want %v (all %v)", label, i, got[i], want[i], got)
		}
	}
}
