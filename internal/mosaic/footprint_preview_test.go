package mosaic

import (
	"math"
	"testing"
)

func TestPlanFootprintPreviewGroupsChipsAndWarnsInvalidWCS(t *testing.T) {
	valid := makeInput("good.fits", 10, 8, nil, headerWithCRPIX(5, 4))
	chip := makeInput("good.fits", 6, 4, nil, headerWithCRPIX(5, 4))
	chip.SCIExt = 2
	invalid := makeInput("bad.fits", 10, 8, nil, headerWithCRPIX(5, 4))
	invalid.HDU.Header.Cards["CD2_2"] = "0"

	groups, width, height, err := PlanFootprintPreview([]Input{valid, chip, invalid}, 1)
	if err != nil {
		t.Fatalf("PlanFootprintPreview() error = %v", err)
	}
	if width <= 0 || height <= 0 {
		t.Fatalf("canvas size = %dx%d, want positive", width, height)
	}
	if len(groups) != 2 || groups[0].SourceNumber != 1 || groups[1].SourceNumber != 2 {
		t.Fatalf("groups = %#v, want two numbered source groups", groups)
	}
	if len(groups[0].Footprints) != 2 {
		t.Fatalf("good source footprints = %d, want one per SCI chip", len(groups[0].Footprints))
	}
	if groups[1].Error == "" || len(groups[1].Footprints) != 0 {
		t.Fatalf("invalid source = %#v, want warning and no footprint", groups[1])
	}
}

func TestPlanFootprintPreviewUsesBuildScaleBounds(t *testing.T) {
	input := makeInput("one.fits", 10, 8, nil, headerWithCRPIX(5, 4))
	_, width, height, err := PlanFootprintPreview([]Input{input}, 2)
	if err != nil {
		t.Fatalf("PlanFootprintPreview() error = %v", err)
	}
	if width != 20 || height != 16 {
		t.Fatalf("canvas size = %dx%d, want 20x16", width, height)
	}
}

func TestResolvePreviewScaleUsesValidReferenceForFinalScale(t *testing.T) {
	invalid := makeInput("bad-first.fits", 10, 8, nil, headerWithCRPIX(5, 4))
	invalid.HDU.Header.Cards["CD2_2"] = "0"
	valid := makeInput("valid-second.fits", 10, 8, nil, headerWithCRPIX(5, 4))
	valid.HDU.Header.Cards["CD1_1"] = "0.00002"
	valid.HDU.Header.Cards["CD2_2"] = "0.00002"

	scale := ResolvePreviewScale([]Input{invalid, valid}, 1, 0.072)
	if math.Abs(scale-1) > 1e-12 {
		t.Fatalf("ResolvePreviewScale() = %v, want 1 from valid reference plate scale", scale)
	}
	groups, width, height, err := PlanFootprintPreview([]Input{invalid, valid}, scale)
	if err != nil {
		t.Fatalf("PlanFootprintPreview() error = %v", err)
	}
	if width != 11 || height != 9 {
		t.Fatalf("canvas size = %dx%d, want Build-parity 11x9 at resolved scale", width, height)
	}
	if len(groups) != 2 || groups[0].Error == "" || len(groups[0].Footprints) != 0 || len(groups[1].Footprints) != 1 {
		t.Fatalf("groups = %#v, want invalid warning first and valid outline second", groups)
	}
}
