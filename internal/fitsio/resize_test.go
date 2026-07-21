package fitsio

import (
	"context"
	"math"
	"path/filepath"
	"testing"
)

func TestResizeFITSFilesPreservesPlanesAndUpdatesWCS(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "drizzle.fits")
	header := Header{Cards: map[string]string{
		"CRPIX1": "10", "CRPIX2": "20", "CD1_1": "0.01", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.01",
	}}
	data := ImageData{Width: 4, Height: 4, Pixels: []float32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}}
	err := WriteFloat32ImageWithExtensions(input, Header{Cards: map[string]string{"TELESCOP": "'TEST'"}}, ImageData{},
		ImageExtension{ExtName: "SCI", Header: header, Data: data},
		ImageExtension{ExtName: "WHT", Header: Header{Cards: map[string]string{}}, Data: ImageData{Width: 4, Height: 4, Pixels: ones(16)}},
		ImageExtension{ExtName: "ERR", Header: Header{Cards: map[string]string{}}, Data: ImageData{Width: 4, Height: 4, Pixels: ones(16)}},
		ImageExtension{ExtName: "CTX", Header: Header{Cards: map[string]string{}}, Data: ImageData{Width: 4, Height: 4, Int32Pixels: []int32{1, 2, 4, 8, 16, 32, 64, 128, 1, 2, 4, 8, 16, 32, 64, 128}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := ResizeFITSFiles(context.Background(), []string{input}, 2, nil)
	if err != nil {
		t.Fatalf("ResizeFITSFiles: %v", err)
	}
	if got, want := outputs[0], filepath.Join(dir, "drizzle_x2.fits"); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	got, err := LoadFile(outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(got.HDUs) != 5 {
		t.Fatalf("HDU count = %d, want 5", len(got.HDUs))
	}
	if got.HDUs[0].Data.Width != 0 || HeaderString(got.HDUs[0].Header, "TELESCOP") != "TEST" {
		t.Fatalf("metadata primary was not preserved: %+v", got.HDUs[0])
	}
	if want := []float32{3.5, 5.5, 11.5, 13.5}; !samePixels(got.HDUs[1].Data.Pixels, want) {
		t.Fatalf("SCI pixels = %v, want %v", got.HDUs[1].Data.Pixels, want)
	}
	if want := []float32{4, 4, 4, 4}; !samePixels(got.HDUs[2].Data.Pixels, want) {
		t.Fatalf("WHT pixels = %v, want %v", got.HDUs[2].Data.Pixels, want)
	}
	if want := []float32{0.5, 0.5, 0.5, 0.5}; !samePixels(got.HDUs[3].Data.Pixels, want) {
		t.Fatalf("ERR pixels = %v, want %v", got.HDUs[3].Data.Pixels, want)
	}
	if want := []float32{51, 204, 51, 204}; !samePixels(got.HDUs[4].Data.Pixels, want) {
		t.Fatalf("CTX pixels = %v, want %v", got.HDUs[4].Data.Pixels, want)
	}
	if got, want := got.HDUs[4].Data.Int32Pixels, []int32{51, 204, 51, 204}; !sameInt32(got, want) {
		t.Fatalf("CTX exact pixels = %v, want %v", got, want)
	}
	if value, _ := HeaderFloat(got.HDUs[1].Header, "CRPIX1"); value != 5.25 {
		t.Fatalf("CRPIX1 = %v, want 5.25", value)
	}
	if value, _ := HeaderFloat(got.HDUs[1].Header, "CD1_1"); value != 0.02 {
		t.Fatalf("CD1_1 = %v, want 0.02", value)
	}
}

func TestResizeFITSFilesPreservesHighMaskBitsExactly(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "mask.fits")
	mask := []int32{1 << 30, 1, 2, 4}
	if err := WriteFloat32ImageWithExtensions(input, Header{}, ImageData{}, ImageExtension{ExtName: "DQ", Header: Header{Cards: map[string]string{}}, Data: ImageData{Width: 2, Height: 2, Int32Pixels: mask}}); err != nil {
		t.Fatal(err)
	}
	outputs, err := ResizeFITSFiles(context.Background(), []string{input}, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.HDUs[1].Header.Cards["BITPIX"] != "32" {
		t.Fatalf("BITPIX = %q, want 32", got.HDUs[1].Header.Cards["BITPIX"])
	}
	if want := int32((1 << 30) | 1 | 2 | 4); len(got.HDUs[1].Data.Int32Pixels) != 1 || got.HDUs[1].Data.Int32Pixels[0] != want {
		t.Fatalf("DQ = %v, want %d", got.HDUs[1].Data.Int32Pixels, want)
	}
}

func TestResizeFITSFilesDiscardsIncompleteEdgeBlocks(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "odd.fits")
	sci := ImageData{Width: 5, Height: 3, Pixels: []float32{1, 2, 3, 4, 99, 5, 6, 7, 8, 99, 99, 99, 99, 99, 99}}
	wht := ImageData{Width: 5, Height: 3, Pixels: ones(15)}
	dq := ImageData{Width: 5, Height: 3, Int32Pixels: []int32{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 1024, 1024, 1024, 1024}}
	if err := WriteFloat32ImageWithExtensions(input, Header{}, sci,
		ImageExtension{ExtName: "WHT", Header: Header{Cards: map[string]string{}}, Data: wht},
		ImageExtension{ExtName: "DQ", Header: Header{Cards: map[string]string{}}, Data: dq},
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ValidateResizeInputs([]string{input}, 2); err != nil {
		t.Fatalf("ValidateResizeInputs rejected partial edges: %v", err)
	}
	if _, _, err := ValidateResizeInputs([]string{input}, 4); err == nil {
		t.Fatal("ValidateResizeInputs allowed zero-height output")
	}
	outputs, err := ResizeFITSFiles(context.Background(), []string{input}, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.HDUs[0].Data.Width != 2 || got.HDUs[0].Data.Height != 1 {
		t.Fatalf("SCI size = %dx%d, want 2x1", got.HDUs[0].Data.Width, got.HDUs[0].Data.Height)
	}
	if want := []float32{3.5, 5.5}; !samePixels(got.HDUs[0].Data.Pixels, want) {
		t.Fatalf("SCI = %v, want %v", got.HDUs[0].Data.Pixels, want)
	}
	if want := []float32{4, 4}; !samePixels(got.HDUs[1].Data.Pixels, want) {
		t.Fatalf("WHT = %v, want %v", got.HDUs[1].Data.Pixels, want)
	}
	if want := []int32{1 | 2 | 32 | 64, 4 | 8 | 128 | 256}; !sameInt32(got.HDUs[2].Data.Int32Pixels, want) {
		t.Fatalf("DQ = %v, want %v", got.HDUs[2].Data.Int32Pixels, want)
	}
}

func TestResizeFITSFilesRejectsMismatchedDimensionsAndExistingOutput(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.fits")
	b := filepath.Join(dir, "b.fits")
	if err := WriteFloat32Image(a, Header{}, ImageData{Width: 4, Height: 4, Pixels: ones(16)}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFloat32Image(b, Header{}, ImageData{Width: 8, Height: 4, Pixels: ones(32)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ValidateResizeInputs([]string{a, b}, 2); err == nil {
		t.Fatal("ValidateResizeInputs accepted mismatched dimensions")
	}
	if err := WriteFloat32Image(ResizeOutputPath(a, 2), Header{}, ImageData{Width: 1, Height: 1, Pixels: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResizeFITSFiles(context.Background(), []string{a}, 2, nil); err == nil {
		t.Fatal("ResizeFITSFiles overwrote existing output")
	}
}

func ones(n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = 1
	}
	return out
}
func samePixels(got, want []float32) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if math.Abs(float64(got[i]-want[i])) > 1e-6 {
			return false
		}
	}
	return true
}

func sameInt32(got, want []int32) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
