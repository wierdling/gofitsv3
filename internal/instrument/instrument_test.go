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
	// JWST excludes DO_NOT_USE and NON_SCIENCE, not every non-zero DQ bit.
	if info.BadDQBits != 513 {
		t.Fatalf("BadDQBits = %d, want 513 (DO_NOT_USE|NON_SCIENCE)", info.BadDQBits)
	}
	if info.DQAction != DQActionExclude {
		t.Fatalf("DQAction = %v, want DQActionExclude", info.DQAction)
	}
}

func TestScalePresetsAreAscending(t *testing.T) {
	presets := ScalePresets()
	if len(presets) == 0 {
		t.Fatal("ScalePresets returned no presets")
	}
	for i := 1; i < len(presets); i++ {
		if presets[i].PixelScale < presets[i-1].PixelScale {
			t.Fatalf("presets not ascending at %d: %#v then %#v", i, presets[i-1], presets[i])
		}
	}
}

func TestFromHeaderUnknownFallsBackToDefault(t *testing.T) {
	info, ok := FromHeader(fitsio.Header{Cards: map[string]string{"INSTRUME": "'unknown'", "DETECTOR": "'mystery'"}})
	if ok || info != Default {
		t.Fatalf("unknown header = %#v, ok=%t; want Default,false", info, ok)
	}
}

func TestFromHeaderNormalizesWhitespaceAndCase(t *testing.T) {
	if _, ok := FromHeader(fitsio.Header{Cards: map[string]string{"INSTRUME": "' miri '", "DETECTOR": "'mirimage'"}}); !ok {
		t.Fatal("normalized MIRI header was not recognized")
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
		if info.BadDQBits != 513 {
			t.Fatalf("%s BadDQBits = %d, want 513 (JWST DO_NOT_USE|NON_SCIENCE)", c.detector, info.BadDQBits)
		}
		if info.DQAction != DQActionExclude {
			t.Fatalf("%s DQAction = %v, want DQActionExclude", c.detector, info.DQAction)
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
	if info.DQAction != DQActionRepair {
		t.Fatalf("DQAction = %v, want DQActionRepair", info.DQAction)
	}
	if info.HasSIP {
		t.Fatal("HasSIP = true, want false for WFPC2")
	}
}
