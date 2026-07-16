package ui

import (
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
)

func TestArtifactMaskEditorControllerEditsUndoRedoAndDocument(t *testing.T) {
	input := artifactMaskTestInput("miri_cal.fits", 5, 5)
	ctrl, err := newArtifactMaskEditorController(0, input, nil)
	if err != nil {
		t.Fatalf("newArtifactMaskEditorController error = %v", err)
	}

	if err := ctrl.applyRectangle(1, 1, 2, 2, false); err != nil {
		t.Fatalf("applyRectangle error = %v", err)
	}
	if got := ctrl.countMasked(); got != 4 {
		t.Fatalf("countMasked after rectangle = %d, want 4", got)
	}
	if err := ctrl.applyBrush([]mosaic.MaskPoint{{X: 1, Y: 1}}, 0.5, true); err != nil {
		t.Fatalf("applyBrush erase error = %v", err)
	}
	if got := ctrl.countMasked(); got != 3 {
		t.Fatalf("countMasked after erase = %d, want 3", got)
	}
	if !ctrl.undoLast() || ctrl.countMasked() != 4 {
		t.Fatalf("undo failed, count = %d", ctrl.countMasked())
	}
	if !ctrl.redoLast() || ctrl.countMasked() != 3 {
		t.Fatalf("redo failed, count = %d", ctrl.countMasked())
	}

	doc := ctrl.document()
	if doc.SourceMode != models.ArtifactMaskSourceInput || doc.SourceKey == "" || doc.Width != 5 || doc.Height != 5 {
		t.Fatalf("document metadata = %+v, want single-frame source document", doc)
	}
	if doc.Purpose != models.ArtifactMaskPurposeMIRIArtifact {
		t.Fatalf("document purpose = %q, want MIRI artifact", doc.Purpose)
	}
	if len(doc.Targets) != 1 || !doc.Targets[0].Selected {
		t.Fatalf("document targets = %+v, want selected target", doc.Targets)
	}
	if len(doc.Operations) != 1 || len(doc.Operations[0].Mask) != 25 {
		t.Fatalf("document operations = %+v, want one raster operation", doc.Operations)
	}
}

func TestArtifactMaskEditorControllerLoadsExistingRasterDocument(t *testing.T) {
	input := artifactMaskTestInput("miri_cal.fits", 3, 2)
	doc := models.ArtifactMaskDocument{
		SourceMode: models.ArtifactMaskSourceInput,
		SourceKey:  artifactMaskTargetKey(input),
		Width:      3,
		Height:     2,
		Operations: []models.ArtifactMaskOperation{
			{Kind: models.ArtifactMaskRegionRaster, Width: 3, Height: 2, Mask: []byte{1, 0, 1, 0, 0, 1}},
		},
	}

	ctrl, err := newArtifactMaskEditorController(0, input, &doc)
	if err != nil {
		t.Fatalf("newArtifactMaskEditorController error = %v", err)
	}
	if got := ctrl.countMasked(); got != 3 {
		t.Fatalf("countMasked = %d, want loaded raster mask count 3", got)
	}
}

func TestArtifactMaskEditorControllerThresholdPreviewAndMorphology(t *testing.T) {
	input := artifactMaskTestInput("miri_cal.fits", 4, 3)
	input.HDU.Data.Pixels = []float32{
		1, 10, 11, 1,
		1, 12, 1, 20,
		1, 1, 1, 21,
	}
	ctrl, err := newArtifactMaskEditorController(0, input, nil)
	if err != nil {
		t.Fatalf("newArtifactMaskEditorController error = %v", err)
	}

	if err := ctrl.previewThreshold(10, 21, 0, 0, 3, 2, 1, 1); err != nil {
		t.Fatalf("previewThreshold error = %v", err)
	}
	if got := ctrl.countPreview(); got != 3 {
		t.Fatalf("countPreview = %d, want seeded 3-pixel component", got)
	}
	if err := ctrl.applyPreview(false); err != nil {
		t.Fatalf("applyPreview error = %v", err)
	}
	if got := ctrl.countMasked(); got != 3 {
		t.Fatalf("countMasked after preview accept = %d, want 3", got)
	}
	if err := ctrl.applyMorphology(true, 1); err != nil {
		t.Fatalf("applyMorphology grow error = %v", err)
	}
	if ctrl.countMasked() <= 3 {
		t.Fatalf("countMasked after grow = %d, want larger than 3", ctrl.countMasked())
	}
	if !ctrl.undoLast() || ctrl.countMasked() != 3 {
		t.Fatalf("undo morphology failed, count = %d", ctrl.countMasked())
	}
}

func TestArtifactMaskEditorBuildExportTargetsDefaultsPropagationOff(t *testing.T) {
	source := artifactMaskTestInput("source_cal.fits", 3, 3)
	source.PrimaryHeader.Cards["INSTRUME"] = "'MIRI'"
	other := artifactMaskTestInput("other_cal.fits", 3, 3)
	other.PrimaryHeader.Cards["INSTRUME"] = "'MIRI'"
	ctrl, err := newArtifactMaskEditorController(0, source, nil)
	if err != nil {
		t.Fatalf("newArtifactMaskEditorController error = %v", err)
	}
	if err := ctrl.applyRectangle(1, 1, 1, 1, false); err != nil {
		t.Fatalf("applyRectangle error = %v", err)
	}
	editor := &artifactMaskEditorWindow{
		ws:   &mosaicWorkspace{state: &mosaicState{inputs: []mosaic.Input{source, other}}},
		ctrl: ctrl,
	}

	targets := editor.buildExportTargets(false)
	if len(targets) != 1 || !targets[0].Selected {
		t.Fatalf("targets without propagation = %+v, want only selected source", targets)
	}
	targets = editor.buildExportTargets(true)
	if len(targets) != 2 {
		t.Fatalf("targets with propagation len = %d, want source plus other", len(targets))
	}
	if !targets[0].Selected || targets[1].Selected {
		t.Fatalf("target defaults = %+v, want source selected and propagated target unchecked", targets)
	}
}

func TestArtifactMaskEditorBuildRowDestripeTargetsArePerInput(t *testing.T) {
	source := artifactMaskTestInput("source_cal.fits", 3, 3)
	source.PrimaryHeader.Cards["INSTRUME"] = "'NIRCAM'"
	other := artifactMaskTestInput("other_cal.fits", 3, 3)
	other.PrimaryHeader.Cards["INSTRUME"] = "'NIRCAM'"
	ctrl, err := newArtifactMaskEditorControllerForPurpose(0, source, models.ArtifactMaskPurposeRowDestripe, nil)
	if err != nil {
		t.Fatalf("newArtifactMaskEditorControllerForPurpose error = %v", err)
	}
	if err := ctrl.applyRectangle(1, 1, 1, 1, false); err != nil {
		t.Fatalf("applyRectangle error = %v", err)
	}
	editor := &artifactMaskEditorWindow{
		ws:   &mosaicWorkspace{state: &mosaicState{inputs: []mosaic.Input{source, other}}},
		ctrl: ctrl,
	}

	targets := editor.buildExportTargets(true)
	if len(targets) != 1 || !targets[0].Selected {
		t.Fatalf("row targets = %+v, want only selected source", targets)
	}
	doc := ctrl.document()
	if doc.Purpose != models.ArtifactMaskPurposeRowDestripe {
		t.Fatalf("document purpose = %q, want row destripe", doc.Purpose)
	}
}

func TestArtifactMaskEditorBuildMosaicTargetsDefaultsOverlapsOn(t *testing.T) {
	first := artifactMaskTestInput("first_cal.fits", 3, 3)
	second := artifactMaskTestInput("second_cal.fits", 3, 3)
	referenceOnly := artifactMaskTestInput("reference_cal.fits", 3, 3)
	referenceOnly.ReferenceOnly = true
	excluded := artifactMaskTestInput("excluded_cal.fits", 3, 3)
	excluded.Excluded = true
	nircam := artifactMaskTestInput("nircam_cal.fits", 3, 3)
	nircam.PrimaryHeader.Cards["INSTRUME"] = "'NIRCAM'"

	ctrl, err := newMosaicArtifactMaskEditorController(3, 3, make([]float32, 9), nil)
	if err != nil {
		t.Fatalf("newMosaicArtifactMaskEditorController error = %v", err)
	}
	if err := ctrl.applyRectangle(1, 1, 1, 1, false); err != nil {
		t.Fatalf("applyRectangle error = %v", err)
	}
	editor := &artifactMaskEditorWindow{
		ws: &mosaicWorkspace{state: &mosaicState{
			inputs: []mosaic.Input{first, second, referenceOnly, excluded, nircam},
			result: &mosaic.Result{Width: 3, Height: 3, Scale: 1, Pixels: make([]float32, 9)},
		}},
		ctrl: ctrl,
	}

	targets := editor.buildExportTargets(false)
	if len(targets) != 2 {
		t.Fatalf("mosaic targets len = %d, want only overlapping science MIRI inputs", len(targets))
	}
	for _, target := range targets {
		if !target.Selected {
			t.Fatalf("target %s selected = false, want overlapping mosaic targets on by default", target.Label)
		}
		if got := mosaic.CountMaskPixels(target.Mask); got == 0 {
			t.Fatalf("target %s has empty projected mask", target.Label)
		}
	}
}

func TestMosaicArtifactMaskDocumentStaleHandling(t *testing.T) {
	ctrl, err := newMosaicArtifactMaskEditorController(2, 2, make([]float32, 4), nil)
	if err != nil {
		t.Fatalf("newMosaicArtifactMaskEditorController error = %v", err)
	}
	if err := ctrl.applyRectangle(0, 0, 1, 1, false); err != nil {
		t.Fatalf("applyRectangle error = %v", err)
	}
	doc := ctrl.document()
	project := upsertArtifactMaskDocument(nil, doc)

	loaded, err := newMosaicArtifactMaskEditorController(2, 2, make([]float32, 4), findMosaicArtifactMaskDocument(project, 2, 2))
	if err != nil {
		t.Fatalf("newMosaicArtifactMaskEditorController load error = %v", err)
	}
	if loaded.countMasked() != 4 {
		t.Fatalf("loaded mosaic mask count = %d, want 4", loaded.countMasked())
	}

	markMosaicArtifactMaskDocumentsStale(project)
	stale, err := newMosaicArtifactMaskEditorController(2, 2, make([]float32, 4), findMosaicArtifactMaskDocument(project, 2, 2))
	if err != nil {
		t.Fatalf("newMosaicArtifactMaskEditorController stale load error = %v", err)
	}
	if stale.countMasked() != 0 {
		t.Fatalf("stale mosaic mask count = %d, want fresh empty mask", stale.countMasked())
	}
}

func TestArtifactMaskTargetsFromExportTargetsPersistsReviewSelection(t *testing.T) {
	first := artifactMaskTestInput("first_cal.fits", 2, 2)
	second := artifactMaskTestInput("second_cal.fits", 2, 2)
	targets := artifactMaskTargetsFromExportTargets([]artifactMaskExportTarget{
		{Input: first, Selected: true},
		{Input: second, Selected: false},
	})

	if len(targets) != 2 || !targets[0].Selected || targets[1].Selected {
		t.Fatalf("persisted target selections = %+v, want true then false", targets)
	}
	if targets[0].Key == "" || targets[1].Path == "" {
		t.Fatalf("persisted target identities = %+v, want source keys and paths", targets)
	}
}

func TestUpsertArtifactMaskDocumentReplacesSameSource(t *testing.T) {
	input := artifactMaskTestInput("miri_cal.fits", 2, 2)
	doc := models.ArtifactMaskDocument{SourceMode: models.ArtifactMaskSourceInput, SourceKey: artifactMaskTargetKey(input), Width: 2, Height: 2}
	project := upsertArtifactMaskDocument(nil, doc)
	if project == nil || project.Version != 1 || len(project.Documents) != 1 {
		t.Fatalf("project after insert = %+v, want versioned single document", project)
	}

	replacement := doc
	replacement.Height = 3
	project = upsertArtifactMaskDocument(project, replacement)
	if len(project.Documents) != 1 || project.Documents[0].Height != 3 {
		t.Fatalf("project after replacement = %+v, want replaced document", project.Documents)
	}
}

func TestUpsertArtifactMaskDocumentKeepsDifferentPurposes(t *testing.T) {
	input := artifactMaskTestInput("nircam_cal.fits", 2, 2)
	key := artifactMaskTargetKey(input)
	miriDoc := models.ArtifactMaskDocument{Purpose: models.ArtifactMaskPurposeMIRIArtifact, SourceMode: models.ArtifactMaskSourceInput, SourceKey: key, Width: 2, Height: 2}
	rowDoc := models.ArtifactMaskDocument{Purpose: models.ArtifactMaskPurposeRowDestripe, SourceMode: models.ArtifactMaskSourceInput, SourceKey: key, Width: 2, Height: 2}

	project := upsertArtifactMaskDocument(nil, miriDoc)
	project = upsertArtifactMaskDocument(project, rowDoc)
	if len(project.Documents) != 2 {
		t.Fatalf("project documents = %+v, want one document per purpose", project.Documents)
	}
	if findArtifactMaskDocumentForPurpose(project, input, models.ArtifactMaskPurposeMIRIArtifact) == nil {
		t.Fatal("missing MIRI-purpose document")
	}
	if findArtifactMaskDocumentForPurpose(project, input, models.ArtifactMaskPurposeRowDestripe) == nil {
		t.Fatal("missing row-destripe-purpose document")
	}
}

func TestArtifactMaskTargetKeyUsesSourcePathAndSCIExt(t *testing.T) {
	input := artifactMaskTestInput("working.fits", 2, 2)
	input.SourcePath = "downloads/jw12345_cal.fits"
	input.SCIExt = 2

	key := artifactMaskTargetKey(input)
	if key != "downloads\\jw12345_cal.fits[sci,2]" && key != "downloads/jw12345_cal.fits[sci,2]" {
		t.Fatalf("artifactMaskTargetKey = %q, want source path plus sci ext", key)
	}
}

func artifactMaskTestInput(path string, width, height int) mosaic.Input {
	return mosaic.Input{
		Path: path,
		HDU: fitsio.HDU{
			Header: fitsio.Header{Cards: map[string]string{
				"CRPIX1": "1", "CRPIX2": "1", "CRVAL1": "0", "CRVAL2": "0",
				"CD1_1": "1", "CD1_2": "0", "CD2_1": "0", "CD2_2": "1",
			}},
			Data: fitsio.ImageData{Width: width, Height: height, Pixels: make([]float32, width*height)},
		},
		PrimaryHeader: fitsio.Header{Cards: map[string]string{"INSTRUME": "'MIRI'"}},
	}
}
