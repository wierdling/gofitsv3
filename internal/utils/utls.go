package utils

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gofitsv3/internal/fitsio"
)

func Clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func ParseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func ClampLevel(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func FormatHeadersLines(primary, sci fitsio.Header) []string {
	lines := make([]string, 0, len(primary.Cards)+len(sci.Cards)+4)
	lines = append(lines, "Primary header")
	for _, key := range sortedKeys(primary.Cards) {
		lines = append(lines, fmt.Sprintf("%-8s = %s", key, primary.Cards[key]))
	}
	lines = append(lines, "")
	lines = append(lines, "SCI header")
	for _, key := range sortedKeys(sci.Cards) {
		lines = append(lines, fmt.Sprintf("%-8s = %s", key, sci.Cards[key]))
	}
	return lines
}
