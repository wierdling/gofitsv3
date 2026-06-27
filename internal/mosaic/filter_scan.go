package mosaic

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gofitsv3/internal/fitsio"
)

var pipelineFLCPattern = regexp.MustCompile(`(?i)\d{2}_flc\.fits$`)

type FilterFile struct {
	Path string
	// Filter is the filter name this file was grouped under (the map key),
	// duplicated here so a flattened file list is self-describing.
	Filter     string
	ProposalID string
	// ExposureTime is the formatted exposure duration (e.g. "1230s") read from
	// the primary header, or "Unknown" when absent.
	ExposureTime string
	// DateObs is the observation calendar date ("YYYY-MM-DD") parsed from the
	// DATE-OBS header, or "" when absent. Used for range filtering.
	DateObs string
}

func IsPipelineProductFLC(path string) bool {
	return pipelineFLCPattern.MatchString(filepath.Base(path))
}

func DiscoverFilters(dir string) (map[string][]string, error) {
	filesByFilter, err := DiscoverFilterFiles(dir)
	if err != nil {
		return nil, err
	}

	groups := make(map[string][]string, len(filesByFilter))
	for filter, files := range filesByFilter {
		for _, file := range files {
			groups[filter] = append(groups[filter], file.Path)
		}
	}
	return groups, nil
}

func DiscoverFilterFiles(dir string) (map[string][]FilterFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	groups := make(map[string][]FilterFile)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		if !LooksLikeFLC(path) || IsPipelineProductFLC(path) {
			continue
		}

		header, err := fitsio.LoadPrimaryHeader(path)
		if err != nil {
			continue
		}
		filter := fitsio.FilterString(header)
		if filter == "" {
			filter = "Unknown"
		}
		proposalID := fitsio.HeaderString(header, "PROPOSID", "PROPOSAL", "PROPOSALID")
		if proposalID == "" {
			proposalID = "Unknown"
		}
		exposure := formatExposure(loadExposureTime(header))
		dateObs := parseDateObs(fitsio.HeaderString(header, "DATE-OBS", "DATEOBS"))
		groups[filter] = append(groups[filter], FilterFile{Path: path, Filter: filter, ProposalID: proposalID, ExposureTime: exposure, DateObs: dateObs})
	}

	for filter := range groups {
		sort.Slice(groups[filter], func(i, j int) bool {
			return groups[filter][i].Path < groups[filter][j].Path
		})
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("no matching calibrated _flc/_flt FITS files with filter headers found in %s", dir)
	}
	return groups, nil
}

func FilterOptions(groups map[string][]string) []string {
	options := make([]string, 0, len(groups))
	for filter, paths := range groups {
		options = append(options, fmt.Sprintf("%s (%d files)", filter, len(paths)))
	}
	sort.Strings(options)
	return options
}

func PathsForFilterOption(groups map[string][]string, option string) []string {
	filter := option
	if idx := strings.LastIndex(option, " ("); idx >= 0 {
		filter = option[:idx]
	}
	return append([]string(nil), groups[filter]...)
}

// AllFilterFiles flattens the per-filter map into a single slice, sorted by
// path. Each FilterFile already carries its Filter name, so the result is a
// self-describing list suitable for order-independent faceted filtering.
func AllFilterFiles(groups map[string][]FilterFile) []FilterFile {
	files := make([]FilterFile, 0)
	for _, group := range groups {
		files = append(files, group...)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// FileCriteria is an order-independent set of facet constraints. Empty/zero
// fields impose no constraint, so any subset of facets can be applied in any
// combination.
type FileCriteria struct {
	Filter      string // exact filter name, "" = any
	ProposalID  string // exact proposal ID, "" = any
	Exposure    string // exact exposure label (e.g. "1230s"), "" = any
	DateMin     string // inclusive "YYYY-MM-DD" lower bound, "" = no lower bound
	DateMax     string // inclusive "YYYY-MM-DD" upper bound, "" = no upper bound
	ProductType string // "flc"/"flt", "" = any
}

// MatchFiles returns the paths of files satisfying every non-empty facet in c.
// Files with an unknown (empty) DateObs always pass the date range so they are
// never silently dropped for lacking a header keyword.
func MatchFiles(files []FilterFile, c FileCriteria) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if c.Filter != "" && f.Filter != c.Filter {
			continue
		}
		if c.ProposalID != "" && f.ProposalID != c.ProposalID {
			continue
		}
		if c.Exposure != "" && f.ExposureTime != c.Exposure {
			continue
		}
		if f.DateObs != "" {
			if c.DateMin != "" && f.DateObs < c.DateMin {
				continue
			}
			if c.DateMax != "" && f.DateObs > c.DateMax {
				continue
			}
		}
		if c.ProductType != "" && ProductType(f.Path) != c.ProductType {
			continue
		}
		paths = append(paths, f.Path)
	}
	return paths
}

// FilterFacetOptions, ProposalFacetOptions, and ExposureFacetOptions each build
// a dropdown option list for one independent facet: an "Any (<total>)" entry
// followed by every distinct value with its file count. Use FacetValue to
// recover the bare value (or "" for "Any") from a selected option.
func FilterFacetOptions(files []FilterFile) []string {
	return facetOptions(files, func(f FilterFile) string { return f.Filter }, func(a, b string) bool { return a < b })
}

func ProposalFacetOptions(files []FilterFile) []string {
	return facetOptions(files, func(f FilterFile) string { return f.ProposalID }, func(a, b string) bool { return a < b })
}

func ExposureFacetOptions(files []FilterFile) []string {
	return facetOptions(files, func(f FilterFile) string { return f.ExposureTime }, exposureLess)
}

// DateValues returns the sorted distinct known observation dates ("YYYY-MM-DD"),
// for populating the min/max date range selects. Files without a DATE-OBS are
// omitted.
func DateValues(files []FilterFile) []string {
	seen := make(map[string]struct{})
	dates := make([]string, 0)
	for _, f := range files {
		if f.DateObs == "" {
			continue
		}
		if _, ok := seen[f.DateObs]; ok {
			continue
		}
		seen[f.DateObs] = struct{}{}
		dates = append(dates, f.DateObs)
	}
	sort.Strings(dates)
	return dates
}

// FacetValue recovers the bare facet value from a selected option produced by
// the *FacetOptions helpers, returning "" for the "Any ..." aggregate entry.
func FacetValue(option string) string {
	v := filterFromOption(option)
	if v == "Any" {
		return ""
	}
	return v
}

func facetOptions(files []FilterFile, value func(FilterFile) string, less func(a, b string) bool) []string {
	counts := make(map[string]int)
	for _, f := range files {
		v := strings.TrimSpace(value(f))
		if v == "" {
			v = "Unknown"
		}
		counts[v]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return less(keys[i], keys[j]) })
	options := make([]string, 0, len(keys)+1)
	options = append(options, fmt.Sprintf("Any (%d files)", len(files)))
	for _, k := range keys {
		options = append(options, fmt.Sprintf("%s (%d files)", k, counts[k]))
	}
	return options
}

// parseDateObs normalises a DATE-OBS header value to a "YYYY-MM-DD" calendar
// date, stripping any time component ("2009-07-25T14:03:11" -> "2009-07-25").
// Returns "" when the value is missing or not date-shaped.
func parseDateObs(raw string) string {
	date := strings.TrimSpace(raw)
	if i := strings.IndexAny(date, "Tt "); i >= 0 {
		date = date[:i]
	}
	if len(date) != 10 || date[4] != '-' || date[7] != '-' {
		return ""
	}
	return date
}

// formatExposure renders an exposure duration in seconds as a compact label
// (e.g. "1230s"), or "Unknown" when the value is missing or invalid.
func formatExposure(sec float64) string {
	if sec <= 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return "Unknown"
	}
	return strconv.FormatFloat(sec, 'f', -1, 64) + "s"
}

// exposureLess orders exposure labels numerically, placing "Unknown" (and any
// non-numeric label) after all numeric exposures.
func exposureLess(a, b string) bool {
	av, aok := parseExposureSeconds(a)
	bv, bok := parseExposureSeconds(b)
	if aok && bok {
		return av < bv
	}
	if aok != bok {
		return aok
	}
	return a < b
}

func parseExposureSeconds(label string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(label), "s"), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func filterFromOption(option string) string {
	if idx := strings.LastIndex(option, " ("); idx >= 0 {
		return option[:idx]
	}
	return option
}

// ProductType reports the calibrated product type of an input path, "flc" or
// "flt", or "" when the filename matches neither.
func ProductType(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(base, "_flc"):
		return "flc"
	case strings.Contains(base, "_flt"):
		return "flt"
	default:
		return ""
	}
}

// AvailableProductTypes reports whether any discovered file is an _flc and/or an
// _flt product, across all filters.
func AvailableProductTypes(filesByFilter map[string][]FilterFile) (hasFLC, hasFLT bool) {
	for _, files := range filesByFilter {
		for _, file := range files {
			switch ProductType(file.Path) {
			case "flc":
				hasFLC = true
			case "flt":
				hasFLT = true
			}
			if hasFLC && hasFLT {
				return
			}
		}
	}
	return
}
