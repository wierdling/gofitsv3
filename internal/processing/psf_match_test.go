package processing

import (
	"context"
	"testing"
)

func TestSuggestPSFTargetUsesLargestAxis(t *testing.T) {
	g := SuggestPSFTarget([]PSFMeasurement{{FWHMX: 2, FWHMY: 4}, {FWHMX: 3, FWHMY: 3}})
	if g.FWHMX != 3 || g.FWHMY != 4 {
		t.Fatalf("target=%+v", g)
	}
}

func TestConvolveToPSFExpandsImpulseAndProtectsSaturation(t *testing.T) {
	p := make([]float32, 25)
	p[12] = 1
	out, err := ConvolveToPSF(context.Background(), p, 5, 5, PSFMeasurement{FWHMX: 1, FWHMY: 1}, PSFTarget{FWHMX: 3, FWHMY: 3}, false, 0)
	if err != nil || out[12] >= 1 || out[11] <= 0 {
		t.Fatalf("unexpected convolution err=%v center=%v neighbor=%v", err, out[12], out[11])
	}
	out, err = ConvolveToPSF(context.Background(), p, 5, 5, PSFMeasurement{FWHMX: 1, FWHMY: 1}, PSFTarget{FWHMX: 3, FWHMY: 3}, true, .9)
	if err != nil || out[12] != 1 {
		t.Fatalf("protected center=%v err=%v", out[12], err)
	}
}
