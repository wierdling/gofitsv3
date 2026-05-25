package processing

import (
	"os"
	"path/filepath"
	"testing"

	"gofitsv3/internal/export"
)

func TestExportStarlessDebugWritesExpectedFiles(t *testing.T) {
	const w, h = 9, 9
	channels := make([][]float32, 3)
	base := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			base[y*w+x] = float32(5 + x + y)
		}
	}
	for i := range channels {
		channels[i] = append([]float32(nil), base...)
		addSyntheticStar(channels[i], w, h, 4, 4, float32(30+10*i))
	}
	settings := DefaultStarMaskSettings()
	settings.MaskGrowRadius = 2
	settings.MaskSoftEdgeRadius = 2
	settings.InpaintRadius = 2
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}

	dir := t.TempDir()
	err = ExportStarlessDebug(result, StarDebugExportSettings{
		Dir:    dir,
		Prefix: "debug",
		Format: export.PNG,
	})
	if err != nil {
		t.Fatalf("ExportStarlessDebug error = %v", err)
	}

	wantFiles := []string{
		"debug_detection.png",
		"debug_candidate_mask.png",
		"debug_accepted_seeds.png",
		"debug_rejected_seeds.png",
		"debug_hard_mask.png",
		"debug_alpha_mask.png",
		"debug_starless_ch1.png",
		"debug_starless_ch2.png",
		"debug_starless_ch3.png",
		"debug_stars_ch1.png",
		"debug_stars_ch2.png",
		"debug_stars_ch3.png",
	}
	for _, name := range wantFiles {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("expected debug file %q: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("debug file %q is empty", name)
		}
	}
}

func TestStarDebugExportSettingsValidateRejectsInvalidValues(t *testing.T) {
	if err := (StarDebugExportSettings{}).Validate(); err == nil {
		t.Fatal("expected empty directory validation error")
	}
	if err := (StarDebugExportSettings{Dir: t.TempDir(), Format: export.Format("gif")}).Validate(); err == nil {
		t.Fatal("expected unsupported format validation error")
	}
}
