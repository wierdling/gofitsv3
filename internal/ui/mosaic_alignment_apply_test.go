package ui

import (
	"errors"
	"testing"

	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

func TestAlignmentWorksetSkipsLoadedAndLockedTargets(t *testing.T) {
	ws := &mosaicWorkspace{state: &mosaicState{
		inputs: []mosaic.Input{
			{Path: "reference.fits"},
			{Path: "cached.fits"},
			{Path: "locked.fits", OffsetLocked: true},
			{Path: "missing.fits"},
		},
		statuses: []mosaic.InputStatus{{Status: "reference"}, {Status: "loaded alignment"}, {}, {}},
	}}

	inputs, indexes := ws.alignmentWorkset()
	if len(inputs) != 2 || len(indexes) != 2 {
		t.Fatalf("workset lengths = %d/%d, want 2/2", len(inputs), len(indexes))
	}
	if inputs[0].Path != "reference.fits" || indexes[0] != 0 {
		t.Fatalf("reference = %q at %d, want reference.fits at 0", inputs[0].Path, indexes[0])
	}
	if inputs[1].Path != "missing.fits" || indexes[1] != 3 {
		t.Fatalf("target = %q at %d, want missing.fits at 3", inputs[1].Path, indexes[1])
	}
}

func TestAlignmentWorksetPreservesMultiReferenceBehavior(t *testing.T) {
	ws := &mosaicWorkspace{state: &mosaicState{
		alignmentSettings: models.AlignmentSettings{NumRefs: 2},
		inputs:            []mosaic.Input{{Path: "reference.fits"}, {Path: "cached.fits"}, {Path: "missing.fits"}},
		statuses:          []mosaic.InputStatus{{Status: "reference"}, {Status: "loaded alignment"}, {}},
	}}

	inputs, indexes := ws.alignmentWorkset()
	if len(inputs) != 3 || len(indexes) != 3 {
		t.Fatalf("workset lengths = %d/%d, want 3/3", len(inputs), len(indexes))
	}
	if indexes[1] != 1 || ws.alignmentNumRefs() != 2 {
		t.Fatalf("multi-reference cache handling = indexes %v, numRefs %d", indexes, ws.alignmentNumRefs())
	}
}

func TestApplyAlignmentRowsAndSavePreservesReferenceAndUnchecked(t *testing.T) {
	ws := &mosaicWorkspace{state: &mosaicState{
		inputs:   []mosaic.Input{{Path: "reference.fits", OffsetX: 9}, {Path: "apply.fits"}, {Path: "unchecked.fits", OffsetX: 7}},
		statuses: []mosaic.InputStatus{{Status: "reference"}, {}, {}},
	}}
	rows := []alignmentResultRow{
		{stateIdx: 0, result: mosaic.StarAlignmentResult{Applied: true, OffsetX: 1}},
		{stateIdx: 1, result: mosaic.StarAlignmentResult{Applied: true, OffsetX: 2, OffsetY: -3, HasManualTransform: true, ManualTransform: processing.AffineTransform{A: 1}}},
		{stateIdx: 2, result: mosaic.StarAlignmentResult{Applied: true, OffsetX: 4}},
	}
	var saved []string
	err := ws.applyAlignmentRowsAndSave(rows, func(i int) bool { return i != 2 }, func(target, reference mosaic.Input, result mosaic.StarAlignmentResult) error {
		saved = append(saved, target.Path+"->"+reference.Path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ws.state.inputs[0].OffsetX; got != 9 {
		t.Fatalf("reference offset = %v, want preserved 9", got)
	}
	if got := ws.state.inputs[1]; got.OffsetX != 2 || got.OffsetY != -3 || !got.HasManualTransform {
		t.Fatalf("applied input = %+v", got)
	}
	if got := ws.state.inputs[2].OffsetX; got != 7 {
		t.Fatalf("unchecked offset = %v, want preserved 7", got)
	}
	if len(saved) != 1 || saved[0] != "apply.fits->reference.fits" {
		t.Fatalf("saved = %v", saved)
	}
}

func TestApplyAlignmentRowsAndSaveReturnsSaveErrorAfterApplying(t *testing.T) {
	ws := &mosaicWorkspace{state: &mosaicState{
		inputs:   []mosaic.Input{{Path: "reference.fits"}, {Path: "target.fits"}},
		statuses: []mosaic.InputStatus{{Status: "reference"}, {}},
	}}
	err := ws.applyAlignmentRowsAndSave([]alignmentResultRow{{stateIdx: 1, result: mosaic.StarAlignmentResult{Applied: true, OffsetX: 5}}}, func(int) bool { return true }, func(mosaic.Input, mosaic.Input, mosaic.StarAlignmentResult) error {
		return errors.New("disk full")
	})
	if err == nil {
		t.Fatal("expected save error")
	}
	if got := ws.state.inputs[1].OffsetX; got != 5 {
		t.Fatalf("offset = %v, want applied value despite save failure", got)
	}
}
