package instrument

import (
	"testing"

	"gofitsv3/internal/fitsio"
)

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
