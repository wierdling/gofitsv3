package ui

import (
	"testing"

	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

func TestMosaicAlignmentSidecarLoadsValidTransformsAndStatuses(t *testing.T) {
	inputs := []mosaic.Input{{Path: "reference.fits"}, {Path: "target.fits"}, {Path: "missing.fits"}}
	statuses := []mosaic.InputStatus{{Status: "loaded"}, {Status: "loaded"}, {Status: "loaded"}}
	transform := processing.AffineTransform{A: 1, E: 1, C: 2, F: -3}
	summary := loadMosaicAlignmentSidecarsWith(inputs, statuses, nil, func(target, reference mosaic.Input) mosaic.AlignmentSidecarLoadOutcome {
		if target.Path == "target.fits" && reference.Path == "reference.fits" {
			return mosaic.AlignmentSidecarLoadOutcome{Status: mosaic.AlignmentSidecarLoaded, Entry: &mosaic.AlignmentSidecarEntry{OffsetX: 2, OffsetY: -3, ManualTransform: transform, HasManualTransform: true}}
		}
		return mosaic.AlignmentSidecarLoadOutcome{Status: mosaic.AlignmentSidecarNotFound}
	})

	if !summary.HasReference || summary.Reference.Path != "reference.fits" || summary.Loaded != 1 {
		t.Fatalf("summary = %+v, want reference.fits and one loaded alignment", summary)
	}
	if got := inputs[1]; got.OffsetX != 2 || got.OffsetY != -3 || !got.HasManualTransform || got.ManualTransform != transform {
		t.Fatalf("loaded target = %+v", got)
	}
	if got := statuses[1]; got.Status != "loaded alignment" || got.OffsetX != 2 || got.OffsetY != -3 || !got.HasAffine {
		t.Fatalf("loaded status = %+v", got)
	}
	if inputs[2].OffsetX != 0 || statuses[2].Status != "loaded" {
		t.Fatalf("missing alignment should remain available: input=%+v status=%+v", inputs[2], statuses[2])
	}
}

func TestMosaicAlignmentSidecarReportsReferenceChangeOnce(t *testing.T) {
	staleTransform := processing.AffineTransform{A: 1, E: 1, C: 42, F: -7}
	inputs := []mosaic.Input{
		{Path: "one.fits", OffsetX: 42, OffsetY: -7, HasManualTransform: true, ManualTransform: staleTransform, OffsetLocked: true},
		{Path: "two.fits", OffsetX: 42, OffsetY: -7, HasManualTransform: true, ManualTransform: staleTransform, OffsetLocked: true},
	}
	statuses := []mosaic.InputStatus{{Status: "loaded"}, {Status: "loaded"}}
	reference := &mosaic.Input{Path: "baseline.fits", ReferenceOnly: true}
	summary := loadMosaicAlignmentSidecarsWith(inputs, statuses, reference, func(mosaic.Input, mosaic.Input) mosaic.AlignmentSidecarLoadOutcome {
		return mosaic.AlignmentSidecarLoadOutcome{Status: mosaic.AlignmentSidecarReferenceChanged}
	})

	if summary.ReferenceChanged != 2 || summary.ReferenceChangedNotice != mosaicAlignmentReferenceChangedMessage {
		t.Fatalf("summary = %+v, want aggregated reference-change warning", summary)
	}
	for i, input := range inputs {
		if input.OffsetX != 0 || input.OffsetY != 0 || input.HasManualTransform || input.OffsetLocked || input.ManualTransform != (processing.AffineTransform{}) {
			t.Fatalf("input %d retained stale project alignment: %+v", i, input)
		}
		if status := statuses[i]; status.Status != "alignment needs refresh" || status.OffsetX != 0 || status.OffsetY != 0 || status.HasAffine {
			t.Fatalf("status %d = %+v, want cleared alignment status", i, status)
		}
	}
}

func TestMosaicAlignmentSidecarUsesFirstEligibleInputAsReference(t *testing.T) {
	inputs := []mosaic.Input{{Path: "excluded.fits", Excluded: true}, {Path: "usable.fits"}}
	reference, ok := effectiveMosaicAlignmentReference(inputs, nil)
	if !ok || reference.Path != "usable.fits" {
		t.Fatalf("effective reference = (%+v, %t), want usable.fits", reference, ok)
	}
}
