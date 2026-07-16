package ui

import (
	"path/filepath"
	"strings"

	"gofitsv3/internal/models"
)

func encodeProjectRelativePath(projectPath, path string) string {
	path = strings.TrimSpace(path)
	if path == "" || projectPath == "" {
		return path
	}
	if !filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	projectDir := filepath.Dir(projectPath)
	rel, err := filepath.Rel(projectDir, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return filepath.Clean(path)
	}
	return filepath.Clean(rel)
}

func resolveProjectRelativePath(projectPath, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if projectPath == "" || filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(projectPath), path))
}

func resolveSkysubSettingsForProject(s models.SkysubSettings, projectPath string) models.SkysubSettings {
	s.RowDestripeMaskDir = resolveProjectRelativePath(projectPath, s.RowDestripeMaskDir)
	s.MIRIArtifactMaskDir = resolveProjectRelativePath(projectPath, s.MIRIArtifactMaskDir)
	return s
}
