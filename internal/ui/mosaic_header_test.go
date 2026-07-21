package ui

import "testing"

func TestMosaicStatsTextIncludesSavedFilename(t *testing.T) {
	got := mosaicStatsText("ngc_1234_drizzle.fits", 1.25, 0.5, 1200, 900)
	want := "File: ngc_1234_drizzle.fits | Mean: 1.2500 | Std: 0.5000 | Size: 1200x900"
	if got != want {
		t.Fatalf("mosaicStatsText() = %q, want %q", got, want)
	}
}

func TestMosaicStatsTextOmitsFilenameBeforeSave(t *testing.T) {
	got := mosaicStatsText("", 1.25, 0.5, 1200, 900)
	want := "Mean: 1.2500 | Std: 0.5000 | Size: 1200x900"
	if got != want {
		t.Fatalf("mosaicStatsText() = %q, want %q", got, want)
	}
}
