package mosaic

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gofitsv3/internal/fitsio"
)

var pipelineFLCPattern = regexp.MustCompile(`(?i)\d{2}_flc\.fits$`)

func IsPipelineProductFLC(path string) bool {
	return pipelineFLCPattern.MatchString(filepath.Base(path))
}

func DiscoverFilters(dir string) (map[string][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	groups := make(map[string][]string)
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
		filter := fitsio.HeaderString(header, "FILTER", "FILTER1", "FILTER2")
		if filter == "" {
			filter = "Unknown"
		}
		groups[filter] = append(groups[filter], path)
	}

	for filter := range groups {
		sort.Strings(groups[filter])
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("no matching raw _flc.fits files with filter headers found in %s", dir)
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
