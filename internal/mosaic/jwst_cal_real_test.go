package mosaic

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadRealMIRICalFile exercises the full JWST MIRI _cal load path against a
// real Stage-2 product when one is present in TestImages. It is skipped when the
// file is absent so CI without the (large) image still passes.
func TestLoadRealMIRICalFile(t *testing.T) {
	path := filepath.Join("..", "..", "TestImages", "jw09224001001_02101_00001_mirimage_cal.fits")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			t.Skipf("no MIRI _cal test image available: %v", err)
		}
		t.Fatalf("stat MIRI _cal test image: %v", err)
	}

	inputs, err := LoadInputsFromPath(path)
	if err != nil {
		t.Fatalf("LoadInputsFromPath: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %d, want 1 (single SCI extension)", len(inputs))
	}
	in := inputs[0]

	if ProductType(path) != "cal" {
		t.Fatalf("ProductType = %q, want cal", ProductType(path))
	}
	if in.ExposureTime <= 0 {
		t.Fatalf("ExposureTime = %v, want > 0 (EFFEXPTM)", in.ExposureTime)
	}

	// MIRI cal data is MJy/sr surface brightness: Auto normalization must NOT
	// divide it by exposure time. (The header parser truncates "MJy/sr" to
	// "MJy", so this depends on the calibrated-flux check, not the rate check.)
	if normalize, _ := NormalizationFor(NormAuto, in.BUnit, in.ExposureTime); normalize {
		t.Fatalf("NormAuto normalize=true for BUnit=%q, want false", in.BUnit)
	}

	// Sanity: real flux data with finite pixels present.
	finite := 0
	for _, v := range in.HDU.Data.Pixels {
		if !math.IsNaN(float64(v)) {
			finite++
		}
	}
	if finite == 0 {
		t.Fatal("no finite pixels after load")
	}
}
