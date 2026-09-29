package astroio

import (
	"context"
	"errors"
	"testing"

	"gofitsv3/internal/fitsio"
)

type collectingWriter struct {
	rows []Row
}

func (w *collectingWriter) WriteRow(_ context.Context, row Row) error {
	w.rows = append(w.rows, Row{Y: row.Y, Float32: append([]float32(nil), row.Float32...), ExactDQ: append([]uint32(nil), row.ExactDQ...)})
	return nil
}

func TestFITSDecoderMetadataReadAndStream(t *testing.T) {
	path := t.TempDir() + "\\sample.fits"
	err := fitsio.WriteFloat32ImageWithExtensions(path,
		fitsio.Header{Cards: map[string]string{"OBJECT": "'test'"}},
		fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{1, 2, 3, 4}},
		fitsio.ImageExtension{ExtName: "SCI", Header: fitsio.Header{Cards: map[string]string{"EXTVER": "2"}}, Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{5, 6, 7, 8}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	source, err := (FITSDecoder{}).Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := source.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Planes) != 2 || meta.Planes[0].ID != "fits:hdu:0" || meta.Planes[1].ID != "fits:hdu:1" {
		t.Fatalf("planes = %#v", meta.Planes)
	}
	if meta.Cards["OBJECT"] != "'test'" {
		t.Fatalf("primary cards = %#v", meta.Cards)
	}
	plane, err := source.ReadPlane(context.Background(), meta.Planes[1].ID, ReadOptions{})
	if err != nil || len(plane.Data) != 4 || plane.Data[2] != 7 {
		t.Fatalf("ReadPlane = %#v, error %v", plane, err)
	}
	writer := new(collectingWriter)
	if err := source.StreamPlane(context.Background(), meta.Planes[1].ID, ReadOptions{}, writer); err != nil {
		t.Fatal(err)
	}
	if len(writer.rows) != 2 || writer.rows[1].Float32[0] != 7 {
		t.Fatalf("streamed rows = %#v", writer.rows)
	}
}

func TestFITSMetadataKeepsHeaderOnlyPrimary(t *testing.T) {
	path := t.TempDir() + "\\header-only.fits"
	if err := fitsio.WriteFloat32ImageWithExtensions(path,
		fitsio.Header{Cards: map[string]string{"OBJECT": "'header'"}}, fitsio.ImageData{},
		fitsio.ImageExtension{ExtName: "SCI", Data: fitsio.ImageData{Width: 2, Height: 1, Pixels: []float32{9, 8}}}); err != nil {
		t.Fatal(err)
	}
	source, err := (FITSDecoder{}).Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := source.Metadata(context.Background())
	if err != nil || meta.Cards["OBJECT"] != "'header'" || len(meta.Planes) != 1 {
		t.Fatalf("metadata = %#v, error %v", meta, err)
	}
}

func TestFITSReadUsesExactHDUIndexAndChecksDQ(t *testing.T) {
	path := t.TempDir() + "\\duplicate.fits"
	if err := fitsio.WriteFloat32ImageWithExtensions(path,
		fitsio.Header{}, fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{1}},
		fitsio.ImageExtension{ExtName: "SCI", Data: fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{2}}},
		fitsio.ImageExtension{ExtName: "SCI", Data: fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{3}}}); err != nil {
		t.Fatal(err)
	}
	source, err := (FITSDecoder{}).Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	plane, err := source.ReadPlane(context.Background(), "fits:hdu:2", ReadOptions{})
	if err != nil || plane.Data[0] != 3 {
		t.Fatalf("plane = %#v, error %v", plane, err)
	}
	if _, err := source.ReadPlane(context.Background(), "fits:hdu:2", ReadOptions{ExactIntegerDQ: true}); err == nil {
		t.Fatal("exact DQ unexpectedly accepted for SCI")
	}
}

func TestFITSExactDQPreservesHighBitsAndStreamsWriterErrors(t *testing.T) {
	path := t.TempDir() + "\\dq.fits"
	if err := fitsio.WriteFloat32ImageWithExtensions(path, fitsio.Header{}, fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{1}}, fitsio.ImageExtension{ExtName: "DQ", Data: fitsio.ImageData{Width: 2, Height: 2, Int32Pixels: []int32{-1, 0x40000000, 1, 2}}}); err != nil {
		t.Fatal(err)
	}
	source, err := (FITSDecoder{}).Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	plane, err := source.ReadPlane(context.Background(), "fits:hdu:1", ReadOptions{ExactIntegerDQ: true})
	if err != nil || len(plane.ExactDQ) != 4 || plane.ExactDQ[0] != 0xffffffff || plane.ExactDQ[1] != 0x40000000 {
		t.Fatalf("DQ = %#v, error %v", plane, err)
	}
	failing := &errorWriter{err: errors.New("stop")}
	if err := source.StreamPlane(context.Background(), "fits:hdu:1", ReadOptions{}, failing); !errors.Is(err, failing.err) {
		t.Fatalf("stream error = %v", err)
	}
}

type errorWriter struct{ err error }

func (w *errorWriter) WriteRow(context.Context, Row) error { return w.err }

func TestFITSDecoderProbeAndCancellation(t *testing.T) {
	path := t.TempDir() + "\\sample.fits"
	if err := fitsio.WriteFloat32Image(path, fitsio.Header{}, fitsio.ImageData{Width: 1, Height: 1, Pixels: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	matched, err := (FITSDecoder{}).Probe(path)
	if err != nil || !matched {
		t.Fatalf("Probe = %v, %v", matched, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (FITSDecoder{}).Open(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open canceled error = %v", err)
	}
}

func TestRegistryRejectsUnknownFormat(t *testing.T) {
	path := t.TempDir() + "\\sample.txt"
	if matched, err := (FITSDecoder{}).Probe(path); err == nil || matched {
		t.Fatalf("Probe missing file = %v, %v", matched, err)
	}
	var r Registry
	if _, err := r.Open(context.Background(), path); err == nil {
		t.Fatal("empty registry unexpectedly opened file")
	}
}

func TestRegistryOpenHonorsCanceledContextBeforeProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var r Registry
	r.Register(FITSDecoder{})
	if _, err := r.Open(ctx, t.TempDir()+"\\missing.fits"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled registry open error = %v", err)
	}
}

func TestFITSRejectsHeaderOnlyPlaneID(t *testing.T) {
	path := t.TempDir() + "\\header-only.fits"
	if err := fitsio.WriteFloat32Image(path, fitsio.Header{}, fitsio.ImageData{}); err != nil {
		t.Fatal(err)
	}
	source, err := (FITSDecoder{}).Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadPlane(context.Background(), "fits:hdu:0", ReadOptions{}); err == nil {
		t.Fatal("header-only HDU unexpectedly read as an image")
	}
	if err := source.StreamPlane(context.Background(), "fits:hdu:0", ReadOptions{}, &collectingWriter{}); err == nil {
		t.Fatal("header-only HDU unexpectedly streamed as an image")
	}
}
