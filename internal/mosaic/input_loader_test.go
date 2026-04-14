package mosaic

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestCombineSCIHDUsPlacesExtensionsOnSharedCanvas(t *testing.T) {
	primary := fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'"}}
	ref := fitsio.HDU{
		Header: fitsio.Header{Cards: map[string]string{
			"CRPIX1": "10",
			"CRPIX2": "10",
			"CRVAL1": "100",
			"CRVAL2": "22",
			"CD1_1":  "1",
			"CD1_2":  "0",
			"CD2_1":  "0",
			"CD2_2":  "1",
			"EXTVER": "1",
		}},
		Data:    fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{1, 1, 1, 1}},
		ExtName: "SCI",
	}
	shifted := fitsio.HDU{
		Header: fitsio.Header{Cards: map[string]string{
			"CRPIX1": "8",
			"CRPIX2": "10",
			"CRVAL1": "100",
			"CRVAL2": "22",
			"CD1_1":  "1",
			"CD1_2":  "0",
			"CD2_1":  "0",
			"CD2_2":  "1",
			"EXTVER": "2",
		}},
		Data:    fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{2, 2, 2, 2}},
		ExtName: "SCI",
	}

	combined, _, err := combineSCIHDUs("test_flc.fits", primary, []fitsio.HDU{ref, shifted}, &fitsio.File{HDUs: []fitsio.HDU{ref, shifted}})
	if err != nil {
		t.Fatalf("combineSCIHDUs returned error: %v", err)
	}
	if combined.Data.Width != 4 || combined.Data.Height != 2 {
		t.Fatalf("combined size = %dx%d, want 4x2", combined.Data.Width, combined.Data.Height)
	}
	for i, want := range []float32{1, 1, 2, 2, 1, 1, 2, 2} {
		if math.Abs(float64(combined.Data.Pixels[i]-want)) > 1e-6 {
			t.Fatalf("pixel[%d] = %v, want %v", i, combined.Data.Pixels[i], want)
		}
	}
}

func TestCombineSCIHDUsShiftsCRPIXForExpandedCanvas(t *testing.T) {
	primary := fitsio.Header{Cards: map[string]string{"FILTER": "'F502N'"}}
	ref := fitsio.HDU{
		Header: fitsio.Header{Cards: map[string]string{
			"CRPIX1": "10",
			"CRPIX2": "10",
			"CRVAL1": "100",
			"CRVAL2": "22",
			"CD1_1":  "1",
			"CD1_2":  "0",
			"CD2_1":  "0",
			"CD2_2":  "1",
		}},
		Data:    fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{1, 1, 1, 1}},
		ExtName: "SCI",
	}
	left := fitsio.HDU{
		Header: fitsio.Header{Cards: map[string]string{
			"CRPIX1": "12",
			"CRPIX2": "10",
			"CRVAL1": "100",
			"CRVAL2": "22",
			"CD1_1":  "1",
			"CD1_2":  "0",
			"CD2_1":  "0",
			"CD2_2":  "1",
		}},
		Data:    fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{2, 2, 2, 2}},
		ExtName: "SCI",
	}

	combined, _, err := combineSCIHDUs("test_flc.fits", primary, []fitsio.HDU{ref, left}, &fitsio.File{HDUs: []fitsio.HDU{ref, left}})
	if err != nil {
		t.Fatalf("combineSCIHDUs returned error: %v", err)
	}
	if got := fitsio.HeaderString(combined.Header, "CRPIX1"); got != "12" {
		t.Fatalf("CRPIX1 = %q, want 12", got)
	}
	if got := fitsio.HeaderString(combined.Header, "CRPIX2"); got != "10" {
		t.Fatalf("CRPIX2 = %q, want 10", got)
	}
}
