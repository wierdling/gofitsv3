package ui

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeMagicFilterAcceptsNamedDrizzleFiles(t *testing.T) {
	tests := []struct {
		name       string
		wantFilter string
		wantNumber int
	}{
		{"F435W_drz.fits", "F435W", 435},
		{"F555W_target_drz.fits", "F555W", 555},
		{"F555W_20260716_221824_drz.fits", "F555W", 555},
		{"F555W_20260716_221824_driz.fits", "F555W", 555},
		{"F658N_20260716_224237_driz.fits", "F658N", 658},
		{"F814W_20260716_224734_driz.fits", "F814W", 814},
		{"F555W_20260716_221824_drizzle.fits", "F555W", 555},
		{"f814w_target-v2_DRZ.FITS", "F814W", 814},
		{"F658N_target_part2_drz.fits", "F658N", 658},
		{"F150W2_target_drz.fits", "F150W2", 150},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter, number, ok := composeMagicFilter(tt.name)
			if !ok || filter != tt.wantFilter || number != tt.wantNumber {
				t.Fatalf("composeMagicFilter(%q) = %q, %d, %v; want %q, %d, true", tt.name, filter, number, ok, tt.wantFilter, tt.wantNumber)
			}
		})
	}
}

func TestComposeMagicFilterRejectsOtherNames(t *testing.T) {
	for _, name := range []string{
		"target_F555W_drz.fits",
		"F555W_target_cal.fits",
		"F555W__drz.fits",
		"F555W____drz.fits",
		"F555W_bad name_drz.fits",
		"F555_target_drz.fits",
		"F555W_target_drz.txt",
		"F555W_target_drizz.fits",
		"F555W_target_drz.fit",
		"F555W_target_drz.fts",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := composeMagicFilter(name); ok {
				t.Fatalf("composeMagicFilter(%q) accepted an unsupported filename", name)
			}
		})
	}
}

func TestDiscoverComposeMagicFilesIsNonrecursiveAndSortsNumerically(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"F435W_drz.fits",
		"F555W_20260716_221824_driz.fits",
		"F658N_20260716_224237_driz.fits",
		"F814W_20260716_224734_driz.fits",
		"F814W_target_drz.fits",
		"F555W_z_drz.fits",
		"F658N_target_drz.fits",
		"F555W_A_drz.fits",
		"not_a_filter.fits",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "F200W_nested_drz.fits"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	files, err := discoverComposeMagicFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"F435W_drz.fits",
		"F555W_20260716_221824_driz.fits",
		"F555W_A_drz.fits",
		"F555W_z_drz.fits",
		"F658N_20260716_224237_driz.fits",
		"F658N_target_drz.fits",
		"F814W_20260716_224734_driz.fits",
		"F814W_target_drz.fits",
	}
	if len(files) != len(want) {
		t.Fatalf("discovered %d files, want %d: %#v", len(files), len(want), files)
	}
	for i, name := range want {
		if files[i].Name != name {
			t.Errorf("file[%d].Name = %q, want %q", i, files[i].Name, name)
		}
		if !filepath.IsAbs(files[i].Path) {
			t.Errorf("file[%d].Path = %q, want absolute path", i, files[i].Path)
		}
	}
}

func TestDiscoverComposeMagicFilesRejectsEmptyDirectoryPath(t *testing.T) {
	if _, err := discoverComposeMagicFiles("  "); err == nil {
		t.Fatal("discoverComposeMagicFiles accepted an empty directory path")
	}
}

func TestDefaultComposeMagicRowsAssignsPrimaryAndCustomChannels(t *testing.T) {
	files := []composeMagicFile{
		{Path: "f600.fits", FilterNumber: 600},
		{Path: "f300.fits", FilterNumber: 300},
		{Path: "f400.fits", FilterNumber: 400},
		{Path: "f500.fits", FilterNumber: 500},
		{Path: "f200.fits", FilterNumber: 200},
	}
	rows := defaultComposeMagicRows(files)
	wantAssignments := []composeMagicAssignment{composeMagicRed, composeMagicCustom, composeMagicGreen, composeMagicCustom, composeMagicBlue}
	for i, want := range wantAssignments {
		if rows[i].Assignment != want {
			t.Errorf("row[%d].Assignment = %q, want %q", i, rows[i].Assignment, want)
		}
		if rows[i].Color.A != 255 {
			t.Errorf("row[%d].Color.A = %d, want 255", i, rows[i].Color.A)
		}
	}
	firstCustom := overlayLayerPalette[0]
	secondCustom := overlayLayerPalette[1]
	if rows[1].Color != (color.NRGBA{R: firstCustom[0], G: firstCustom[1], B: firstCustom[2], A: 255}) {
		t.Errorf("first custom color = %#v, want first overlay palette color", rows[1].Color)
	}
	if rows[3].Color != (color.NRGBA{R: secondCustom[0], G: secondCustom[1], B: secondCustom[2], A: 255}) {
		t.Errorf("second custom color = %#v, want second overlay palette color", rows[3].Color)
	}
}

func TestValidateComposeMagicPlanAcceptsOneOfEachPrimary(t *testing.T) {
	rows := validComposeMagicRows()
	if err := validateComposeMagicPlan(rows); err != nil {
		t.Fatalf("validateComposeMagicPlan returned %v", err)
	}
}

func TestValidateComposeMagicPlanRejectsInvalidRows(t *testing.T) {
	tests := []struct {
		name string
		edit func([]composeMagicRow) []composeMagicRow
		want string
	}{
		{"empty path", func(rows []composeMagicRow) []composeMagicRow { rows[0].File.Path = " "; return rows }, "empty file path"},
		{"duplicate path", func(rows []composeMagicRow) []composeMagicRow {
			rows[1].File.Path = strings.ToUpper(rows[0].File.Path)
			return rows
		}, "duplicates file"},
		{"missing primary", func(rows []composeMagicRow) []composeMagicRow { rows[0].Assignment = composeMagicCustom; return rows }, "exactly one Blue channel is required (found 0)"},
		{"duplicate primary", func(rows []composeMagicRow) []composeMagicRow { rows[1].Assignment = composeMagicBlue; return rows }, "exactly one Blue channel is required (found 2)"},
		{"unknown assignment", func(rows []composeMagicRow) []composeMagicRow { rows[0].Assignment = "Purple"; return rows }, "unknown channel assignment"},
		{"transparent color", func(rows []composeMagicRow) []composeMagicRow { rows[0].Color.A = 0; return rows }, "must be opaque"},
		{"too many custom channels", addTooManyComposeMagicCustomRows, "maximum of"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := tt.edit(validComposeMagicRows())
			err := validateComposeMagicPlan(rows)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateComposeMagicPlan error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateComposeMagicCapacityUsesRemainingLayerSlots(t *testing.T) {
	rows := validComposeMagicRows()
	rows = append(rows,
		composeMagicRow{File: composeMagicFile{Path: "custom-1.fits"}, Assignment: composeMagicCustom, Color: color.NRGBA{A: 255}},
		composeMagicRow{File: composeMagicFile{Path: "custom-2.fits"}, Assignment: composeMagicCustom, Color: color.NRGBA{A: 255}},
	)
	if err := validateComposeMagicCapacity(rows, maxOverlayLayers-2); err != nil {
		t.Fatalf("two remaining slots rejected: %v", err)
	}
	err := validateComposeMagicCapacity(rows, maxOverlayLayers-1)
	if err == nil || !strings.Contains(err.Error(), "needs 2 custom channels, but only 1") {
		t.Fatalf("capacity error = %v, want specific required and remaining counts", err)
	}
}

func validComposeMagicRows() []composeMagicRow {
	return []composeMagicRow{
		{File: composeMagicFile{Path: "blue.fits"}, Assignment: composeMagicBlue, Color: color.NRGBA{A: 255}},
		{File: composeMagicFile{Path: "green.fits"}, Assignment: composeMagicGreen, Color: color.NRGBA{A: 255}},
		{File: composeMagicFile{Path: "red.fits"}, Assignment: composeMagicRed, Color: color.NRGBA{A: 255}},
	}
}

func addTooManyComposeMagicCustomRows(rows []composeMagicRow) []composeMagicRow {
	for i := 0; i <= maxOverlayLayers; i++ {
		rows = append(rows, composeMagicRow{
			File:       composeMagicFile{Path: filepath.Join("custom", string(rune('a'+i)))},
			Assignment: composeMagicCustom,
			Color:      color.NRGBA{A: 255},
		})
	}
	return rows
}
