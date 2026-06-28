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

func TestDiscoverFiltersGroupsWFPC2FLTFiles(t *testing.T) {
	dir := t.TempDir()
	writeMinimalWFPC2FITS(t, filepath.Join(dir, "u6l60101m_flt.fits"), "F555W")
	writeMinimalWFPC2FITS(t, filepath.Join(dir, "u6l60102m_flt.fits"), "F555W")

	groups, err := DiscoverFilters(dir)
	if err != nil {
		t.Fatalf("DiscoverFilters returned error: %v", err)
	}

	got := []string{filepath.Base(groups["F555W"][0]), filepath.Base(groups["F555W"][1])}
	want := []string{"u6l60101m_flt.fits", "u6l60102m_flt.fits"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("F555W files = %v, want %v", got, want)
	}
	if _, ok := groups["25"]; ok {
		t.Fatal("DiscoverFilters grouped by numeric WFPC2 wheel position, want filter name")
	}
}

func TestDiscoverFilterFilesTracksProposalIDs(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFITSWithProposal(t, filepath.Join(dir, "rawa_flc.fits"), "F606W", "9978")
	writeMinimalFITSWithProposal(t, filepath.Join(dir, "rawb_flc.fits"), "F606W", "9978")
	writeMinimalFITSWithProposal(t, filepath.Join(dir, "rawc_flc.fits"), "F606W", "12060")
	writeMinimalFITSWithProposal(t, filepath.Join(dir, "rawd_flc.fits"), "F814W", "12060")

	filesByFilter, err := DiscoverFilterFiles(dir)
	if err != nil {
		t.Fatalf("DiscoverFilterFiles returned error: %v", err)
	}

	files := AllFilterFiles(filesByFilter)

	filterOptions := FilterFacetOptions(files)
	wantFilters := []string{"Any (4 files)", "F606W (3 files)", "F814W (1 files)"}
	if !reflect.DeepEqual(filterOptions, wantFilters) {
		t.Fatalf("FilterFacetOptions = %v, want %v", filterOptions, wantFilters)
	}

	// Proposal facet spans all files independently of the filter selection.
	proposalOptions := ProposalFacetOptions(files)
	wantProposals := []string{"Any (4 files)", "12060 (2 files)", "9978 (2 files)"}
	if !reflect.DeepEqual(proposalOptions, wantProposals) {
		t.Fatalf("ProposalFacetOptions = %v, want %v", proposalOptions, wantProposals)
	}

	paths := MatchFiles(files, FileCriteria{Filter: "F606W", ProposalID: "9978"})
	got := []string{filepath.Base(paths[0]), filepath.Base(paths[1])}
	want := []string{"rawa_flc.fits", "rawb_flc.fits"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MatchFiles(F606W,9978) = %v, want %v", got, want)
	}
}

func TestDiscoverFilterFilesTracksExposureTimes(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFITSFull(t, filepath.Join(dir, "rawa_flc.fits"), "F606W", "9978", "1230.0")
	writeMinimalFITSFull(t, filepath.Join(dir, "rawb_flc.fits"), "F606W", "9978", "500.0")
	writeMinimalFITSFull(t, filepath.Join(dir, "rawc_flc.fits"), "F606W", "12060", "1230.0")

	filesByFilter, err := DiscoverFilterFiles(dir)
	if err != nil {
		t.Fatalf("DiscoverFilterFiles returned error: %v", err)
	}

	files := AllFilterFiles(filesByFilter)

	// Exposure facet across all files, sorted ascending.
	expAll := ExposureFacetOptions(files)
	wantAll := []string{"Any (3 files)", "500s (1 files)", "1230s (2 files)"}
	if !reflect.DeepEqual(expAll, wantAll) {
		t.Fatalf("ExposureFacetOptions = %v, want %v", expAll, wantAll)
	}

	// Filter + a specific exposure (independent facets).
	paths := MatchFiles(files, FileCriteria{Filter: "F606W", Exposure: "1230s"})
	got := []string{filepath.Base(paths[0]), filepath.Base(paths[1])}
	want := []string{"rawa_flc.fits", "rawc_flc.fits"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MatchFiles(F606W,1230s) = %v, want %v", got, want)
	}

	// Proposal + exposure combined.
	paths = MatchFiles(files, FileCriteria{ProposalID: "9978", Exposure: "1230s"})
	if len(paths) != 1 || filepath.Base(paths[0]) != "rawa_flc.fits" {
		t.Fatalf("MatchFiles(9978,1230s) = %v, want [rawa_flc.fits]", paths)
	}
}

func TestDiscoverFilterFilesTracksObservationDates(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFITSDated(t, filepath.Join(dir, "rawa_flc.fits"), "F606W", "2009-07-25T14:03:11")
	writeMinimalFITSDated(t, filepath.Join(dir, "rawb_flc.fits"), "F606W", "2009-07-26")
	writeMinimalFITSDated(t, filepath.Join(dir, "rawc_flc.fits"), "F606W", "2010-01-02")

	filesByFilter, err := DiscoverFilterFiles(dir)
	if err != nil {
		t.Fatalf("DiscoverFilterFiles returned error: %v", err)
	}
	files := AllFilterFiles(filesByFilter)

	dates := DateValues(files)
	wantDates := []string{"2009-07-25", "2009-07-26", "2010-01-02"}
	if !reflect.DeepEqual(dates, wantDates) {
		t.Fatalf("DateValues = %v, want %v", dates, wantDates)
	}

	// Inclusive range bounded to the two July nights.
	paths := MatchFiles(files, FileCriteria{DateMin: "2009-07-25", DateMax: "2009-07-26"})
	got := []string{filepath.Base(paths[0]), filepath.Base(paths[1])}
	want := []string{"rawa_flc.fits", "rawb_flc.fits"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MatchFiles(date range) = %v, want %v", got, want)
	}
}

func TestDiscoverFiltersGroupsRealWFPC2FLTFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "TestImages", "WFPC2")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no WFPC2 FLT test image directory found")
	}

	groups, err := DiscoverFilters(dir)
	if err != nil {
		t.Fatalf("DiscoverFilters returned error: %v", err)
	}
	if got := len(groups["F555W"]); got != 2 {
		t.Fatalf("len(F555W) = %d, want 2", got)
	}
}

func TestDiscoverFiltersGroupsJWSTCalFiles(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFITS(t, filepath.Join(dir, "jw01_cal.fits"), "F770W")
	writeMinimalFITS(t, filepath.Join(dir, "jw02_cal.fits"), "F770W")
	writeMinimalFITS(t, filepath.Join(dir, "jw03_cal.fits"), "F1000W")

	groups, err := DiscoverFilters(dir)
	if err != nil {
		t.Fatalf("DiscoverFilters returned error: %v", err)
	}
	if got := len(groups["F770W"]); got != 2 {
		t.Fatalf("len(F770W) = %d, want 2", got)
	}
	if got := len(groups["F1000W"]); got != 1 {
		t.Fatalf("len(F1000W) = %d, want 1", got)
	}
}

func TestProductTypeRecognisesCal(t *testing.T) {
	cases := map[string]string{
		"ick909c1q_flc.fits": "flc",
		"u6l60101m_flt.fits": "flt",
		"jw01234_cal.fits":   "cal",
		"random.fits":        "",
	}
	for path, want := range cases {
		if got := ProductType(path); got != want {
			t.Fatalf("ProductType(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestAvailableProductTypesReportsCal(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFITS(t, filepath.Join(dir, "rawa_flc.fits"), "F606W")
	writeMinimalFITS(t, filepath.Join(dir, "jw01_cal.fits"), "F770W")

	filesByFilter, err := DiscoverFilterFiles(dir)
	if err != nil {
		t.Fatalf("DiscoverFilterFiles returned error: %v", err)
	}
	hasFLC, hasFLT, hasCal := AvailableProductTypes(filesByFilter)
	if !hasFLC || hasFLT || !hasCal {
		t.Fatalf("AvailableProductTypes = (flc=%t,flt=%t,cal=%t), want (true,false,true)", hasFLC, hasFLT, hasCal)
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

func writeMinimalFITSWithProposal(t *testing.T, path string, filter string, proposalID string) {
	t.Helper()
	content := makeHeaderCard("SIMPLE", "=                    T") +
		makeHeaderCard("BITPIX", "=                    8") +
		makeHeaderCard("NAXIS", "=                    0") +
		makeHeaderCard("FILTER", "= '"+padFilter(filter)+"'") +
		makeHeaderCard("PROPOSID", "= "+proposalID) +
		makeHeaderCard("END", "")
	for len(content)%2880 != 0 {
		content += strings.Repeat(" ", 2880-(len(content)%2880))
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeMinimalFITSWithProposal: %v", err)
	}
}

func writeMinimalFITSFull(t *testing.T, path, filter, proposalID, exptime string) {
	t.Helper()
	content := makeHeaderCard("SIMPLE", "=                    T") +
		makeHeaderCard("BITPIX", "=                    8") +
		makeHeaderCard("NAXIS", "=                    0") +
		makeHeaderCard("FILTER", "= '"+padFilter(filter)+"'") +
		makeHeaderCard("PROPOSID", "= "+proposalID) +
		makeHeaderCard("EXPTIME", "= "+exptime) +
		makeHeaderCard("END", "")
	for len(content)%2880 != 0 {
		content += strings.Repeat(" ", 2880-(len(content)%2880))
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeMinimalFITSFull: %v", err)
	}
}

func writeMinimalFITSDated(t *testing.T, path, filter, dateObs string) {
	t.Helper()
	content := makeHeaderCard("SIMPLE", "=                    T") +
		makeHeaderCard("BITPIX", "=                    8") +
		makeHeaderCard("NAXIS", "=                    0") +
		makeHeaderCard("FILTER", "= '"+padFilter(filter)+"'") +
		makeHeaderCard("DATE-OBS", "= '"+dateObs+"'") +
		makeHeaderCard("END", "")
	for len(content)%2880 != 0 {
		content += strings.Repeat(" ", 2880-(len(content)%2880))
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeMinimalFITSDated: %v", err)
	}
}

func writeMinimalWFPC2FITS(t *testing.T, path string, filter string) {
	t.Helper()
	content := makeHeaderCard("SIMPLE", "=                    T") +
		makeHeaderCard("BITPIX", "=                    8") +
		makeHeaderCard("NAXIS", "=                    0") +
		makeHeaderCard("INSTRUME", "= 'WFPC2   '") +
		makeHeaderCard("FILTNAM1", "= '"+padFilter(filter)+"'") +
		makeHeaderCard("FILTNAM2", "= '        '") +
		makeHeaderCard("FILTER1", "=                   25") +
		makeHeaderCard("FILTER2", "=                    0") +
		makeHeaderCard("END", "")
	for len(content)%2880 != 0 {
		content += strings.Repeat(" ", 2880-(len(content)%2880))
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeMinimalWFPC2FITS: %v", err)
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
