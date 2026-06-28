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
		{"ELECTRONS", true},   // total counts -> normalize
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
