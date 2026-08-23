package mosaic

import "testing"

func TestNormalizationForCalibratedFluxNotNormalized(t *testing.T) {
	// JWST Stage-2 cal data is in MJy/sr (calibrated surface brightness) and
	// must not be divided by exposure time under the Auto policy.
	cases := []struct {
		bunit string
		want  bool
	}{
		{"MJy/sr", false},
		{"MJy", false},
		{"Jy", false},
		{"ELECTRONS", true},    // total counts -> normalize
		{"ELECTRONS/S", false}, // already a rate
		{"", false},            // unknown -> leave alone
	}
	for _, c := range cases {
		normalize, _ := NormalizationFor(NormAuto, c.bunit, 100)
		if normalize != c.want {
			t.Fatalf("NormalizationFor(Auto, %q) normalize=%t, want %t", c.bunit, normalize, c.want)
		}
	}
}

func TestBunitIsAlreadyRate(t *testing.T) {
	cases := map[string]bool{
		"MJy/sr":      true,  // JWST calibrated surface brightness
		"MJy":         true,  // calibrated flux
		"ELECTRONS/S": true,  // per-second rate
		"DN/s":        true,  // per-second rate
		"ELECTRONS":   false, // total counts -> needs /EXPTIME
		"COUNTS":      false, // total counts
		"":            false, // unknown -> leave existing behaviour
	}
	for bunit, want := range cases {
		if got := bunitIsAlreadyRate(bunit); got != want {
			t.Fatalf("bunitIsAlreadyRate(%q) = %t, want %t", bunit, got, want)
		}
	}
}

func TestNormalizationForOffNeverNormalizes(t *testing.T) {
	if normalize, _ := NormalizationFor(NormOff, "ELECTRONS", 100); normalize {
		t.Fatal("NormOff should never normalize")
	}
}

func TestNormalizationForAutoLeavesUnknownUnitsOff(t *testing.T) {
	for _, unit := range []string{"", "UNKNOWN", "MJY/SR", "PHOTONS", "COUNTS/S"} {
		if normalize, _ := NormalizationFor(NormAuto, unit, 100); normalize {
			t.Errorf("NormalizationFor(Auto, %q) normalized unknown/rate unit", unit)
		}
	}
}

func TestNormalizationForAutoRecognizesExplicitTotalCounts(t *testing.T) {
	for _, unit := range []string{"ELECTRON", "ELECTRONS", "E-", "COUNTS", "COUNT", "DN", "ADU", "COUNTS/PIXEL"} {
		if normalize, scale := NormalizationFor(NormAuto, unit, 100); !normalize || scale != 0.01 {
			t.Errorf("NormalizationFor(Auto, %q) = (%t, %v), want (true, 0.01)", unit, normalize, scale)
		}
	}
}
