package ui

import (
	"encoding/csv"
	"io"

	"gofitsv3/internal/mosaic"
)

type alignmentResultRow struct {
	stateIdx int
	result   mosaic.StarAlignmentResult
	// Target and reference identify the frames used by the worker. Keeping
	// immutable identities with the result prevents a later reload/reorder from
	// applying a stale transform to a different frame at the same index.
	target              mosaic.Input
	reference           mosaic.Input
	targetGeneration    uint64
	referenceGeneration uint64
}

// alignmentInputSnapshot freezes all identity-bearing state used by a worker.
// Callers take it under inputMu before launching background alignment so row
// binding never consults live, concurrently replaced inputs.
type alignmentInputSnapshot struct {
	inputs      []mosaic.Input
	statuses    []mosaic.InputStatus
	reference   *mosaic.Input
	generations map[string]uint64
}

func (ws *mosaicWorkspace) alignmentInputSnapshot() alignmentInputSnapshot {
	ws.inputMu.RLock()
	defer ws.inputMu.RUnlock()
	s := alignmentInputSnapshot{inputs: append([]mosaic.Input(nil), ws.state.inputs...), statuses: append([]mosaic.InputStatus(nil), ws.state.statuses...), generations: make(map[string]uint64, len(ws.inputGenerations))}
	for k, v := range ws.inputGenerations {
		s.generations[k] = v
	}
	if ws.state.referenceInput != nil {
		ref := *ws.state.referenceInput
		s.reference = &ref
	}
	return s
}

// buildAlignmentResultRows keeps the review list in the same order as the
// Input Frames list. Alignment operates on a filtered copy, so result indexes
// must be mapped back to the original state indexes before displaying them.
func buildAlignmentResultRows(inputs []mosaic.Input, results []mosaic.StarAlignmentResult, hasExternalReference bool) []alignmentResultRow {
	rows := make([]alignmentResultRow, 0, len(inputs))
	resultIdx := 0
	if hasExternalReference {
		resultIdx = 1
	}
	for stateIdx, input := range inputs {
		if input.Excluded {
			continue
		}
		if input.OffsetLocked {
			resultIdx++
			continue
		}
		if resultIdx >= len(results) {
			break
		}
		rows = append(rows, alignmentResultRow{stateIdx: stateIdx, result: results[resultIdx], target: input})
		resultIdx++
	}
	return rows
}

// buildAlignmentResultRowsForStateIndices maps alignment results from a
// reference-plus-target workset back to the workspace input indexes. A -1
// state index represents the external reference baseline and is not shown.
func buildAlignmentResultRowsForStateIndices(results []mosaic.StarAlignmentResult, stateIndices []int) []alignmentResultRow {
	rows := make([]alignmentResultRow, 0, len(stateIndices))
	for resultIndex, stateIdx := range stateIndices {
		if resultIndex >= len(results) {
			break
		}
		if stateIdx < 0 {
			continue
		}
		rows = append(rows, alignmentResultRow{stateIdx: stateIdx, result: results[resultIndex]})
	}
	return rows
}

func bindAlignmentResultIdentities(rows []alignmentResultRow, inputs []mosaic.Input, reference mosaic.Input) {
	for i := range rows {
		if rows[i].stateIdx >= 0 && rows[i].stateIdx < len(inputs) {
			rows[i].target = inputs[rows[i].stateIdx]
			rows[i].reference = reference
		}
	}
}

func bindAlignmentResultGenerations(rows []alignmentResultRow, ws *mosaicWorkspace) {
	ws.inputMu.RLock()
	defer ws.inputMu.RUnlock()
	for i := range rows {
		if rows[i].stateIdx >= 0 && rows[i].stateIdx < len(ws.state.inputs) {
			rows[i].targetGeneration = ws.inputGeneration(ws.state.inputs[rows[i].stateIdx])
		}
		rows[i].referenceGeneration = ws.inputGeneration(rows[i].reference)
	}
}

func bindAlignmentResultSnapshot(rows []alignmentResultRow, snapshot alignmentInputSnapshot) {
	for i := range rows {
		if rows[i].stateIdx >= 0 && rows[i].stateIdx < len(snapshot.inputs) {
			rows[i].target = snapshot.inputs[rows[i].stateIdx]
			rows[i].targetGeneration = snapshot.generations[artifactMaskTargetKey(rows[i].target)]
		}
		if snapshot.reference != nil {
			rows[i].reference = *snapshot.reference
		}
		rows[i].referenceGeneration = snapshot.generations[artifactMaskTargetKey(rows[i].reference)]
	}
}

func writeAlignmentCSV(w io.Writer, rows []alignmentResultRow, inputs []mosaic.Input) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"image_name", "aligned", "message"}); err != nil {
		return err
	}
	for _, row := range rows {
		if row.stateIdx < 0 || row.stateIdx >= len(inputs) {
			continue
		}
		message := row.result.Error
		aligned := "false"
		if row.result.Applied {
			aligned = "true"
		}
		if err := cw.Write([]string{mosaic.InputLabel(inputs[row.stateIdx]), aligned, message}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
