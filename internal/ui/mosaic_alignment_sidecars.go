package ui

import (
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

// mosaicAlignmentReferenceChangedMessage is shown once after loading a set of
// inputs whose saved transforms refer to a changed reference image.
const mosaicAlignmentReferenceChangedMessage = "Saved alignments were not loaded because the reference image changed. Re-align before drizzling."

type mosaicAlignmentSidecarLoadSummary struct {
	Reference              mosaic.Input
	HasReference           bool
	Loaded                 int
	ReferenceChanged       int
	ReferenceChangedNotice string
}

type alignmentSidecarLoader func(mosaic.Input, mosaic.Input) mosaic.AlignmentSidecarLoadOutcome

// effectiveMosaicAlignmentReference uses the explicit reference baseline when
// present. Otherwise the first usable input is the alignment reference.
func effectiveMosaicAlignmentReference(inputs []mosaic.Input, referenceInput *mosaic.Input) (mosaic.Input, bool) {
	if referenceInput != nil && !referenceInput.Excluded {
		return *referenceInput, true
	}
	for _, input := range inputs {
		if !input.Excluded {
			return input, true
		}
	}
	return mosaic.Input{}, false
}

// loadMosaicAlignmentSidecars restores valid per-image transforms once the
// complete input set and effective reference are known. Stale or invalid
// sidecars clear any restored project alignment so it cannot be drizzled
// against a different image identity or reference frame.
func loadMosaicAlignmentSidecars(inputs []mosaic.Input, statuses []mosaic.InputStatus, referenceInput *mosaic.Input) mosaicAlignmentSidecarLoadSummary {
	return loadMosaicAlignmentSidecarsWith(inputs, statuses, referenceInput, mosaic.LoadValidatedAlignmentSidecar)
}

func loadMosaicAlignmentSidecarsWith(inputs []mosaic.Input, statuses []mosaic.InputStatus, referenceInput *mosaic.Input, load alignmentSidecarLoader) mosaicAlignmentSidecarLoadSummary {
	summary := mosaicAlignmentSidecarLoadSummary{}
	reference, ok := effectiveMosaicAlignmentReference(inputs, referenceInput)
	if !ok {
		return summary
	}
	summary.Reference, summary.HasReference = reference, true

	for i := range inputs {
		if inputs[i].Excluded || sameMosaicAlignmentInput(inputs[i], reference) {
			continue
		}
		outcome := load(inputs[i], reference)
		switch outcome.Status {
		case mosaic.AlignmentSidecarLoaded:
			if outcome.Entry == nil {
				continue
			}
			inputs[i].OffsetX = outcome.Entry.OffsetX
			inputs[i].OffsetY = outcome.Entry.OffsetY
			inputs[i].ManualTransform = outcome.Entry.ManualTransform
			inputs[i].HasManualTransform = outcome.Entry.HasManualTransform
			summary.Loaded++
			if i < len(statuses) {
				statuses[i].Status = "loaded alignment"
				statuses[i].OffsetX = inputs[i].OffsetX
				statuses[i].OffsetY = inputs[i].OffsetY
				statuses[i].HasAffine = inputs[i].HasManualTransform
			}
		case mosaic.AlignmentSidecarReferenceChanged:
			summary.ReferenceChanged++
			clearMosaicAlignment(&inputs[i], statuses, i)
		case mosaic.AlignmentSidecarTargetChanged, mosaic.AlignmentSidecarInvalid:
			clearMosaicAlignment(&inputs[i], statuses, i)
		}
	}
	if summary.ReferenceChanged > 0 {
		summary.ReferenceChangedNotice = mosaicAlignmentReferenceChangedMessage
	}
	return summary
}

func clearMosaicAlignment(input *mosaic.Input, statuses []mosaic.InputStatus, index int) {
	input.OffsetX = 0
	input.OffsetY = 0
	input.ManualTransform = processing.AffineTransform{}
	input.HasManualTransform = false
	input.OffsetLocked = false
	if index < len(statuses) {
		statuses[index].Status = "alignment needs refresh"
		statuses[index].OffsetX = 0
		statuses[index].OffsetY = 0
		statuses[index].HasAffine = false
	}
}

func sameMosaicAlignmentInput(a, b mosaic.Input) bool {
	return a.Path == b.Path && a.SCIExt == b.SCIExt
}
