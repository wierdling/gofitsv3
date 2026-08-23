package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gofitsv3/internal/models"
)

// writeProjectJSON atomically stages a project beside its destination. The
// caller can opt out of replacing an existing destination (generated queue
// projects use this to avoid accidental overwrite).
func writeProjectJSON(path string, data []byte, replace bool) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mosaic-project-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if !replace {
		// A hard-link publication is atomic and fails with ErrExist without
		// clobbering a concurrently-created destination.
		if err := os.Link(tmpPath, path); err != nil {
			_ = os.Remove(tmpPath)
			if os.IsExist(err) {
				return os.ErrExist
			}
			return err
		}
		_ = os.Remove(tmpPath)
		return nil
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace project: %w", err)
	}
	return nil
}

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
