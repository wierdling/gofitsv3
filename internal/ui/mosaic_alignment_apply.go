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
	ws.inputMu.RLock()
	defer ws.inputMu.RUnlock()
	return alignmentWorksetFor(ws.state.inputs, ws.state.statuses, ws.state.referenceInput, ws.state.alignmentSettings.NumRefs)
}

func alignmentWorksetFor(stateInputs []mosaic.Input, statuses []mosaic.InputStatus, external *mosaic.Input, numRefs int) ([]mosaic.Input, []int) {
	reference, ok := effectiveMosaicAlignmentReference(stateInputs, external)
	if !ok {
		return nil, nil
	}

	inputs := []mosaic.Input{reference}
	stateIndices := []int{-1}
	if external == nil {
		for i := range stateInputs {
			if sameMosaicAlignmentInput(stateInputs[i], reference) {
				stateIndices[0] = i
				break
			}
		}
	} else {
		inputs[0].ReferenceOnly = true
	}

	for i, input := range stateInputs {
		if input.Excluded || sameMosaicAlignmentInput(input, reference) || input.OffsetLocked {
			continue
		}
		// A sidecar describes a transform into one reference frame. Multiple
		// designated references have different semantics, so retain the normal
		// multi-reference alignment behavior instead of reusing that cache.
		if (external != nil || numRefs <= 1) && i < len(statuses) && statuses[i].Status == "loaded alignment" {
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
	ws.inputMu.RLock()
	defer ws.inputMu.RUnlock()
	return alignmentNumRefsFor(ws.state.referenceInput, ws.state.alignmentSettings.NumRefs)
}

func alignmentNumRefsFor(external *mosaic.Input, numRefs int) int {
	if external != nil {
		return 1
	}
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
	ws.inputMu.Lock()
	defer ws.inputMu.Unlock()
	reference, hasReference := effectiveMosaicAlignmentReference(ws.state.inputs, ws.state.referenceInput)
	var saveErrs []error
	for i, row := range rows {
		if !checked(i) || !row.result.Applied || row.stateIdx < 0 || row.stateIdx >= len(ws.state.inputs) {
			continue
		}
		input := &ws.state.inputs[row.stateIdx]
		if row.target.Path != "" && !sameMosaicAlignmentInput(*input, row.target) {
			continue
		}
		if row.reference.Path != "" && (!hasReference || !sameMosaicAlignmentInput(reference, row.reference)) {
			continue
		}
		if row.target.Path != "" && ws.inputGenerationLocked(*input) != row.targetGeneration {
			continue
		}
		if row.reference.Path != "" && ws.inputGenerationLocked(reference) != row.referenceGeneration {
			continue
		}
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
