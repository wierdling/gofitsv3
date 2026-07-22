package mosaic

import (
	"math"
	"strconv"
	"testing"

	"gofitsv3/internal/fitsio"
)

func inputWithHeader(cards map[string]string) Input {
	return Input{HDU: fitsio.HDU{Header: fitsio.Header{Cards: cards}}}
}

func TestNativePlateScaleArcsecFromCDMatrix(t *testing.T) {
	// A pure 0.05"/px scale expressed as a CD matrix (degrees/pixel).
	deg := 0.05 / 3600
	in := inputWithHeader(map[string]string{
		"CD1_1": "-" + fmtF(deg), "CD1_2": "0",
		"CD2_1": "0", "CD2_2": fmtF(deg),
	})
	got, ok := NativePlateScaleArcsec(in)
	if !ok {
		t.Fatal("expected ok from CD matrix")
	}
	if math.Abs(got-0.05) > 1e-9 {
		t.Fatalf("got %v, want 0.05", got)
	}
}

func TestNativePlateScaleArcsecInstrumentFallback(t *testing.T) {
	// No WCS cards: must fall back to the instrument table (ACS/WFC = 0.05).
	in := inputWithHeader(map[string]string{
		"INSTRUME": "ACS", "DETECTOR": "WFC",
	})
	got, ok := NativePlateScaleArcsec(in)
	if !ok {
		t.Fatal("expected instrument-table fallback")
	}
	if math.Abs(got-0.05) > 1e-9 {
		t.Fatalf("got %v, want 0.05", got)
	}
}

func TestNativePlateScaleArcsecNoData(t *testing.T) {
	if _, ok := NativePlateScaleArcsec(inputWithHeader(map[string]string{})); ok {
		t.Fatal("expected ok=false with neither WCS nor known instrument")
	}
}

func fmtF(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
