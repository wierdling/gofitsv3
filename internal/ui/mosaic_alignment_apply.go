package ui

import (
	"errors"
	"fmt"

	"gofitsv3/internal/mosaic"
)

type alignmentSidecarSaver func(mosaic.Input, mosaic.Input, mosaic.StarAlignmentResult) error

// alignmentWorkset returns the effective reference followed only by targets
// that still need alignment. Loaded sidecars and locked user adjustments are
// deliberately retained in state but never sent back through star alignment.
// stateIndices maps each workset item back to state.inputs; -1 is an external
// reference baseline.
func (ws *mosaicWorkspace) alignmentWorkset() ([]mosaic.Input, []int) {
	reference, ok := effectiveMosaicAlignmentReference(ws.state.inputs, ws.state.referenceInput)
	if !ok {
		return nil, nil
	}

	inputs := []mosaic.Input{reference}
	stateIndices := []int{-1}
	if ws.state.referenceInput == nil {
		for i := range ws.state.inputs {
			if sameMosaicAlignmentInput(ws.state.inputs[i], reference) {
				stateIndices[0] = i
				break
			}
		}
	} else {
		inputs[0].ReferenceOnly = true
	}

	for i, input := range ws.state.inputs {
		if input.Excluded || sameMosaicAlignmentInput(input, reference) || input.OffsetLocked {
			continue
		}
		// A sidecar describes a transform into one reference frame. Multiple
		// designated references have different semantics, so retain the normal
		// multi-reference alignment behavior instead of reusing that cache.
		if ws.usesSingleAlignmentReference() && i < len(ws.state.statuses) && ws.state.statuses[i].Status == "loaded alignment" {
			continue
		}
		inputs = append(inputs, input)
		stateIndices = append(stateIndices, i)
	}
	return inputs, stateIndices
}

func (ws *mosaicWorkspace) usesSingleAlignmentReference() bool {
	return ws.state.referenceInput != nil || ws.state.alignmentSettings.NumRefs <= 1
}

func (ws *mosaicWorkspace) alignmentNumRefs() int {
	if ws.state.referenceInput != nil {
		return 1
	}
	numRefs := ws.state.alignmentSettings.NumRefs
	if numRefs < 1 {
		return 1
	}
	return numRefs
}

// applyAlignmentRowsAndSave applies only reviewed successful rows, then saves
// their sidecars against the current effective reference. The reference and
// unchecked rows are left untouched. A save error is returned so callers can
// report it without implying persistence succeeded.
func (ws *mosaicWorkspace) applyAlignmentRowsAndSave(rows []alignmentResultRow, checked func(int) bool, save alignmentSidecarSaver) error {
	reference, hasReference := effectiveMosaicAlignmentReference(ws.state.inputs, ws.state.referenceInput)
	var saveErrs []error
	for i, row := range rows {
		if !checked(i) || !row.result.Applied || row.stateIdx < 0 || row.stateIdx >= len(ws.state.inputs) {
			continue
		}
		input := &ws.state.inputs[row.stateIdx]
		if sameMosaicAlignmentInput(*input, reference) {
			continue
		}
		input.OffsetX = row.result.OffsetX
		input.OffsetY = row.result.OffsetY
		input.ManualTransform = row.result.ManualTransform
		input.HasManualTransform = row.result.HasManualTransform
		if row.stateIdx < len(ws.state.statuses) {
			ws.state.statuses[row.stateIdx].Status = "star aligned"
			ws.state.statuses[row.stateIdx].Error = ""
		}
		if !hasReference {
			saveErrs = append(saveErrs, fmt.Errorf("save alignment for %s: no reference image", mosaic.InputLabel(*input)))
			continue
		}
		if !ws.usesSingleAlignmentReference() {
			continue
		}
		if err := save(*input, reference, row.result); err != nil {
			saveErrs = append(saveErrs, fmt.Errorf("save alignment for %s: %w", mosaic.InputLabel(*input), err))
		}
	}
	return errors.Join(saveErrs...)
}
