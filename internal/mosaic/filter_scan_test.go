package mosaic

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIsPipelineProductFLC(t *testing.T) {
	if !IsPipelineProductFLC("abc20_flc.fits") {
		t.Fatalf("expected pipeline-style file to be excluded")
	}
	if IsPipelineProductFLC("ick909c1q_flc.fits") {
		t.Fatalf("expected raw-style file to be kept")
	}
}

func TestDiscoverFiltersGroupsRawFLCFiles(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFITS(t, filepath.Join(dir, "rawa_flc.fits"), "F502N")
	writeMinimalFITS(t, filepath.Join(dir, "rawb_flc.fits"), "F502N")
	writeMinimalFITS(t, filepath.Join(dir, "rawc_flc.fits"), "F657N")
	writeMinimalFITS(t, filepath.Join(dir, "prod20_flc.fits"), "F502N")

	groups, err := DiscoverFilters(dir)
	if err != nil {
		t.Fatalf("DiscoverFilters returned error: %v", err)
	}

	got502 := []string{filepath.Base(groups["F502N"][0]), filepath.Base(groups["F502N"][1])}
	want502 := []string{"rawa_flc.fits", "rawb_flc.fits"}
	if !reflect.DeepEqual(got502, want502) {
		t.Fatalf("F502N files = %v, want %v", got502, want502)
	}
	got657 := []string{filepath.Base(groups["F657N"][0])}
	want657 := []string{"rawc_flc.fits"}
	if !reflect.DeepEqual(got657, want657) {
		t.Fatalf("F657N files = %v, want %v", got657, want657)
	}
}

func writeMinimalFITS(t *testing.T, path string, filter string) {
	t.Helper()
	content := makeHeaderCard("SIMPLE", "=                    T") +
		makeHeaderCard("BITPIX", "=                    8") +
		makeHeaderCard("NAXIS", "=                    0") +
		makeHeaderCard("FILTER", "= '"+padFilter(filter)+"'") +
		makeHeaderCard("END", "")
	for len(content)%2880 != 0 {
		content += strings.Repeat(" ", 2880-(len(content)%2880))
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeMinimalFITS: %v", err)
	}
}

func makeHeaderCard(key, suffix string) string {
	card := key
	if len(card) < 8 {
		card += strings.Repeat(" ", 8-len(card))
	}
	card += suffix
	if len(card) < 80 {
		card += strings.Repeat(" ", 80-len(card))
	}
	return card[:80]
}

func padFilter(filter string) string {
	if len(filter) < 8 {
		return filter + strings.Repeat(" ", 8-len(filter))
	}
	return filter[:8]
}
