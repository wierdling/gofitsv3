package ui

import (
	"encoding/csv"
	"io"

	"gofitsv3/internal/mosaic"
)

type alignmentResultRow struct {
	stateIdx int
	result   mosaic.StarAlignmentResult
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
		rows = append(rows, alignmentResultRow{stateIdx: stateIdx, result: results[resultIdx]})
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
