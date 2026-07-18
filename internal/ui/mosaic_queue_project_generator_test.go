package ui

import (
	"os"
	"path/filepath"
	"testing"

	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
)

func TestGeneratedProjectGroupsSortsAndFiltersProductType(t *testing.T) {
	groups := generatedProjectGroups(map[string][]mosaic.FilterFile{
		"F606W": {
			{Path: filepath.Join(t.TempDir(), "b_flt.fits"), Filter: "F606W"},
			{Path: filepath.Join(t.TempDir(), "a_flc.fits"), Filter: "F606W"},
		},
		"F814W": {{Path: filepath.Join(t.TempDir(), "c_flc.fits"), Filter: "F814W"}},
	}, "flc")
	if len(groups) != 2 || groups[0].Filter != "F606W" || groups[1].Filter != "F814W" {
		t.Fatalf("groups = %+v", groups)
	}
	if len(groups[0].Files) != 1 || filepath.Base(groups[0].Files[0]) != "a_flc.fits" {
		t.Fatalf("F606W files = %+v", groups[0].Files)
	}
}

func TestBuildGeneratedMosaicProjectCopiesSettingsAndReplacesInputs(t *testing.T) {
	templatePath := filepath.Join(t.TempDir(), "template", "base.json")
	outputPath := filepath.Join(t.TempDir(), "output", "F606W_project.json")
	template := models.MosaicProject{
		DrizzleSettings:   models.DrizzleSettings{Scale: 2.5},
		SkysubSettings:    models.SkysubSettings{Enabled: true, RowDestripeMaskDir: "masks"},
		AlignmentSettings: models.AlignmentSettings{AlignmentMode: 3},
		ReferencePath:     "reference.fits",
		ReferenceSCIExt:   2,
		ExposureNormMode:  1,
		ArtifactMasks:     &models.ArtifactMaskProject{Version: 1},
		Inputs:            []models.MosaicInputState{{Path: "old.fits", OffsetX: 4, Excluded: true}},
	}
	project := buildGeneratedMosaicProject(template, templatePath, outputPath, "F606W", []string{"a.fits", "b.fits"})
	if project.ActiveFilter != "F606W" || len(project.Inputs) != 2 || project.DrizzleSettings.Scale != 2.5 || !project.SkysubSettings.Enabled {
		t.Fatalf("generated project = %+v", project)
	}
	if project.Inputs[0].Path != "a.fits" || project.Inputs[0].OffsetX != 0 || project.Inputs[0].Excluded {
		t.Fatalf("generated input state = %+v", project.Inputs[0])
	}
	if project.ArtifactMasks != nil || project.ReferencePath != filepath.Join(filepath.Dir(templatePath), "reference.fits") {
		t.Fatalf("generated reference/masks = %q / %+v", project.ReferencePath, project.ArtifactMasks)
	}
	if project.DrizzleSettingsSet != template.DrizzleSettingsSet || project.SkysubSettingsSet != template.SkysubSettingsSet {
		t.Fatalf("settings-set flags not preserved: drizzle=%v skysub=%v", project.DrizzleSettingsSet, project.SkysubSettingsSet)
	}
	if project.SkysubSettings.RowDestripeMaskDir != filepath.Join(filepath.Dir(templatePath), "masks") {
		t.Fatalf("generated mask dir = %q", project.SkysubSettings.RowDestripeMaskDir)
	}
}

func TestWriteGeneratedMosaicProjectDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project.json")
	if err := writeGeneratedMosaicProject(path, models.MosaicProject{}); err != nil {
		t.Fatal(err)
	}
	if err := writeGeneratedMosaicProject(path, models.MosaicProject{}); err == nil {
		t.Fatal("expected existing project path to be rejected")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("project disappeared after rejected write: %v", err)
	}
}

func TestReadMosaicProjectAllowsEmptyInputTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "template.json")
	if err := os.WriteFile(path, []byte(`{"drizzleSettings":{"scale":2.0},"referencePath":"reference.fits"}`), 0644); err != nil {
		t.Fatal(err)
	}
	project, gotPath, err := readMosaicProject(path)
	if err != nil {
		t.Fatalf("readMosaicProject returned error: %v", err)
	}
	if gotPath != path || len(project.Inputs) != 0 || project.ReferencePath != "reference.fits" {
		t.Fatalf("template = %+v, path = %q", project, gotPath)
	}
}

func TestLoadMosaicProjectDataRejectsEmptyInputProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "template.json")
	if err := os.WriteFile(path, []byte(`{"referencePath":"reference.fits"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMosaicProjectData(nil, path, nil); err == nil {
		t.Fatal("expected empty-input project to be rejected for execution")
	}
}
