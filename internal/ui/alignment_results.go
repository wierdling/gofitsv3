package ui

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

// alignmentDiagnosticsText is deliberately pure so the dialog and tests use
// the same report wording. Residual samples remain available for plot widgets.
func alignmentDiagnosticsText(r mosaic.StarAlignmentResult) string {
	transform := processing.SummarizeAlignmentTransform(r.ManualTransform)
	text := fmt.Sprintf("Detected %d/%d  Matched %d  RANSAC accepted %d  rejected %d\nX RMS %.3f  Y RMS %.3f  Radial RMS %.3f  Median %.3f  Max %.3f\nRANSAC consensus %.1f%%  Final support %.1f%%\nShift (%.3f, %.3f)  Rotation %.4f°  Scale %.6f  Skew %.6f\nAffine [%.6g %.6g %.6g; %.6g %.6g %.6g]", r.DetectedSourceStars, r.DetectedReferenceStars, r.MatchedStars, r.AcceptedStars, r.RejectedStars, r.XRMS, r.YRMS, r.RadialRMS, r.MedianError, r.MaxError, r.RANSACInlierPercent, r.FinalSupportPercent, transform.ShiftX, transform.ShiftY, transform.RotationDeg, transform.Scale, transform.Skew, transform.A, transform.B, transform.C, transform.D, transform.E, transform.F)
	if r.RScaleRMS > 0 {
		text += fmt.Sprintf("\nRScale RMS %.3f (max %.3f) vs affine %.3f", r.RScaleRMS, r.RScaleMaxError, r.RadialRMS)
	}
	if r.Warnings != "" {
		text += "\nWarnings: " + r.Warnings
	}
	return text
}

func alignmentResiduals(r mosaic.StarAlignmentResult) []processing.Residual {
	var out []processing.Residual
	if r.Residuals != "" {
		_ = json.Unmarshal([]byte(r.Residuals), &out)
	}
	return out
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// alignmentPlotImage renders four compact validation views: matched-star
// overlay, residual vectors, X/Y residuals versus position, and spatial
// distribution. All points come from the final-transform residual samples.
func alignmentPlotImage(r mosaic.StarAlignmentResult) image.Image {
	const w, h = 760, 440
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(x, y, color.RGBA{18, 20, 25, 255})
		}
	}
	res := alignmentResiduals(r)
	if len(res) == 0 {
		return out
	}
	minX, maxX, minY, maxY := res[0].X, res[0].X, res[0].Y, res[0].Y
	for _, v := range res[1:] {
		minX = math.Min(minX, v.X)
		maxX = math.Max(maxX, v.X)
		minY = math.Min(minY, v.Y)
		maxY = math.Max(maxY, v.Y)
	}
	if maxX == minX {
		maxX = minX + 1
	}
	if maxY == minY {
		maxY = minY + 1
	}
	for qi := 0; qi < 4; qi++ {
		ox := (qi % 2) * 380
		oy := (qi / 2) * 220
		for x := ox + 8; x < ox+372; x++ {
			out.Set(x, oy+210, color.RGBA{70, 75, 85, 255})
		}
		for y := oy + 8; y < oy+212; y++ {
			out.Set(ox+8, y, color.RGBA{70, 75, 85, 255})
		}
	}
	mapP := func(v float64, a, b float64, span int) int { return int(float64(span-24)*(v-a)/(b-a)) + 12 }
	drawLine := func(x0, y0, x1, y1 int, c color.Color) {
		dx, dy := absInt(x1-x0), absInt(y1-y0)
		sx, sy := -1, -1
		if x0 < x1 {
			sx = 1
		}
		if y0 < y1 {
			sy = 1
		}
		err := dx - dy
		for {
			if x0 >= 0 && x0 < w && y0 >= 0 && y0 < h {
				out.Set(x0, y0, c)
			}
			if x0 == x1 && y0 == y1 {
				break
			}
			e2 := 2 * err
			if e2 > -dy {
				err -= dy
				x0 += sx
			}
			if e2 < dx {
				err += dx
				y0 += sy
			}
		}
	}
	for _, v := range res {
		x, y := mapP(v.X, minX, maxX, 380), mapP(v.Y, minY, maxY, 220) // overlay
		out.Set(x, y, color.RGBA{80, 210, 255, 255})                   // projected/source
		rx, ry := mapP(v.TargetX, minX, maxX, 380), mapP(v.TargetY, minY, maxY, 220)
		out.Set(rx, ry, color.RGBA{255, 190, 80, 255}) // matched reference
		drawLine(x, y, rx, ry, color.RGBA{180, 180, 230, 255})
		// vector in top-right
		vx, vy := 380+x, y
		ex := vx + int(v.DX*8)
		ey := vy + int(v.DY*8)
		drawLine(vx, vy, ex, ey, color.RGBA{255, 100, 100, 255})
		// x/y residuals versus position in bottom-left/right
		xp, yp := mapP(v.X, minX, maxX, 380), 220+mapP(v.DX, -math.Max(1, r.MaxError), math.Max(1, r.MaxError), 220)
		if xp < 380 && yp >= 220 {
			out.Set(xp, yp, color.RGBA{255, 180, 90, 255})
		}
		xp2, yp2 := 380+mapP(v.X, minX, maxX, 380), 220+mapP(v.DY, -math.Max(1, r.MaxError), math.Max(1, r.MaxError), 220)
		if xp2 < w && yp2 >= 220 {
			out.Set(xp2, yp2, color.RGBA{140, 255, 140, 255})
		}
	}
	return out
}

func showAlignmentDiagnosticsDialog(win fyne.Window, row alignmentResultRow, name string) {
	plot := canvas.NewRaster(func(_ int, _ int) image.Image { return alignmentPlotImage(row.result) })
	plot.SetMinSize(fyne.NewSize(760, 440))
	export := widget.NewButton("Export residual CSV...", func() {
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()
			if filepath.Ext(path) == "" {
				path += ".csv"
			}
			f, e := os.Create(path)
			if e != nil {
				dialog.ShowError(e, win)
				return
			}
			e = writeAlignmentResidualCSV(f, row, name)
			ce := f.Close()
			if e != nil {
				dialog.ShowError(e, win)
			} else if ce != nil {
				dialog.ShowError(ce, win)
			} else {
				dialog.ShowInformation("Exported", "Residual CSV exported successfully.", win)
			}
		}, win)
		save.SetFileName(name + "_residuals.csv")
		save.SetFilter(storage.NewExtensionFileFilter([]string{".csv"}))
		save.Show()
	})
	dialog.NewCustom("Alignment Diagnostics — "+name, "Close", container.NewBorder(widget.NewLabel(alignmentDiagnosticsText(row.result)), nil, nil, nil, container.NewVBox(plot, export)), win).Show()
}

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
	if err := cw.Write([]string{"image_name", "aligned", "message", "detected_source", "detected_reference", "matched", "accepted", "rejected", "x_rms", "y_rms", "radial_rms", "median_residual", "max_residual", "ransac_inlier_percent", "final_support", "final_support_percent", "shift_x", "shift_y", "affine_a", "affine_b", "affine_c", "affine_d", "affine_e", "affine_f"}); err != nil {
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
		r := row.result
		fields := []string{mosaic.InputLabel(inputs[row.stateIdx]), aligned, message,
			fmt.Sprintf("%d", r.DetectedSourceStars), fmt.Sprintf("%d", r.DetectedReferenceStars), fmt.Sprintf("%d", r.MatchedStars), fmt.Sprintf("%d", r.AcceptedStars), fmt.Sprintf("%d", r.RejectedStars),
			fmt.Sprintf("%.6g", r.XRMS), fmt.Sprintf("%.6g", r.YRMS), fmt.Sprintf("%.6g", r.RadialRMS), fmt.Sprintf("%.6g", r.MedianError), fmt.Sprintf("%.6g", r.MaxError), fmt.Sprintf("%.4g", r.RANSACInlierPercent),
			fmt.Sprintf("%d", r.FinalSupport), fmt.Sprintf("%.4g", r.FinalSupportPercent),
			fmt.Sprintf("%.6g", r.OffsetX), fmt.Sprintf("%.6g", r.OffsetY), fmt.Sprintf("%.6g", r.ManualTransform.A), fmt.Sprintf("%.6g", r.ManualTransform.B), fmt.Sprintf("%.6g", r.ManualTransform.C), fmt.Sprintf("%.6g", r.ManualTransform.D), fmt.Sprintf("%.6g", r.ManualTransform.E), fmt.Sprintf("%.6g", r.ManualTransform.F),
		}
		if err := cw.Write(fields); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// writeAlignmentResidualCSV exports the exact residual samples used by the
// report. Coordinates and vectors are in reference-pixel space.
func writeAlignmentResidualCSV(w io.Writer, row alignmentResultRow, inputName string) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"image_name", "x", "y", "target_x", "target_y", "dx", "dy", "radial"}); err != nil {
		return err
	}
	var residuals []processing.Residual
	if row.result.Residuals != "" {
		if err := json.Unmarshal([]byte(row.result.Residuals), &residuals); err != nil {
			return err
		}
	}
	for _, residual := range residuals {
		if err := cw.Write([]string{inputName, fmt.Sprintf("%.8g", residual.X), fmt.Sprintf("%.8g", residual.Y), fmt.Sprintf("%.8g", residual.TargetX), fmt.Sprintf("%.8g", residual.TargetY), fmt.Sprintf("%.8g", residual.DX), fmt.Sprintf("%.8g", residual.DY), fmt.Sprintf("%.8g", residual.Radial)}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
