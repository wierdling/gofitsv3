package ui

import (
	"fmt"
	"path/filepath"

	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
)

type artifactMaskEditorTool int

const (
	artifactMaskToolBrush artifactMaskEditorTool = iota
	artifactMaskToolRectangle
	artifactMaskToolPolygon
)

type artifactMaskEditorController struct {
	inputIndex       int
	input            mosaic.Input
	purpose          models.ArtifactMaskPurpose
	sourceMode       models.ArtifactMaskSourceMode
	sourceKey        string
	name             string
	width            int
	height           int
	pixels           []float32
	sourceGeneration uint64
	mask             []bool
	preview          []bool
	undo             [][]bool
	redo             [][]bool
}

func newArtifactMaskEditorController(inputIndex int, input mosaic.Input, existing *models.ArtifactMaskDocument) (*artifactMaskEditorController, error) {
	return newArtifactMaskEditorControllerForPurpose(inputIndex, input, models.ArtifactMaskPurposeMIRIArtifact, existing)
}

func newArtifactMaskEditorControllerForPurpose(inputIndex int, input mosaic.Input, purpose models.ArtifactMaskPurpose, existing *models.ArtifactMaskDocument) (*artifactMaskEditorController, error) {
	if purpose == "" {
		purpose = models.ArtifactMaskPurposeMIRIArtifact
	}
	w := input.HDU.Data.Width
	h := input.HDU.Data.Height
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("input %s has invalid dimensions %dx%d", mosaic.InputKey(input), w, h)
	}
	mask, err := mosaic.NewZeroArtifactMask(w, h)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Width == w && existing.Height == h {
		for _, op := range existing.Operations {
			if op.Kind != models.ArtifactMaskRegionRaster || op.Width != w || op.Height != h || len(op.Mask) != w*h {
				continue
			}
			for i, v := range op.Mask {
				mask[i] = v != 0
			}
		}
	}
	return &artifactMaskEditorController{
		inputIndex: inputIndex,
		input:      input,
		purpose:    purpose,
		sourceMode: models.ArtifactMaskSourceInput,
		sourceKey:  artifactMaskTargetKey(input),
		name:       mosaic.InputLabel(input),
		width:      w,
		height:     h,
		pixels:     append([]float32(nil), input.HDU.Data.Pixels...),
		mask:       mask,
	}, nil
}

func newMosaicArtifactMaskEditorController(width, height int, pixels []float32, existing *models.ArtifactMaskDocument) (*artifactMaskEditorController, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("mosaic result has invalid dimensions %dx%d", width, height)
	}
	mask, err := mosaic.NewZeroArtifactMask(width, height)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Width == width && existing.Height == height && !existing.Stale {
		for _, op := range existing.Operations {
			if op.Kind != models.ArtifactMaskRegionRaster || op.Width != width || op.Height != height || len(op.Mask) != width*height {
				continue
			}
			for i, v := range op.Mask {
				mask[i] = v != 0
			}
		}
	}
	return &artifactMaskEditorController{
		inputIndex: -1,
		purpose:    models.ArtifactMaskPurposeMIRIArtifact,
		sourceMode: models.ArtifactMaskSourceMosaic,
		sourceKey:  artifactMaskMosaicSourceKey(width, height),
		name:       "Current Mosaic",
		width:      width,
		height:     height,
		pixels:     pixels,
		mask:       mask,
	}, nil
}

func (c *artifactMaskEditorController) applySelection(selection []bool, erase bool) error {
	if len(selection) != c.width*c.height {
		return fmt.Errorf("selection len=%d vs dimensions %dx%d", len(selection), c.width, c.height)
	}
	c.pushUndo()
	for i, selected := range selection {
		if !selected {
			continue
		}
		c.mask[i] = !erase
	}
	c.redo = nil
	return nil
}

func (c *artifactMaskEditorController) setPreview(selection []bool) error {
	if len(selection) != c.width*c.height {
		return fmt.Errorf("preview len=%d vs dimensions %dx%d", len(selection), c.width, c.height)
	}
	c.preview = cloneBoolMask(selection)
	return nil
}

func (c *artifactMaskEditorController) clearPreview() {
	c.preview = nil
}

func (c *artifactMaskEditorController) applyPreview(erase bool) error {
	if len(c.preview) == 0 {
		return fmt.Errorf("no preview selection to apply")
	}
	selection := cloneBoolMask(c.preview)
	c.preview = nil
	return c.applySelection(selection, erase)
}

func (c *artifactMaskEditorController) applyBrush(points []mosaic.MaskPoint, radius float64, erase bool) error {
	selection, err := mosaic.RasterizeBrushStrokeMask(c.width, c.height, points, radius)
	if err != nil {
		return err
	}
	return c.applySelection(selection, erase)
}

func (c *artifactMaskEditorController) previewThreshold(minValue, maxValue float32, x0, y0, x1, y1, seedX, seedY int) error {
	threshold, err := mosaic.RasterizeThresholdMask(c.pixels, c.width, c.height, x0, y0, x1, y1, minValue, maxValue)
	if err != nil {
		return err
	}
	component, err := mosaic.SelectConnectedThresholdComponent(threshold, c.width, c.height, seedX, seedY)
	if err != nil {
		return err
	}
	return c.setPreview(component)
}

func (c *artifactMaskEditorController) applyMorphology(grow bool, radius int) error {
	var (
		next []bool
		err  error
	)
	if grow {
		next, err = mosaic.DilateArtifactMask(c.mask, c.width, c.height, radius)
	} else {
		next, err = mosaic.ErodeArtifactMask(c.mask, c.width, c.height, radius)
	}
	if err != nil {
		return err
	}
	c.pushUndo()
	c.mask = next
	c.preview = nil
	c.redo = nil
	return nil
}

func (c *artifactMaskEditorController) applyRectangle(x0, y0, x1, y1 float64, erase bool) error {
	selection, err := mosaic.RasterizeRectangleMask(c.width, c.height, x0, y0, x1, y1)
	if err != nil {
		return err
	}
	return c.applySelection(selection, erase)
}

func (c *artifactMaskEditorController) applyPolygon(points []mosaic.MaskPoint, erase bool) error {
	selection, err := mosaic.RasterizePolygonMask(c.width, c.height, points)
	if err != nil {
		return err
	}
	return c.applySelection(selection, erase)
}

func (c *artifactMaskEditorController) countMasked() int {
	return mosaic.CountMaskPixels(c.mask)
}

func (c *artifactMaskEditorController) countPreview() int {
	return mosaic.CountMaskPixels(c.preview)
}

func (c *artifactMaskEditorController) canUndo() bool { return len(c.undo) > 0 }
func (c *artifactMaskEditorController) canRedo() bool { return len(c.redo) > 0 }

func (c *artifactMaskEditorController) undoLast() bool {
	if len(c.undo) == 0 {
		return false
	}
	c.redo = append(c.redo, cloneBoolMask(c.mask))
	last := c.undo[len(c.undo)-1]
	c.undo = c.undo[:len(c.undo)-1]
	c.mask = last
	return true
}

func (c *artifactMaskEditorController) redoLast() bool {
	if len(c.redo) == 0 {
		return false
	}
	c.undo = append(c.undo, cloneBoolMask(c.mask))
	last := c.redo[len(c.redo)-1]
	c.redo = c.redo[:len(c.redo)-1]
	c.mask = last
	return true
}

func (c *artifactMaskEditorController) document() models.ArtifactMaskDocument {
	if c.sourceMode == models.ArtifactMaskSourceMosaic {
		return models.ArtifactMaskDocument{
			ID:         c.sourceKey,
			Name:       c.name,
			Purpose:    c.purpose,
			SourceMode: models.ArtifactMaskSourceMosaic,
			SourceKey:  c.sourceKey,
			Width:      c.width,
			Height:     c.height,
			Operations: []models.ArtifactMaskOperation{
				{
					Mode:   models.ArtifactMaskOperationAdd,
					Kind:   models.ArtifactMaskRegionRaster,
					Width:  c.width,
					Height: c.height,
					Mask:   boolMaskToBytes(c.mask),
				},
			},
		}
	}
	key := c.sourceKey
	return models.ArtifactMaskDocument{
		ID:         key,
		Name:       c.name,
		Purpose:    c.purpose,
		SourceMode: models.ArtifactMaskSourceInput,
		SourceKey:  key,
		Width:      c.width,
		Height:     c.height,
		Targets: []models.ArtifactMaskTarget{
			{Key: key, Path: artifactMaskSourcePath(c.input), SCIExt: c.input.SCIExt, Selected: true},
		},
		Operations: []models.ArtifactMaskOperation{
			{
				Mode:   models.ArtifactMaskOperationAdd,
				Kind:   models.ArtifactMaskRegionRaster,
				Width:  c.width,
				Height: c.height,
				Mask:   boolMaskToBytes(c.mask),
			},
		},
	}
}

func (c *artifactMaskEditorController) pushUndo() {
	c.undo = append(c.undo, cloneBoolMask(c.mask))
	const maxUndo = 50
	if len(c.undo) > maxUndo {
		copy(c.undo, c.undo[len(c.undo)-maxUndo:])
		c.undo = c.undo[:maxUndo]
	}
}

func cloneBoolMask(mask []bool) []bool {
	out := make([]bool, len(mask))
	copy(out, mask)
	return out
}

func boolMaskToBytes(mask []bool) []byte {
	out := make([]byte, len(mask))
	for i, v := range mask {
		if v {
			out[i] = 1
		}
	}
	return out
}

func artifactMaskSourcePath(input mosaic.Input) string {
	if input.SourcePath != "" {
		return input.SourcePath
	}
	return input.Path
}

func artifactMaskTargetKey(input mosaic.Input) string {
	path := filepath.Clean(artifactMaskSourcePath(input))
	if input.SCIExt > 0 {
		return fmt.Sprintf("%s[sci,%d]", path, input.SCIExt)
	}
	return path
}

func findArtifactMaskDocument(project *models.ArtifactMaskProject, input mosaic.Input) *models.ArtifactMaskDocument {
	return findArtifactMaskDocumentForPurpose(project, input, models.ArtifactMaskPurposeMIRIArtifact)
}

func findArtifactMaskDocumentForPurpose(project *models.ArtifactMaskProject, input mosaic.Input, purpose models.ArtifactMaskPurpose) *models.ArtifactMaskDocument {
	if project == nil {
		return nil
	}
	if purpose == "" {
		purpose = models.ArtifactMaskPurposeMIRIArtifact
	}
	key := artifactMaskTargetKey(input)
	for i := range project.Documents {
		docPurpose := project.Documents[i].Purpose
		if docPurpose == "" {
			docPurpose = models.ArtifactMaskPurposeMIRIArtifact
		}
		if docPurpose == purpose && project.Documents[i].SourceMode == models.ArtifactMaskSourceInput && project.Documents[i].SourceKey == key {
			return &project.Documents[i]
		}
	}
	return nil
}

func findMosaicArtifactMaskDocument(project *models.ArtifactMaskProject, width, height int) *models.ArtifactMaskDocument {
	if project == nil {
		return nil
	}
	key := artifactMaskMosaicSourceKey(width, height)
	for i := range project.Documents {
		docPurpose := project.Documents[i].Purpose
		if docPurpose == "" {
			docPurpose = models.ArtifactMaskPurposeMIRIArtifact
		}
		if docPurpose == models.ArtifactMaskPurposeMIRIArtifact && project.Documents[i].SourceMode == models.ArtifactMaskSourceMosaic && project.Documents[i].SourceKey == key {
			return &project.Documents[i]
		}
	}
	return nil
}

func upsertArtifactMaskDocument(project *models.ArtifactMaskProject, doc models.ArtifactMaskDocument) *models.ArtifactMaskProject {
	if project == nil {
		project = &models.ArtifactMaskProject{Version: 1}
	}
	if project.Version == 0 {
		project.Version = 1
	}
	for i := range project.Documents {
		existingPurpose := project.Documents[i].Purpose
		if existingPurpose == "" {
			existingPurpose = models.ArtifactMaskPurposeMIRIArtifact
		}
		docPurpose := doc.Purpose
		if docPurpose == "" {
			docPurpose = models.ArtifactMaskPurposeMIRIArtifact
		}
		if existingPurpose == docPurpose && project.Documents[i].SourceMode == doc.SourceMode && project.Documents[i].SourceKey == doc.SourceKey {
			project.Documents[i] = doc
			return project
		}
	}
	project.Documents = append(project.Documents, doc)
	return project
}

func markMosaicArtifactMaskDocumentsStale(project *models.ArtifactMaskProject) {
	if project == nil {
		return
	}
	for i := range project.Documents {
		if project.Documents[i].SourceMode == models.ArtifactMaskSourceMosaic {
			project.Documents[i].Stale = true
		}
	}
}

func artifactMaskMosaicSourceKey(width, height int) string {
	return fmt.Sprintf("mosaic:%dx%d", width, height)
}
