package instrument

import (
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestFromHeaderRecognizesMIRIImager(t *testing.T) {
	info, ok := FromHeader(fitsio.Header{Cards: map[string]string{
		"INSTRUME": "'MIRI'",
		"DETECTOR": "'MIRIMAGE'",
	}})
	if !ok {
		t.Fatal("FromHeader did not recognize MIRI/MIRIMAGE")
	}
	if info.Chips != 1 {
		t.Fatalf("Chips = %d, want 1", info.Chips)
	}
	// JWST must mask only DO_NOT_USE (bit 0 = 1), not every non-zero DQ bit.
	if info.BadDQBits != 1 {
		t.Fatalf("BadDQBits = %d, want 1 (DO_NOT_USE only)", info.BadDQBits)
	}
}

func TestFromHeaderRecognizesNIRCam(t *testing.T) {
	cases := []struct {
		detector  string
		wantScale float64
	}{
		{"NRCA1", 0.031},    // short-wave
		{"NRCB4", 0.031},    // short-wave
		{"NRCALONG", 0.063}, // long-wave
		{"NRCBLONG", 0.063}, // long-wave
	}
	for _, c := range cases {
		info, ok := FromHeader(fitsio.Header{Cards: map[string]string{
			"INSTRUME": "'NIRCAM'",
			"DETECTOR": "'" + c.detector + "'",
		}})
		if !ok {
			t.Fatalf("FromHeader did not recognize NIRCAM/%s", c.detector)
		}
		if info.PixelScale != c.wantScale {
			t.Fatalf("%s PixelScale = %v, want %v", c.detector, info.PixelScale, c.wantScale)
		}
		if info.BadDQBits != 1 {
			t.Fatalf("%s BadDQBits = %d, want 1 (JWST DO_NOT_USE)", c.detector, info.BadDQBits)
		}
	}
}

func TestFromHeaderRecognizesWFPC2FLT(t *testing.T) {
	info, ok := FromHeader(fitsio.Header{Cards: map[string]string{
		"INSTRUME": "'WFPC2'",
		"DETECTOR": "'PC'",
	}})
	if !ok {
		t.Fatal("FromHeader did not recognize WFPC2/PC")
	}
	if info.Chips != 4 {
		t.Fatalf("Chips = %d, want 4", info.Chips)
	}
	if info.ChipInnerTrim != 0 {
		t.Fatalf("ChipInnerTrim = %d, want 0", info.ChipInnerTrim)
	}
	if info.BadDQBits != 0 {
		t.Fatalf("BadDQBits = %d, want 0 to treat any non-zero WFPC2 DQ as bad", info.BadDQBits)
	}
	if info.HasSIP {
		t.Fatal("HasSIP = true, want false for WFPC2")
	}
}
