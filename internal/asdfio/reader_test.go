package asdfio

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writePayload(t *testing.T, path string, payload []byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	at := bytes.Index(b, []byte("\xd3BLK"))
	if at < 0 {
		t.Fatal("block missing")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(payload)))
	if _, err := f.WriteAt(size[:], int64(at)+14); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(size[:], int64(at)+22); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(size[:], int64(at)+30); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(payload, int64(at)+54); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestReadArraySupportsEndianAndExactDQ(t *testing.T) {
	dataPath := fixture(t, "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: big, shape: [1, 2]}", 0)
	payload := []byte{0x3f, 0x80, 0, 0, 0x7f, 0xc0, 0, 0}
	writePayload(t, dataPath, payload)
	d, err := Open(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := d.ReadArray("data")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != math.Float32frombits(0x7fc00000) && !math.IsNaN(float64(got[1])) {
		t.Fatalf("values=%v", got)
	}

	dqPath := fixture(t, "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [1, 2]}\ndq: !core/ndarray-1.0.0 {source: 0, datatype: uint32, byteorder: little, shape: [1, 2]}", 0)
	writePayload(t, dqPath, []byte{1, 0, 0, 0, 0, 0, 2, 128})
	dq, err := Open(dqPath)
	if err != nil {
		t.Fatal(err)
	}
	_, exact, err := dq.ReadArray("dq")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(uint32Bytes(exact), []byte{1, 0, 0, 0, 0, 0, 2, 128}) {
		t.Fatalf("dq=%v", exact)
	}
}

func TestStreamArrayDecodesRows(t *testing.T) {
	p := fixture(t, "data: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [2, 2]}", 0)
	payload := []byte{0, 0, 128, 63, 0, 0, 0, 64, 0, 0, 64, 64, 0, 0, 128, 64}
	writePayload(t, p, payload)
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]float32
	err = d.StreamArray(context.Background(), "data", func(_ int, values []float32, _ []uint32) error {
		rows = append(rows, append([]float32(nil), values...))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rows, [][]float32{{1, 2}, {3, 4}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows=%v want=%v", got, want)
	}
	if got, want := d.Arrays["data"].Shape, []int64{2, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shape=%v want=%v", got, want)
	}
	values, _, err := d.ReadArray("data")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := values, []float32{1, 2, 3, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("values=%v want=%v", got, want)
	}
}

func uint32Bytes(v []uint32) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], x)
	}
	return b
}

func TestReadArrayRejectsChecksumMismatch(t *testing.T) {
	p := fixture(t, "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [1, 2]}", 0)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	at := bytes.Index(b, []byte("\xd3BLK"))
	if at < 0 {
		t.Fatal("block missing")
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte{0, 0, 128, 63, 0, 0, 0, 64}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(payload)))
	_, _ = f.WriteAt(size[:], int64(at)+14)
	_, _ = f.WriteAt(size[:], int64(at)+22)
	_, _ = f.WriteAt(size[:], int64(at)+30)
	_, _ = f.WriteAt(payload, int64(at)+54)
	sum := md5.Sum([]byte("wrong"))
	_, _ = f.WriteAt(sum[:], int64(at)+38)
	f.Close()
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.ReadArray("data"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err=%v", err)
	}
}

func TestStreamArrayChecksumPolicies(t *testing.T) {
	payload := []byte{0, 0, 128, 63, 0, 0, 0, 64}
	for _, tc := range []struct {
		name     string
		checksum bool
		valid    bool
	}{{"absent", false, true}, {"valid", true, true}, {"mismatch", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			p := fixture(t, "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [1, 2]}", 0)
			writePayload(t, p, payload)
			d, err := Open(p)
			if err != nil {
				t.Fatal(err)
			}
			if tc.checksum {
				sum := md5.Sum(payload)
				if !tc.valid {
					sum[0] ^= 1
				}
				f, e := os.OpenFile(p, os.O_RDWR, 0600)
				if e != nil {
					t.Fatal(e)
				}
				_, e = f.WriteAt(sum[:], d.Blocks[0].Offset+38)
				f.Close()
				if e != nil {
					t.Fatal(e)
				}
				d, err = Open(p)
				if err != nil {
					t.Fatal(err)
				}
			}
			rows := 0
			err = d.StreamArray(context.Background(), "data", func(y int, values []float32, _ []uint32) error {
				rows++
				if y != 0 || len(values) != 2 {
					t.Fatalf("row=%d values=%v", y, values)
				}
				return nil
			})
			if tc.valid && err != nil || !tc.valid && (err == nil || !strings.Contains(err.Error(), "checksum")) {
				t.Fatalf("err=%v", err)
			}
			if tc.valid && rows != 1 {
				t.Fatalf("rows=%d", rows)
			}
		})
	}
}

func TestOpenReadsHeaderAcrossChunks(t *testing.T) {
	yaml := "asdf_standard: 1.6.0\n#" + strings.Repeat("x", 5000) + "\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [2, 3]}"
	if _, err := Open(fixture(t, yaml, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsOversizedContiguousBlock(t *testing.T) {
	p := fixture(t, "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [1, 2]}", 0)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	at := strings.Index(string(b), "\xd3BLK")
	f, err := os.OpenFile(p, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	var z [8]byte
	binary.BigEndian.PutUint64(z[:], 12)
	_, _ = f.WriteAt(z[:], int64(at)+14)
	_, _ = f.WriteAt(z[:], int64(at)+22)
	_, _ = f.WriteAt(z[:], int64(at)+30)
	f.Close()
	if _, err := Open(p); err == nil || !strings.Contains(err.Error(), "exactly") {
		t.Fatalf("err=%v", err)
	}
}

func fixture(t *testing.T, yaml string, compression byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "x.asdf")
	h := []byte("#ASDF 1.0.0\n#ASDF_STANDARD 1.6.0\n" + yaml + "\n...\n")
	// Literal ASDF 1.6 block framing: 4-byte magic, 2-byte remainder size,
	// then 48 bytes of fields. Keep this independent of parser constants.
	b := make([]byte, 54+24)
	b[0], b[1], b[2], b[3] = 0xd3, 0x42, 0x4c, 0x4b
	binary.BigEndian.PutUint16(b[4:], 48)
	b[10+3] = compression
	binary.BigEndian.PutUint64(b[14:], 24)
	binary.BigEndian.PutUint64(b[22:], 24)
	binary.BigEndian.PutUint64(b[30:], 24)
	if err := os.WriteFile(path, append(h, b...), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenValidatesJWSTImageModelWithoutReadingPayload(t *testing.T) {
	p := fixture(t, "asdf_standard: 1.6.0\ndata: &plane !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [2, 3]}", 0)
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Blocks) != 1 {
		t.Fatalf("blocks=%d", len(d.Blocks))
	}
	if d.Blocks[0].PayloadOffset != d.Blocks[0].Offset+54 {
		t.Fatalf("payload offset=%d block offset=%d", d.Blocks[0].PayloadOffset, d.Blocks[0].Offset)
	}
	if d.Arrays["data"].Shape[1] != 3 {
		t.Fatalf("shape=%v", d.Arrays["data"].Shape)
	}
}

func TestOpenReadsStandardFromASDFDirective(t *testing.T) {
	p := fixture(t, "data: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [2, 3]}", 0)
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Standard != "1.6.0" {
		t.Fatalf("standard=%q", d.Standard)
	}
}

func TestRejectsUnsupportedProfileFeatures(t *testing.T) {
	tests := []struct{ name, yaml, want string }{{"compression", "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [2,2], compression: zlib}", "compression"}, {"three dimensional", "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [1,2,3]}", "2-D"}, {"external", "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [2,2], inline: true}", "inline"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Open(fixture(t, tt.yaml, 0))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRejectsMissingAndDuplicateDescriptorKeys(t *testing.T) {
	for name, yaml := range map[string]string{
		"missing source":  "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {datatype: float32, byteorder: little, shape: [2,2]}",
		"duplicate shape": "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [2,2], shape: [2,2]}",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Open(fixture(t, yaml, 0))
			if err == nil || !strings.Contains(err.Error(), "descriptor") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRejectsUnknownVersionAndCompression(t *testing.T) {
	p := fixture(t, "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [2,2]}", 1)
	if _, err := Open(p); err == nil || !strings.Contains(err.Error(), "compression") {
		t.Fatalf("err=%v", err)
	}
	p = fixture(t, "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [2,2]}", 0)
	b, _ := os.ReadFile(p)
	b[6] = '2'
	os.WriteFile(p, b, 0600)
	if _, err := Open(p); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("err=%v", err)
	}
}

func TestRejectsMalformedASDFBlockHeader(t *testing.T) {
	base := fixture(t, "asdf_standard: 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [2,2]}", 0)
	cases := []struct {
		name string
		edit func([]byte)
		want string
	}{
		{"declared header too short", func(b []byte) { binary.BigEndian.PutUint16(b[4:], 5) }, "header size"},
		{"nonzero flags", func(b []byte) { binary.BigEndian.PutUint32(b[6:], 1) }, "flags"},
		{"allocated smaller than used", func(b []byte) { binary.BigEndian.PutUint64(b[14:], 1) }, "block size"},
		{"data size mismatch", func(b []byte) { binary.BigEndian.PutUint64(b[30:], 1) }, "block size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := os.ReadFile(base)
			if err != nil {
				t.Fatal(err)
			}
			at := strings.Index(string(b), "\xd3BLK")
			if at < 0 {
				t.Fatal("fixture block not found")
			}
			tc.edit(b[at:])
			p := filepath.Join(t.TempDir(), "bad.asdf")
			if err := os.WriteFile(p, b, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(p); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
