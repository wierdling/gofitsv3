package ui

import (
	"path/filepath"
	"testing"

	"gofitsv3/internal/export"
)

func TestClearComposeOrigPixels(t *testing.T) {
	orig := [3][]float32{
		{1},
		{2},
		{3},
	}

	clearComposeOrigPixels(&orig, -1, 0, 2, 3)

	if orig[0] != nil {
		t.Fatalf("orig[0] = %#v, want nil", orig[0])
	}
	if orig[1] == nil || len(orig[1]) != 1 || orig[1][0] != 2 {
		t.Fatalf("orig[1] = %#v, want preserved slice", orig[1])
	}
	if orig[2] != nil {
		t.Fatalf("orig[2] = %#v, want nil", orig[2])
	}
}

func TestStarlessDebugSettingsForRGBExportUsesRGBPath(t *testing.T) {
	path := filepath.Join("tmp", "nebula_rgb.tif")
	settings := starlessDebugSettingsForRGBExport(path, export.TIFF, export.Options{Quality: 91})
	wantDir := filepath.Join(filepath.Dir(path), "nebula_rgb")
	if settings.Dir != wantDir {
		t.Fatalf("Dir = %q, want %q", settings.Dir, wantDir)
	}
	if settings.Prefix != "starless" {
		t.Fatalf("Prefix = %q, want starless", settings.Prefix)
	}
	if settings.Format != export.TIFF {
		t.Fatalf("Format = %q, want %q", settings.Format, export.TIFF)
	}
	if settings.Options.Quality != 91 {
		t.Fatalf("Quality = %d, want 91", settings.Options.Quality)
	}
}

func TestDefaultStarlessComposeSettingsUseNebulaConservativeDetection(t *testing.T) {
	settings := defaultStarlessComposeSettings()
	if settings.DetectionMode != "min" {
		t.Fatalf("DetectionMode = %q, want min", settings.DetectionMode)
	}
	if settings.DetectionPreprocessMode != "none" {
		t.Fatalf("DetectionPreprocessMode = %q, want none", settings.DetectionPreprocessMode)
	}
	if settings.DetectionMergeMode != "per-channel-merged" {
		t.Fatalf("DetectionMergeMode = %q, want per-channel-merged", settings.DetectionMergeMode)
	}
	if settings.MinDetectedChannels != 2 || settings.MinSeedFootprintArea != 5 {
		t.Fatalf("min detection settings = channels %d footprint %d, want 2/5", settings.MinDetectedChannels, settings.MinSeedFootprintArea)
	}
}
