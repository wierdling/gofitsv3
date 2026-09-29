package mosaic

import (
	"fmt"
	"math"
)

// FootprintPreview describes the metadata-only placement of one source file.
// A source can have several footprints when its FITS file contains multiple
// SCI extensions; SourceNumber remains the same for all of them.
type FootprintPreview struct {
	SourcePath   string
	SourceNumber int
	Footprints   [][4][2]float64
	Status       string
	Error        string
}

// PlanFootprintPreview uses the same WCS planner as Build, but does not read or
// retain image pixels. The returned coordinates are in a common drizzle canvas
// whose dimensions are returned in canvasWidth/canvasHeight.
func PlanFootprintPreview(inputs []Input, scale float64) ([]FootprintPreview, int, int, error) {
	if len(inputs) == 0 {
		return nil, 0, 0, fmt.Errorf("no inputs selected")
	}
	if !isFinite64(scale) || scale <= 0 {
		scale = 1
	}

	// Prefer a valid WCS input as reference so one malformed candidate does not
	// suppress outlines for every other candidate.
	ordered := append([]Input(nil), inputs...)
	refIndex := previewReferenceIndex(ordered, scale)
	if refIndex < 0 {
		out := previewGroups(inputs)
		for i := range out {
			out[i].Status = "warning: invalid or missing WCS"
			out[i].Error = "no candidate has a valid WCS"
		}
		return out, 0, 0, fmt.Errorf("no inputs have a valid WCS")
	}
	ordered[0], ordered[refIndex] = ordered[refIndex], ordered[0]
	planned, statuses, minX, minY, maxX, maxY, err := planInputs(ordered, scale)
	if err != nil {
		return previewGroups(inputs), 0, 0, err
	}
	out := previewGroups(inputs)
	groupIndex := func(key string) int {
		for i := range out {
			if out[i].SourcePath == key {
				return i
			}
		}
		return -1
	}
	// Preserve planner failures (for example a malformed WCS on one chip) on
	// the source group even though planInputs can continue with other inputs.
	for i, status := range statuses {
		if status.Status != "failed" {
			continue
		}
		key := ordered[i].Path
		if ordered[i].SourcePath != "" {
			key = ordered[i].SourcePath
		}
		if idx := groupIndex(key); idx >= 0 {
			out[idx].Status = "warning: invalid or missing WCS"
			out[idx].Error = status.Error
			if out[idx].Error == "" {
				out[idx].Error = "input failed WCS planning"
			}
		}
	}
	for i := range planned {
		p := planned[i]
		key := p.input.Path
		if p.input.SourcePath != "" {
			key = p.input.SourcePath
		}
		idx := groupIndex(key)
		if idx < 0 {
			continue
		}
		out[idx].Status = "ready"
		chips := p.input.ChipFootprints
		if len(chips) == 0 {
			trim := float64(effectiveEdgeTrimForInput(p, p.input.HDU.Data.Width, scale))
			w, h := float64(p.input.HDU.Data.Width), float64(p.input.HDU.Data.Height)
			chips = [][4][2]float64{{{trim, trim}, {w - trim - 1, trim}, {trim, h - trim - 1}, {w - trim - 1, h - trim - 1}}}
		}
		for _, chip := range chips {
			var fp [4][2]float64
			for ci, sc := range chip {
				rx, ry := p.mapPixel(sc[0], sc[1])
				fp[ci] = [2]float64{(rx - minX) * scale, (ry - minY) * scale}
			}
			out[idx].Footprints = append(out[idx].Footprints, fp)
		}
	}
	for i := range out {
		if out[i].Status == "" {
			out[i].Status = "warning: invalid or missing WCS"
		}
	}
	w := int(math.Ceil((maxX - minX + 1) * scale))
	h := int(math.Ceil((maxY - minY + 1) * scale))
	return out, w, h, nil
}

// ResolvePreviewScale matches Build's FinalScale-to-multiplier fallback for
// the metadata-only preview. Lock-to-reference is resolved by Build after a
// reference-only input is loaded and therefore is not applicable here.
func ResolvePreviewScale(inputs []Input, scale, finalScale float64) float64 {
	if finalScale > 0 && len(inputs) > 0 {
		refIndex := previewReferenceIndex(inputs, 1)
		if refIndex < 0 {
			refIndex = 0
		}
		if plateScale, ok := NativePlateScaleArcsec(inputs[refIndex]); ok && plateScale > 0 {
			return plateScale / finalScale
		}
		return finalScale
	}
	if scale > 0 && isFinite64(scale) {
		return scale
	}
	return 1
}

func previewReferenceIndex(inputs []Input, scale float64) int {
	for i := range inputs {
		if _, _, _, _, _, _, err := planInputs([]Input{inputs[i]}, scale); err == nil {
			return i
		}
	}
	return -1
}

func inputSourceKey(inputs []Input, i int) string {
	if inputs[i].SourcePath != "" {
		return inputs[i].SourcePath
	}
	return inputs[i].Path
}

func previewGroups(inputs []Input) []FootprintPreview {
	out := make([]FootprintPreview, 0, len(inputs))
	indices := make(map[string]int, len(inputs))
	for i := range inputs {
		key := inputSourceKey(inputs, i)
		idx, ok := indices[key]
		if !ok {
			idx = len(out)
			indices[key] = idx
			out = append(out, FootprintPreview{SourcePath: key, SourceNumber: idx + 1})
		}
	}
	return out
}
