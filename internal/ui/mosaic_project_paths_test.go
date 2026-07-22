package ui

import (
	"path/filepath"
	"testing"

	"gofitsv3/internal/models"
)

func TestProjectRelativePathRoundTripKeepsMaskDirPortable(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "project", "mosaic_project.json")
	maskDir := filepath.Join(filepath.Dir(projectPath), "masks", "miri")

	encoded := encodeProjectRelativePath(projectPath, maskDir)
	if encoded != filepath.Join("masks", "miri") {
		t.Fatalf("encoded path = %q, want project-relative masks/miri", encoded)
	}

	resolved := resolveProjectRelativePath(projectPath, encoded)
	if resolved != filepath.Clean(maskDir) {
		t.Fatalf("resolved path = %q, want %q", resolved, filepath.Clean(maskDir))
	}
}

func TestProjectRelativePathLeavesExternalAbsoluteDirAbsolute(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, "project", "mosaic_project.json")
	externalDir := filepath.Join(root, "external_masks")

	encoded := encodeProjectRelativePath(projectPath, externalDir)
	if encoded != filepath.Clean(externalDir) {
		t.Fatalf("encoded path = %q, want unchanged absolute %q", encoded, filepath.Clean(externalDir))
	}
}

func TestResolveSkysubSettingsForProjectResolvesMaskDirsOnly(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "mosaic_project.json")
	settings := models.SkysubSettings{
		RowDestripeMaskDir:   "row_masks",
		RowDestripeMaskPath:  "direct_row_mask.fits",
		MIRIArtifactMaskDir:  "masks",
		MIRIArtifactMaskPath: "direct_mask.fits",
	}

	resolved := resolveSkysubSettingsForProject(settings, projectPath)
	wantRowDir := filepath.Join(filepath.Dir(projectPath), "row_masks")
	if resolved.RowDestripeMaskDir != wantRowDir {
		t.Fatalf("RowDestripeMaskDir = %q, want %q", resolved.RowDestripeMaskDir, wantRowDir)
	}
	if resolved.RowDestripeMaskPath != settings.RowDestripeMaskPath {
		t.Fatalf("RowDestripeMaskPath = %q, want unchanged %q", resolved.RowDestripeMaskPath, settings.RowDestripeMaskPath)
	}
	wantDir := filepath.Join(filepath.Dir(projectPath), "masks")
	if resolved.MIRIArtifactMaskDir != wantDir {
		t.Fatalf("MIRIArtifactMaskDir = %q, want %q", resolved.MIRIArtifactMaskDir, wantDir)
	}
	if resolved.MIRIArtifactMaskPath != settings.MIRIArtifactMaskPath {
		t.Fatalf("MIRIArtifactMaskPath = %q, want unchanged %q", resolved.MIRIArtifactMaskPath, settings.MIRIArtifactMaskPath)
	}
}
