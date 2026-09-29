package fitsio

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStreamHDUByIndexRejectsUnsupportedAndShortData(t *testing.T) {
	unsupported := filepath.Join(t.TempDir(), "unsupported.fits")
	bad := fitsHDU("SCI", "1", 1, 1, []float32{1}, nil)
	// Replace BITPIX while retaining the valid header/data shape.
	bad[80+10] = '9'
	if err := os.WriteFile(unsupported, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := StreamHDUByIndex(context.Background(), unsupported, 0, func(int, []float32, []int32) error { return nil }); err == nil {
		t.Fatal("unsupported BITPIX accepted")
	}
	short := fitsHDU("SCI", "1", 2, 1, []float32{1, 2}, nil)
	short = short[:len(short)-2880+2]
	path := filepath.Join(t.TempDir(), "short.fits")
	if err := os.WriteFile(path, short, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := StreamHDUByIndex(context.Background(), path, 0, func(int, []float32, []int32) error { return nil }); err == nil {
		t.Fatal("short payload accepted")
	}
}

func TestStreamHDUByIndexHonorsMidStreamCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cancel.fits")
	data := fitsHDU("SCI", "1", 1, 3, []float32{1, 2, 3}, nil)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, _, err := StreamHDUByIndex(ctx, path, 0, func(y int, _ []float32, _ []int32) error {
		if y == 0 {
			cancel()
		}
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("stream cancellation error = %v", err)
	}
}

func TestCreateFloat32ArtifactOversizedPreservesDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.bin")
	want := []byte("sentinel")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateFloat32Artifact(path, int(^uint32(0)), 1); err == nil {
		t.Fatal("oversized artifact row was accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("destination changed after rejected create: %q", got)
	}
}

func TestOpenFloat32ArtifactRejectsMaxUint32Header(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malicious.bin")
	h := make([]byte, artifactHeaderSize)
	copy(h[:8], artifactMagic[:])
	binary.LittleEndian.PutUint32(h[8:12], ^uint32(0))
	binary.LittleEndian.PutUint32(h[12:16], ^uint32(0))
	if err := os.WriteFile(path, h, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFloat32Artifact(path); err == nil {
		t.Fatal("malicious max-uint32 header was accepted")
	}
}

func TestFloat32ArtifactTransactionCommitPreclosedPreservesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "destination.bin")
	a, err := CreateFloat32Artifact(path, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(0, []float32{7}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	tx, err := BeginFloat32ArtifactTransaction(path, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Artifact().WriteRow(0, []float32{9}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Artifact().Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("commit succeeded with preclosed artifact")
	}
	b, err := OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	row := make([]float32, 1)
	if err := b.ReadRow(0, row); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	if row[0] != 7 {
		t.Fatalf("destination changed after failed commit: %v", row[0])
	}
}

func TestLoadSelectedHDUMatchesLoadFileAndSkipsOtherPixels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selected.fits")
	data := append(fitsHDU("", "", 2, 1, []float32{4, 5}, nil), fitsHDU("SCI", "2", 2, 1, []float32{8, 9}, nil)...)
	data = append(data, fitsHDU("ERR", "1", 2, 1, []float32{99, 100}, nil)...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	primary, got, err := LoadSelectedHDU(path, "SCI", "2")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Data.Pixels, want.GetHDUByExtVer("SCI", "2").Data.Pixels) {
		t.Fatalf("selected pixels = %v, want %v", got.Data.Pixels, want.GetHDUByExtVer("SCI", "2").Data.Pixels)
	}
	if !reflect.DeepEqual(primary, want.HDUs[0].Header) {
		t.Fatal("primary header differs from LoadFile")
	}
	_, got, err = LoadSelectedHDU(path, "", "")
	if err != nil || !reflect.DeepEqual(got.Data.Pixels, want.HDUs[0].Data.Pixels) {
		t.Fatalf("primary selection = %#v, err=%v", got.Data.Pixels, err)
	}
}

func TestLoadFileSelectiveIndexedDecodesOnlyRequestedHDU(t *testing.T) {
	path := filepath.Join(t.TempDir(), "indexed.fits")
	data := buildFITSFile(
		fitsHDU("", "", 1, 1, []float32{0}, map[string]string{"INSTRUME": "GMOS-N"}),
		fitsHDU("", "-1", 2, 1, []float32{1, 2}, nil),
		fitsHDU("", "-1", 2, 1, []float32{3, 4}, nil),
		fitsHDU("", "-1", 2, 1, []float32{5, 6}, nil),
	)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFileSelectiveIndexed(path, func(index int, _ Header) bool { return index == 2 })
	if err != nil {
		t.Fatal(err)
	}
	if len(got.HDUs) != 4 || got.HDUs[1].Data.Pixels != nil || got.HDUs[3].Data.Pixels != nil {
		t.Fatal("sibling HDU pixels were decoded")
	}
	if !reflect.DeepEqual(got.HDUs[2].Data.Pixels, []float32{3, 4}) {
		t.Fatalf("selected pixels = %v", got.HDUs[2].Data.Pixels)
	}
}

func TestLoadSelectedHDUReportsShortSelectedData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.fits")
	data := fitsHDU("SCI", "1", 2, 1, []float32{1, 2}, nil)
	data = data[:len(data)-2880+4]
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSelectedHDU(path, "SCI", "1"); err == nil {
		t.Fatal("short selected data was accepted")
	}
}

func TestFloat32ArtifactRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pixels.bin")
	a, err := CreateFloat32Artifact(path, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(1, []float32{3, 4, 5}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = OpenFloat32Artifact(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	row := make([]float32, 3)
	if err := a.ReadRow(1, row); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(row, []float32{3, 4, 5}) {
		t.Fatalf("row = %v", row)
	}
}

func TestFloat32ArtifactContiguousRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pixels.bin")
	a, err := CreateFloat32Artifact(path, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.WriteRows(1, 2, []float32{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	got := make([]float32, 4)
	if err := a.ReadRows(1, 2, got); err != nil {
		t.Fatal(err)
	}
	if want := []float32{1, 2, 3, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if err := a.ReadRows(2, 2, make([]float32, 4)); err == nil {
		t.Fatal("out-of-bounds rows accepted")
	}
}

func TestFloat32ArtifactBoundedRangeTileAndTransaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.bin")
	a, err := CreateFloat32Artifact(path, 4, 3)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 3; y++ {
		if err := a.WriteRow(y, []float32{float32(y), 1, 2, 3}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	r := make([]float32, 2)
	if err := a.ReadRange(1, 1, 3, r); err != nil || !reflect.DeepEqual(r, []float32{1, 2}) {
		t.Fatalf("range=%v err=%v", r, err)
	}
	if err := a.ReadRange(1, -1, 2, r); err == nil {
		t.Fatal("out-of-bounds range accepted")
	}
	tile := make([]float32, 4)
	if err := a.ReadTile(1, 0, 3, 2, tile); err != nil || !reflect.DeepEqual(tile, []float32{1, 2, 1, 2}) {
		t.Fatalf("tile=%v err=%v", tile, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	tx, err := BeginFloat32ArtifactTransaction(path, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Artifact().WriteRow(0, []float32{9}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	row := make([]float32, 1)
	_ = b.ReadRow(0, row)
	_ = b.Close()
	if row[0] != 9 {
		t.Fatalf("committed value=%v", row[0])
	}
}

func TestFloat32ArtifactRotatedRowsReuseBoundedBuffer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rect.bin")
	a, err := CreateFloat32Artifact(path, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(0, []float32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(1, []float32{4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	row := make([]float32, 2)
	if err := a.ReadRowRotatedCW(0, row); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(row, []float32{4, 1}) {
		t.Fatalf("rotated row 0 = %v", row)
	}
	if err := a.ReadRowRotatedCW(2, row); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(row, []float32{6, 3}) {
		t.Fatalf("rotated row 2 = %v", row)
	}
	// The rotated accessor's source row is artifact-owned and reused. This
	// also guards against regressing to a per-call full-row allocation.
	if len(a.rowBuf) != a.Width {
		t.Fatalf("rotated source buffer length = %d, want %d", len(a.rowBuf), a.Width)
	}
}

func TestFloat32ArtifactTransactionAbortPreservesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.bin")
	a, _ := CreateFloat32Artifact(path, 1, 1)
	_ = a.WriteRow(0, []float32{3})
	_ = a.Close()
	tx, err := BeginFloat32ArtifactTransaction(path, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Artifact().WriteRow(0, []float32{8})
	_ = tx.Abort()
	b, err := OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	row := make([]float32, 1)
	_ = b.ReadRow(0, row)
	_ = b.Close()
	if row[0] != 3 {
		t.Fatalf("abort changed destination: %v", row[0])
	}
}

func TestMaterializedPlaneLeaseTracksLifetime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.bin")
	a, _ := CreateFloat32Artifact(path, 2, 1)
	_ = a.WriteRow(0, []float32{1, 2})
	_ = a.Close()
	i := &ArtifactInstrumentation{}
	SetArtifactInstrumentation(i)
	defer SetArtifactInstrumentation(nil)
	p, err := MaterializeFloat32ArtifactLease(path)
	if err != nil {
		t.Fatal(err)
	}
	if i.MaterializedPlanes.Load() != 1 || len(p.Pixels) != 2 {
		t.Fatalf("lease instrumentation/pixels invalid")
	}
	p.Release()
	if i.MaterializedPlanes.Load() != 0 || p.Pixels != nil {
		t.Fatal("lease did not release")
	}
}

func TestCopySelectedHDUToFloat32ArtifactNormalizesRows(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source.fits")
	if err := os.WriteFile(src, fitsHDU("SCI", "3", 2, 2, []float32{2, 4, 6, 8}, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "artifact.bin")
	if err := CopySelectedHDUToFloat32Artifact(src, "SCI", "3", dst); err != nil {
		t.Fatal(err)
	}
	a, err := OpenFloat32Artifact(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	row := make([]float32, 2)
	if err := a.ReadRow(1, row); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(row, []float32{2.0 / 3.0, 1}) {
		t.Fatalf("normalized row = %v", row)
	}
}

func TestCopySelectedHDUToRawFloat32ArtifactPreservesValues(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "in.fits"), filepath.Join(dir, "raw.bin")
	if err := os.WriteFile(src, fitsHDU("SCI", "1", 2, 1, []float32{2, 8}, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CopySelectedHDUToRawFloat32Artifact(src, "SCI", "1", dst); err != nil {
		t.Fatal(err)
	}
	a, err := OpenFloat32Artifact(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	row := make([]float32, 2)
	if err := a.ReadRow(0, row); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(row, []float32{2, 8}) {
		t.Fatalf("raw row = %v", row)
	}
}

func TestCopySelectedHDUToFloat32ArtifactLeavesDestinationOnSourceFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "short.fits")
	data := fitsHDU("SCI", "1", 2, 2, []float32{1, 2, 3, 4}, nil)
	data = data[:2880+2]
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "artifact.bin")
	want := []byte("previous artifact")
	if err := os.WriteFile(dst, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CopySelectedHDUToFloat32Artifact(src, "SCI", "1", dst); err == nil {
		t.Fatal("short source was accepted")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("destination changed after failed copy: %q", got)
	}
}
